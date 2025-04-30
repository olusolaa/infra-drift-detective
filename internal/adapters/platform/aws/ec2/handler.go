package ec2

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/util"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
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

func (h *EC2Handler) getAccountID(ctx context.Context, logger ports.Logger) (string, error) {
	if h.accountID != "" {
		return h.accountID, nil
	}
	if err := util.Wait(ctx, logger); err != nil {
		return "", err
	}
	input := &sts.GetCallerIdentityInput{}
	output, err := h.stsClient.GetCallerIdentity(ctx, input)
	if err != nil {
		return "", errors.Wrap(err, errors.CodePlatformAPIError, "failed to get AWS caller identity")
	}
	if output.Account == nil {
		return "", errors.New(errors.CodePlatformAPIError, "AWS caller identity response did not contain Account ID")
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
	ec2Filters := BuildEC2Filters(filters)
	input := &ec2.DescribeInstancesInput{Filters: ec2Filters}
	paginator := ec2.NewDescribeInstancesPaginator(client, input)
	accountID, _ := h.getAccountID(ctx, logger)

	pageNum := 0
	instanceCount := 0
	for paginator.HasMorePages() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		pageNum++
		logger.Debugf(ctx, "Fetching EC2 instances page %d", pageNum)
		if err := util.Wait(ctx, logger); err != nil {
			return err
		}
		output, err := paginator.NextPage(ctx)
		if err != nil {
			if strings.Contains(err.Error(), "AuthFailure") || strings.Contains(err.Error(), "UnauthorizedOperation") {
				return errors.Wrap(err, errors.CodePlatformAuthError, "AWS authentication error describing EC2 instances")
			}
			return errors.Wrap(err, errors.CodePlatformAPIError, fmt.Sprintf("failed to describe EC2 instances (page %d)", pageNum))
		}

		for _, reservation := range output.Reservations {
			for _, instance := range reservation.Instances {
				resource, mapErr := newEc2InstanceResource(instance, cfg, cfg.Region, accountID, logger)
				if mapErr != nil {
					logger.Errorf(ctx, mapErr, "Failed create resource wrapper for instance %s, skipping", aws.ToString(instance.InstanceId))
					continue
				}

				select {
				case out <- resource:
					instanceCount++
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
	logger.Debugf(ctx, "Finished EC2 pagination, sent %d resources.", instanceCount)
	return nil
}

func (h *EC2Handler) GetResource(ctx context.Context, cfg aws.Config, id string, logger ports.Logger) (domain.PlatformResource, error) {
	client := ec2.NewFromConfig(cfg)
	input := &ec2.DescribeInstancesInput{InstanceIds: []string{id}}

	logger.Debugf(ctx, "Describing instance %s", id)
	if err := util.Wait(ctx, logger); err != nil {
		return nil, err
	}
	output, err := client.DescribeInstances(ctx, input)
	if err != nil {
		var apiErr smithy.APIError // Use smithy.APIError for AWS SDK v2 errors
		if stderrors.As(err, &apiErr) && (apiErr.ErrorCode() == "InvalidInstanceID.NotFound" || apiErr.ErrorCode() == "InvalidInstanceID.Malformed") {
			return nil, errors.Wrap(err, errors.CodeResourceNotFound, fmt.Sprintf("EC2 instance ID '%s' not found or malformed", id))
		}
		if strings.Contains(err.Error(), "AuthFailure") || strings.Contains(err.Error(), "UnauthorizedOperation") {
			return nil, errors.Wrap(err, errors.CodePlatformAuthError, fmt.Sprintf("AWS authentication error describing EC2 instance %s", id))
		}
		return nil, errors.Wrap(err, errors.CodePlatformAPIError, fmt.Sprintf("failed to describe EC2 instance with ID '%s'", id))
	}
	if len(output.Reservations) == 0 || len(output.Reservations[0].Instances) == 0 { /* ... error handling ... */
		return nil, errors.New(errors.CodeResourceNotFound, fmt.Sprintf("Instance %s not found", id))
	}
	instance := output.Reservations[0].Instances[0]

	accountID, _ := h.getAccountID(ctx, logger)

	resource, mapErr := newEc2InstanceResource(instance, cfg, cfg.Region, accountID, logger)
	if mapErr != nil {
		return nil, errors.Wrap(mapErr, errors.CodeInternal, fmt.Sprintf("failed to create resource wrapper for %s", id))
	}

	return resource, nil
}
