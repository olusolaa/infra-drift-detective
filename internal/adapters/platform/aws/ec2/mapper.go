package ec2

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	aws_errors "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/errors"

	aws_limiter "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/limiter"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	iddErrors "github.com/olusolaa/infra-drift-detector/internal/errors"
)

type ec2InstanceResource struct {
	mu              sync.RWMutex
	meta            domain.ResourceMetadata
	rawInstance     Instance
	logger          ports.Logger
	ec2Client       EC2ClientInterface
	builtAttrs      map[string]any
	fetchErr        error
	attributesBuilt bool
}

func newEc2InstanceResource(
	instance Instance,
	region string,
	accountID string,
	logger ports.Logger,
	client EC2ClientInterface,
) (domain.PlatformResource, error) {

	meta := domain.ResourceMetadata{
		Kind:               domain.KindComputeInstance,
		ProviderType:       "aws",
		ProviderAssignedID: aws.ToString(instance.InstanceId),
		SourceIdentifier:   aws.ToString(instance.InstanceId),
		AccountID:          accountID,
		Region:             region,
	}

	if meta.ProviderAssignedID == "" {
		return nil, iddErrors.New(iddErrors.CodeInternal, "failed to create EC2 resource: missing instance ID")
	}

	return &ec2InstanceResource{
		meta:        meta,
		rawInstance: instance,
		logger:      logger.WithFields(map[string]any{"instance_id": meta.ProviderAssignedID}),
		ec2Client:   client,
	}, nil
}

func (r *ec2InstanceResource) Metadata() domain.ResourceMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.meta
}

func (r *ec2InstanceResource) Attributes(ctx context.Context) (map[string]any, error) {
	r.mu.RLock()
	if r.attributesBuilt {
		builtAttrsCopy := r.copyAttributeMap(r.builtAttrs)
		fetchErr := r.fetchErr
		r.mu.RUnlock()
		return builtAttrsCopy, fetchErr
	}
	bdms := r.rawInstance.BlockDeviceMappings
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.attributesBuilt {
		return r.copyAttributeMap(r.builtAttrs), r.fetchErr
	}

	r.builtAttrs = mapInstanceToAttributes(r.rawInstance, r.logger)
	r.fetchErr = r.fetchAndMapAdditionalAttributes(ctx, bdms)
	r.attributesBuilt = true

	return r.copyAttributeMap(r.builtAttrs), r.fetchErr
}

