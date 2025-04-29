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

type EC2Handler struct {
	stsClient *sts.Client
	accountID string
}

func NewHandler(cfg aws.Config) *EC2Handler {
	stsClient := sts.NewFromConfig(cfg)
	return &EC2Handler{
		stsClient: stsClient,
	}
}

func (h *EC2Handler) Kind() domain.ResourceKind {
	return domain.KindComputeInstance
}

func (h *EC2Handler) getAccountID(ctx context.Context) (string, error) {
	if h.accountID != "" {
		return h.accountID, nil
	}
	input := &sts.GetCallerIdentityInput{}
	output, err := h.stsClient.GetCallerIdentity(ctx, input)
	if err != nil {
		return "", apperrors.Wrap(err, apperrors.CodePlatformAPIError, "failed to get AWS caller identity")
	}
	if output.Account == nil {
		return "", apperrors.New(apperrors.CodePlatformAPIError, "AWS caller identity response did not contain Account ID")
	}
	h.accountID = *output.Account
	return h.accountID, nil
}

func (h *EC2Handler) ListResources(
	ctx context.Context,
	cfg aws.Config,
	filters map[string]string,
	logger ports.Logger,
	out chan<- domain.PlatformResource,
) error {
	client := ec2.NewFromConfig(cfg)
	ec2Filters := buildEC2Filters(filters)

	input := &ec2.DescribeInstancesInput{Filters: ec2Filters}
	paginator := ec2.NewDescribeInstancesPaginator(client, input)

	accountID, err := h.getAccountID(ctx)
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
				mappedResource, mapErr := mapInstanceToDomain(instance, cfg.Region, accountID, ctx, cfg)
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
	client := ec2.NewFromConfig(cfg)
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

	accountID, accErr := h.getAccountID(ctx)
	if accErr != nil {
		logger.Warnf(ctx, "Proceeding without AWS Account ID for GetResource due to STS error: %v", accErr)
		accountID = ""
	}

	mappedResource, mapErr := mapInstanceToDomain(instance, cfg.Region, accountID, ctx, cfg)
	if mapErr != nil {
		return nil, apperrors.Wrap(mapErr, apperrors.CodeInternal, fmt.Sprintf("failed to map EC2 instance %s after retrieval", id))
	}

	return mappedResource, nil
}
