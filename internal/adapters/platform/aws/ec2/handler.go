package ec2

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	apperrors "github.com/olusolaa/infra-drift-detector/internal/errors"
)

type EC2InstancesPaginator interface {
	HasMorePages() bool
	NextPage(ctx context.Context, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

type STSClientInterface interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

type EC2ClientInterface interface {
	DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

type EC2Handler struct {
	STSClient        STSClientInterface
	accountID        string
	PaginatorFactory func(client EC2ClientInterface, input *ec2.DescribeInstancesInput) EC2InstancesPaginator
	EC2ClientFactory func(cfg aws.Config) EC2ClientInterface
	CurrentEC2Client EC2ClientInterface
}

func NewHandler(cfg aws.Config) *EC2Handler {
	stsClient := sts.NewFromConfig(cfg)
	return &EC2Handler{
		STSClient: stsClient,
		PaginatorFactory: func(client EC2ClientInterface, input *ec2.DescribeInstancesInput) EC2InstancesPaginator {
			return ec2.NewDescribeInstancesPaginator(client.(*ec2.Client), input)
		},
		EC2ClientFactory: func(cfg aws.Config) EC2ClientInterface {
			return ec2.NewFromConfig(cfg)
		},
	}
}

func (h *EC2Handler) Kind() domain.ResourceKind {
	return domain.KindComputeInstance
}

func (h *EC2Handler) GetAccountID(ctx context.Context) (string, error) {
	if h.accountID != "" {
		return h.accountID, nil
	}
	input := &sts.GetCallerIdentityInput{}
	output, err := h.STSClient.GetCallerIdentity(ctx, input)
	if err != nil {
		return "", apperrors.Wrap(err, apperrors.CodePlatformAPIError, "failed to get AWS caller identity")
	}
	if output.Account == nil {
		return "", apperrors.New(apperrors.CodePlatformAPIError, "AWS caller identity response did not contain Account ID")
	}
	h.accountID = *output.Account
	return h.accountID, nil
}

func (h *EC2Handler) SetAccountID(accountID string) {
	h.accountID = accountID
}

func (h *EC2Handler) ListResources(
	ctx context.Context,
	cfg aws.Config,
	filters map[string]string,
	logger ports.Logger,
	out chan<- domain.PlatformResource,
) error {
	client := h.EC2ClientFactory(cfg)
	ec2Filters := BuildEC2Filters(filters)

	input := &ec2.DescribeInstancesInput{Filters: ec2Filters}
	paginator := h.PaginatorFactory(client, input)

	accountID, err := h.GetAccountID(ctx)
	if err != nil {
		logger.Warnf(ctx, "Proceeding without AWS Account ID due to STS error: %v", err)
		accountID = ""
	}

	pageNum := 0
	instanceCount := 0
	for paginator.HasMorePages() {
		select {
		case <-ctx.Done():
			logger.Warnf(ctx, "Context cancelled during EC2 instance pagination")
			return ctx.Err()
		default:
		}

		pageNum++
		logger.Debugf(ctx, "Fetching EC2 instances page %d", pageNum)
		output, err := paginator.NextPage(ctx)
		if err != nil {
			if strings.Contains(err.Error(), "AuthFailure") || strings.Contains(err.Error(), "UnauthorizedOperation") {
				return apperrors.Wrap(err, apperrors.CodePlatformAuthError, "AWS authentication error describing EC2 instances")
			}
			return apperrors.Wrap(err, apperrors.CodePlatformAPIError, fmt.Sprintf("failed to describe EC2 instances (page %d)", pageNum))
		}

		for _, reservation := range output.Reservations {
			for _, instance := range reservation.Instances {
				mappedResource, mapErr := MapInstanceToDomain(instance, cfg.Region, accountID, ctx, cfg)
				if mapErr != nil {
					logger.Errorf(ctx, mapErr, "Failed to map EC2 instance %s, skipping", *instance.InstanceId)
					continue
				}

				select {
				case out <- mappedResource:
					instanceCount++
				case <-ctx.Done():
					logger.Warnf(ctx, "Context cancelled while sending EC2 instance %s to channel", *instance.InstanceId)
					return ctx.Err()
				}
			}
		}
	}
	logger.Debugf(ctx, "Finished paginating EC2 instances, found %d total.", instanceCount)
	return nil
}

func (h *EC2Handler) GetResource(ctx context.Context, cfg aws.Config, id string, logger ports.Logger) (domain.PlatformResource, error) {
	var client EC2ClientInterface
	if h.CurrentEC2Client != nil {
		client = h.CurrentEC2Client
	} else {
		client = h.EC2ClientFactory(cfg)
	}

	input := &ec2.DescribeInstancesInput{InstanceIds: []string{id}}
	output, err := client.DescribeInstances(ctx, input)
	if err != nil {
		var apiErr interface{ ErrorCode() string }
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "InvalidInstanceID.NotFound" || apiErr.ErrorCode() == "InvalidInstanceID.Malformed") {
			return nil, apperrors.Wrap(err, apperrors.CodeResourceNotFound, fmt.Sprintf("EC2 instance with ID '%s' not found or ID malformed", id))
		}
		if strings.Contains(err.Error(), "AuthFailure") || strings.Contains(err.Error(), "UnauthorizedOperation") {
			return nil, apperrors.Wrap(err, apperrors.CodePlatformAuthError, "AWS authentication error describing EC2 instance")
		}
		return nil, apperrors.Wrap(err, apperrors.CodePlatformAPIError, fmt.Sprintf("failed to describe EC2 instance with ID '%s'", id))
	}
	if len(output.Reservations) == 0 || len(output.Reservations[0].Instances) == 0 {
		return nil, apperrors.New(apperrors.CodeResourceNotFound, fmt.Sprintf("EC2 instance with ID '%s' not found (empty response)", id))
	}
	instance := output.Reservations[0].Instances[0]

	accountID, accErr := h.GetAccountID(ctx)
	if accErr != nil {
		logger.Warnf(ctx, "Proceeding without AWS Account ID for GetResource due to STS error: %v", accErr)
		accountID = ""
	}

	mappedResource, mapErr := MapInstanceToDomain(instance, cfg.Region, accountID, ctx, cfg)
	if mapErr != nil {
		return nil, apperrors.Wrap(mapErr, apperrors.CodeInternal, fmt.Sprintf("failed to map EC2 instance %s after retrieval", id))
	}

	return mappedResource, nil
}