func (r *ec2InstanceResource) copyAttributeMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (r *ec2InstanceResource) fetchAndMapAdditionalAttributes(ctx context.Context, instanceBDMs []ec2types.InstanceBlockDeviceMapping) error {
	if r.ec2Client == nil {
		return iddErrors.New(iddErrors.CodeInternal, "EC2 client not available for fetching additional attributes")
	}

	instanceID := r.meta.ProviderAssignedID
	var wg sync.WaitGroup
	var fetchErrors []error
	var errMu sync.Mutex

	var fetchedUserData string
	var userDataFetched bool
	var fetchedVolumes map[string]ec2types.Volume
	var volumesFetched bool

	addError := func(err error) {
		errMu.Lock()
		fetchErrors = append(fetchErrors, err)
		errMu.Unlock()
	}

	wg.Add(2)

	go func() {
		defer wg.Done()
		if err := aws_limiter.Wait(ctx, r.logger); err != nil {
			addError(iddErrors.Wrap(err, iddErrors.CodePlatformAPIError, "rate limit error before UserData fetch"))
			return
		}
		userDataInput := &ec2.DescribeInstanceAttributeInput{
			InstanceId: aws.String(instanceID),
			Attribute:  ec2types.InstanceAttributeNameUserData,
		}
		output, err := r.ec2Client.DescribeInstanceAttribute(ctx, userDataInput)
		if err != nil {
			wrappedErr := aws_errors.HandleAWSError("EC2 UserData", instanceID, err, ctx)
			r.logger.Warnf(ctx, "Failed to fetch UserData: %v", wrappedErr)
			addError(wrappedErr)
			return
		}
		if output != nil && output.UserData != nil && output.UserData.Value != nil {
			decoded, decodeErr := base64.StdEncoding.DecodeString(*output.UserData.Value)
			if decodeErr != nil {
				wrappedErr := iddErrors.Wrap(decodeErr, iddErrors.CodeInternal, "failed to decode user data")
				r.logger.Warnf(ctx, "Failed to decode UserData for instance %s: %v", instanceID, wrappedErr)
				addError(wrappedErr)
				return
			}
			fetchedUserData = string(decoded)
			userDataFetched = true
		}
	}()

	go func() {
		defer wg.Done()
		volumeIDs := make([]string, 0)
		for _, bdm := range instanceBDMs {
			if bdm.Ebs != nil && bdm.Ebs.VolumeId != nil {
				volumeIDs = append(volumeIDs, *bdm.Ebs.VolumeId)
			}
		}

		if len(volumeIDs) == 0 {
			return // No EBS volumes attached
		}

		if err := aws_limiter.Wait(ctx, r.logger); err != nil {
			addError(iddErrors.Wrap(err, iddErrors.CodePlatformAPIError, "rate limit error before EBS volume fetch"))
			return
		}
		volumesInput := &ec2.DescribeVolumesInput{VolumeIds: volumeIDs}
		output, err := r.ec2Client.DescribeVolumes(ctx, volumesInput)
		if err != nil {
			wrappedErr := aws_errors.HandleAWSError("EC2 EBS Volumes", instanceID, err, ctx)
			r.logger.Warnf(ctx, "Failed to describe EBS volumes: %v", wrappedErr)
			addError(wrappedErr)
			return
		}

		if output != nil && len(output.Volumes) > 0 {
			fetchedVolumes = make(map[string]ec2types.Volume)
			for _, vol := range output.Volumes {
				fetchedVolumes[aws.ToString(vol.VolumeId)] = vol
			}
			volumesFetched = true
		}
	}()

	wg.Wait()

	if userDataFetched {
		r.builtAttrs["user_data"] = fetchedUserData
	}

	if volumesFetched {
		if bdmList, ok := r.builtAttrs["block_device_mappings"].([]map[string]any); ok {
			newBdmList := make([]map[string]any, len(bdmList))
			for i, bdm := range bdmList {
				newBdm := make(map[string]any)
				for k, v := range bdm {
					newBdm[k] = v
				}

				if ebsMapUntyped, ebsOk := newBdm["ebs"]; ebsOk {
					if ebsMap, ebsMapOk := ebsMapUntyped.(map[string]any); ebsMapOk {
						newEbsMap := make(map[string]any)
						for k, v := range ebsMap {
							newEbsMap[k] = v
						}

						if volID, idOk := newEbsMap["volume_id"].(string); idOk {
							if volDetails, volOk := fetchedVolumes[volID]; volOk {
								newEbsMap["size"] = volDetails.Size
								newEbsMap["iops"] = volDetails.Iops
								newEbsMap["throughput"] = volDetails.Throughput
								newEbsMap["encrypted"] = volDetails.Encrypted
								newEbsMap["kms_key_id"] = volDetails.KmsKeyId
							}
						}
						newBdm["ebs"] = newEbsMap
					}
				}
				newBdmList[i] = newBdm
			}
			r.builtAttrs["block_device_mappings"] = newBdmList
		}
	}

	if len(fetchErrors) > 0 {
		combinedErr := errors.Join(fetchErrors...)
		return iddErrors.Wrap(combinedErr, iddErrors.CodePlatformAPIError, fmt.Sprintf("failed to fetch all attributes for instance %s", r.meta.ProviderAssignedID))
	}

	return nil
}

func mapInstanceToAttributes(instance Instance, logger ports.Logger) map[string]any {
	attrs := map[string]any{}

	if instance.InstanceId != nil {
		attrs[domain.KeyID] = *instance.InstanceId
	}
	if instance.ImageId != nil {
		attrs["image_id"] = *instance.ImageId
	}
	if instance.InstanceType != "" {
		attrs["instance_type"] = string(instance.InstanceType)
	}
	if instance.KeyName != nil {
		attrs["key_name"] = *instance.KeyName
	}
	if instance.LaunchTime != nil {
		attrs["launch_time"] = instance.LaunchTime.UTC().Format(time.RFC3339)
	}
	if instance.PrivateDnsName != nil {
		attrs["private_dns_name"] = *instance.PrivateDnsName
	}
	if instance.PrivateIpAddress != nil {
		attrs["private_ip_address"] = *instance.PrivateIpAddress
	}
	if instance.PublicDnsName != nil {
		attrs["public_dns_name"] = *instance.PublicDnsName
	}
	if instance.PublicIpAddress != nil {
		attrs["public_ip_address"] = *instance.PublicIpAddress
	}
	if instance.SubnetId != nil {
		attrs["subnet_id"] = *instance.SubnetId
	}
	if instance.VpcId != nil {
		attrs["vpc_id"] = *instance.VpcId
	}
	if instance.Architecture != "" {
		attrs["architecture"] = string(instance.Architecture)
	}
	if instance.RootDeviceName != nil {
		attrs["root_device_name"] = *instance.RootDeviceName
	}
	if instance.RootDeviceType != "" {
		attrs["root_device_type"] = string(instance.RootDeviceType)
	}
	if instance.State != nil && instance.State.Name != "" {
		attrs["state"] = string(instance.State.Name)
	}
	if instance.StateTransitionReason != nil {
		attrs["state_reason"] = *instance.StateTransitionReason
	}
	if instance.EnaSupport != nil {
		attrs["ena_support"] = *instance.EnaSupport
	}
	if instance.Hypervisor != "" {
		attrs["hypervisor"] = string(instance.Hypervisor)
	}
	if instance.IamInstanceProfile != nil && instance.IamInstanceProfile.Arn != nil {
		attrs["iam_instance_profile_arn"] = *instance.IamInstanceProfile.Arn
	}
	if instance.InstanceLifecycle != "" {
		attrs["instance_lifecycle"] = string(instance.InstanceLifecycle)
	}
	if instance.PlatformDetails != nil {
		attrs["platform_details"] = *instance.PlatformDetails
	}
	if instance.VirtualizationType != "" {
		attrs["virtualization_type"] = string(instance.VirtualizationType)
	}
	if instance.CpuOptions != nil {
		cpuOpts := map[string]any{}
		if instance.CpuOptions.CoreCount != nil {
			cpuOpts["core_count"] = *instance.CpuOptions.CoreCount
		}
		if instance.CpuOptions.ThreadsPerCore != nil {
			cpuOpts["threads_per_core"] = *instance.CpuOptions.ThreadsPerCore
		}
		attrs["cpu_options"] = cpuOpts
	}
	if instance.CapacityReservationSpecification != nil && instance.CapacityReservationSpecification.CapacityReservationPreference != "" {
		attrs["capacity_reservation_preference"] = string(instance.CapacityReservationSpecification.CapacityReservationPreference)
	}
	if instance.HibernationOptions != nil && instance.HibernationOptions.Configured != nil {
		attrs["hibernation_enabled"] = *instance.HibernationOptions.Configured
	}

	if len(instance.SecurityGroups) > 0 {
		sgs := make([]map[string]string, len(instance.SecurityGroups))
		for i, sg := range instance.SecurityGroups {
			sgs[i] = map[string]string{
				"id":   aws.ToString(sg.GroupId),
				"name": aws.ToString(sg.GroupName),
			}
		}
		attrs["security_groups"] = sgs
	}

	if len(instance.BlockDeviceMappings) > 0 {
		bdms := make([]map[string]any, len(instance.BlockDeviceMappings))
		for i, bdm := range instance.BlockDeviceMappings {
			bdmMap := map[string]any{
				"device_name": aws.ToString(bdm.DeviceName),
			}
			if bdm.Ebs != nil {
				ebsMap := map[string]any{
					"volume_id":             aws.ToString(bdm.Ebs.VolumeId),
					"status":                string(bdm.Ebs.Status),
					"attach_time":           aws.ToTime(bdm.Ebs.AttachTime).UTC().Format(time.RFC3339),
					"delete_on_termination": aws.ToBool(bdm.Ebs.DeleteOnTermination),
					"size":                  nil,
					"iops":                  nil,
					"throughput":            nil,
					"encrypted":             nil,
					"kms_key_id":            nil,
				}
				bdmMap["ebs"] = ebsMap
			}
			bdms[i] = bdmMap
		}
		attrs["block_device_mappings"] = bdms
	}

	tags := map[string]string{}
	for _, tag := range instance.Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	if len(tags) > 0 {
		attrs[domain.KeyTags] = tags
		if name, ok := tags["Name"]; ok {
			attrs[domain.KeyName] = name
		}
	}
	if _, ok := attrs[domain.KeyName]; !ok {
		attrs[domain.KeyName] = attrs[domain.KeyID]
	}

	return attrs
}
