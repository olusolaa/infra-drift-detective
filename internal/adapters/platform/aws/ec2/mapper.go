package ec2

import (
	"context"
	"encoding/base64"
	awstypes "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/types"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	aws_limiter "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/limiter"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

type ec2InstanceResource struct {
	mu              sync.RWMutex
	meta            domain.ResourceMetadata
	baseInstance    types.Instance
	awsConfig       aws.Config
	parentLogger    ports.Logger
	userData        *string
	userDataErr     error
	userDataFetched bool
	volumes         map[string]types.Volume
	volumesErr      error
	volumesFetched  bool
	attributesCache map[string]any
	attributesBuilt bool
}

func newEc2InstanceResource(
	instance types.Instance,
	cfg aws.Config,
	region string,
	accountID string,
	logger ports.Logger,
) (*ec2InstanceResource, error) {
	instanceID := aws.ToString(instance.InstanceId)
	if instanceID == "" {
		return nil, errors.New(errors.CodeInternal, "cannot create resource for instance with nil or empty InstanceId")
	}
	meta := domain.ResourceMetadata{
		Kind:               domain.KindComputeInstance,
		ProviderType:       awstypes.ProviderTypeAWS,
		ProviderAssignedID: instanceID,
		SourceIdentifier:   instanceID,
		Region:             region,
		AccountID:          accountID,
	}
	return &ec2InstanceResource{
		meta:         meta,
		baseInstance: instance,
		awsConfig:    cfg,
		parentLogger: logger.WithFields(map[string]any{"instance_id": instanceID}),
	}, nil
}

func (r *ec2InstanceResource) Metadata() domain.ResourceMetadata {
	return r.meta
}

func (r *ec2InstanceResource) Attributes() map[string]any {
	r.mu.RLock()
	if r.attributesBuilt {
		cacheCopy := make(map[string]any, len(r.attributesCache))
		for k, v := range r.attributesCache {
			cacheCopy[k] = v
		}
		r.mu.RUnlock()
		return cacheCopy
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.attributesBuilt {
		cacheCopy := make(map[string]any, len(r.attributesCache))
		for k, v := range r.attributesCache {
			cacheCopy[k] = v
		}
		return cacheCopy
	}

	r.parentLogger.Debugf(nil, "Building full attribute map (lazy loading if needed)")
	attrs := make(map[string]any)
	mapBaseInstanceAttributes(&r.baseInstance, r.meta.Region, r.meta.AccountID, attrs, r.parentLogger)

	// --- Trigger Lazy Loading ---
	// Using background context for now - Ideally context should be passed to Attributes()
	// If Attributes() doesn't take context, these background fetches might outlive the request.
	// For this exercise, let's use context.Background(), but acknowledge the limitation.
	ctx := context.Background()

	userDataValue, userDataErr := r.getUserData(ctx)
	if userDataErr == nil && userDataValue != nil {
		attrs[domain.ComputeUserDataKey] = *userDataValue
	} else if userDataErr != nil {
		r.parentLogger.Warnf(ctx, "Failed to lazy-load user data: %v", userDataErr)
	}

	volumeDetails, volumesErr := r.getVolumes(ctx)
	if volumesErr == nil && len(volumeDetails) > 0 {
		r.mapBlockDevicesUsingVolumes(attrs, volumeDetails)
	} else if volumesErr != nil {
		r.parentLogger.Warnf(ctx, "Failed to lazy-load volume details: %v. Falling back to mapping from instance data.", volumesErr)
		r.mapBlockDevicesFromInstanceOnly(attrs)
	} else {
		r.mapBlockDevicesFromInstanceOnly(attrs)
	}

	r.attributesCache = attrs
	r.attributesBuilt = true
	r.parentLogger.Debugf(ctx, "Attribute map built successfully")

	finalMap := make(map[string]any, len(attrs))
	for k, v := range attrs {
		finalMap[k] = v
	}
	return finalMap
}

func (r *ec2InstanceResource) getUserData(ctx context.Context) (*string, error) {
	r.mu.RLock()
	if r.userDataFetched {
		err := r.userDataErr
		userData := r.userData
		r.mu.RUnlock()
		return userData, err
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.userDataFetched {
		return r.userData, r.userDataErr
	}

	client := ec2.NewFromConfig(r.awsConfig)
	logger := r.parentLogger.WithFields(map[string]any{"lazy_fetch": "userdata"})
	logger.Debugf(ctx, "Lazy fetching UserData API call")

	input := &ec2.DescribeInstanceAttributeInput{
		Attribute:  types.InstanceAttributeNameUserData,
		InstanceId: &r.meta.ProviderAssignedID,
	}

	if err := aws_limiter.Wait(ctx, logger); err != nil {
		r.userDataErr = err
		r.userDataFetched = true
		return nil, err
	}

	result, err := client.DescribeInstanceAttribute(ctx, input)
	r.userDataFetched = true

	if err != nil {
		r.userDataErr = err
		logger.Warnf(ctx, "DescribeInstanceAttribute(UserData) API call failed: %v", err)
		return nil, err
	}
	if result.UserData != nil && result.UserData.Value != nil && *result.UserData.Value != "" {
		decodedBytes, decodeErr := base64.StdEncoding.DecodeString(*result.UserData.Value)
		if decodeErr != nil {
			r.userDataErr = errors.Wrap(decodeErr, errors.CodeInternal, "failed to decode UserData")
			logger.Warnf(ctx, "Failed to decode UserData: %v", decodeErr)
			return nil, r.userDataErr
		}
		decodedStr := string(decodedBytes)
		r.userData = &decodedStr
		logger.Debugf(ctx, "Successfully fetched and decoded UserData")
	} else {
		logger.Debugf(ctx, "No UserData attribute found")
	}

	r.userDataErr = nil
	return r.userData, nil
}

func (r *ec2InstanceResource) getVolumes(ctx context.Context) (map[string]types.Volume, error) {
	r.mu.RLock()
	if r.volumesFetched {
		err := r.volumesErr
		volumes := r.volumes
		r.mu.RUnlock()
		return volumes, err
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.volumesFetched {
		return r.volumes, r.volumesErr
	}

	client := ec2.NewFromConfig(r.awsConfig)
	logger := r.parentLogger.WithFields(map[string]any{"lazy_fetch": "volumes"})
	logger.Debugf(ctx, "Lazy fetching EBS Volume details")

	volumeIDs := extractEBSVolumeIDs(&r.baseInstance)
	if len(volumeIDs) == 0 {
		logger.Debugf(ctx, "No EBS volumes found in instance block device mappings, skipping DescribeVolumes")
		r.volumes = make(map[string]types.Volume)
		r.volumesFetched = true
		r.volumesErr = nil
		return r.volumes, nil
	}

	input := &ec2.DescribeVolumesInput{VolumeIds: volumeIDs}
	logger.Debugf(ctx, "Calling DescribeVolumes for %d volumes", len(volumeIDs))

	if err := aws_limiter.Wait(ctx, logger); err != nil {
		r.volumesErr = err
		r.volumesFetched = true
		return nil, err
	}

	output, err := client.DescribeVolumes(ctx, input)
	r.volumesFetched = true

	if err != nil {
		r.volumesErr = err
		logger.Warnf(ctx, "DescribeVolumes API call failed: %v", err)
		return nil, err
	}

	r.volumes = make(map[string]types.Volume)
	for _, vol := range output.Volumes {
		if vol.VolumeId != nil {
			r.volumes[*vol.VolumeId] = vol
		}
	}
	logger.Debugf(ctx, "Successfully described %d volumes", len(r.volumes))
	r.volumesErr = nil
	return r.volumes, nil
}

func mapBaseInstanceAttributes(instance *types.Instance, region string, accountID string, attrs map[string]any, logger ports.Logger) {
	attrs[domain.ComputeInstanceTypeKey] = string(instance.InstanceType)
	attrs[domain.ComputeImageIDKey] = aws.ToString(instance.ImageId)
	attrs[domain.ComputeSubnetIDKey] = aws.ToString(instance.SubnetId)
	if instance.Placement != nil {
		attrs[domain.ComputeAvailabilityZoneKey] = aws.ToString(instance.Placement.AvailabilityZone)
	}
	if instance.IamInstanceProfile != nil {
		attrs[domain.ComputeIAMInstanceProfileKey] = aws.ToString(instance.IamInstanceProfile.Arn)
	}

	securityGroupIDs := make([]string, 0, len(instance.SecurityGroups))
	for _, sg := range instance.SecurityGroups {
		securityGroupIDs = append(securityGroupIDs, aws.ToString(sg.GroupId))
	}
	attrs[domain.ComputeSecurityGroupsKey] = securityGroupIDs

	tags := make(map[string]string)
	nameTag := ""
	for _, tag := range instance.Tags {
		key := aws.ToString(tag.Key)
		tags[key] = aws.ToString(tag.Value)
		if key == "Name" {
			nameTag = aws.ToString(tag.Value)
		}
	}
	attrs[domain.KeyTags] = tags
	attrs[domain.KeyID] = aws.ToString(instance.InstanceId)
	attrs[domain.KeyName] = nameTag
}

func (r *ec2InstanceResource) mapBlockDevicesUsingVolumes(attrs map[string]any, volumes map[string]types.Volume) {
	rootDeviceName := aws.ToString(r.baseInstance.RootDeviceName)
	if rootDeviceName != "" {
		for _, bdm := range r.baseInstance.BlockDeviceMappings {
			if aws.ToString(bdm.DeviceName) == rootDeviceName {
				attrs[domain.ComputeRootBlockDeviceKey] = mapSingleBlockDevice(bdm, volumes, true, r.parentLogger)
				break
			}
		}
	}
	ebsDevicesAttrs := make([]map[string]any, 0)
	for _, bdm := range r.baseInstance.BlockDeviceMappings {
		if bdm.Ebs != nil && aws.ToString(bdm.DeviceName) != rootDeviceName {
			blockMap := mapSingleBlockDevice(bdm, volumes, false, r.parentLogger)
			if blockMap != nil {
				ebsDevicesAttrs = append(ebsDevicesAttrs, blockMap)
			}
		}
	}
	if len(ebsDevicesAttrs) > 0 {
		attrs[domain.ComputeEBSBlockDevicesKey] = ebsDevicesAttrs
	}
}

func (r *ec2InstanceResource) mapBlockDevicesFromInstanceOnly(attrs map[string]any) {
	rootDeviceName := aws.ToString(r.baseInstance.RootDeviceName)
	if rootDeviceName != "" {
		for _, bdm := range r.baseInstance.BlockDeviceMappings {
			if aws.ToString(bdm.DeviceName) == rootDeviceName {
				attrs[domain.ComputeRootBlockDeviceKey] = mapSingleBlockDevice(bdm, nil, true, r.parentLogger)
				break
			}
		}
	}
	ebsDevicesAttrs := make([]map[string]any, 0)
	for _, bdm := range r.baseInstance.BlockDeviceMappings {
		if bdm.Ebs != nil && aws.ToString(bdm.DeviceName) != rootDeviceName {
			blockMap := mapSingleBlockDevice(bdm, nil, false, r.parentLogger)
			if blockMap != nil {
				ebsDevicesAttrs = append(ebsDevicesAttrs, blockMap)
			}
		}
	}
	if len(ebsDevicesAttrs) > 0 {
		attrs[domain.ComputeEBSBlockDevicesKey] = ebsDevicesAttrs
	}
}

func mapSingleBlockDevice(bdm types.InstanceBlockDeviceMapping, volumes map[string]types.Volume, isRoot bool, logger ports.Logger) map[string]any {
	if bdm.Ebs == nil {
		return nil
	}
	volID := aws.ToString(bdm.Ebs.VolumeId)
	devName := aws.ToString(bdm.DeviceName)
	norm := make(map[string]any)
	norm["device_name"] = devName
	norm["delete_on_termination"] = aws.ToBool(bdm.Ebs.DeleteOnTermination)

	if volumes != nil {
		if vol, ok := volumes[volID]; ok {
			norm["volume_type"] = string(vol.VolumeType)
			norm["volume_size"] = aws.ToInt32(vol.Size)
			norm["iops"] = aws.ToInt32(vol.Iops)
			norm["throughput"] = aws.ToInt32(vol.Throughput)
			norm["encrypted"] = aws.ToBool(vol.Encrypted)
			norm["kms_key_id"] = aws.ToString(vol.KmsKeyId)
			norm["snapshot_id"] = aws.ToString(vol.SnapshotId)
		} else {
			logger.Warnf(nil, "Volume %s (Device: %s) not found in DescribeVolumes results, using less accurate mapping data", volID, devName)
			norm["encrypted"] = false
		}
	} else {
		logger.Debugf(nil, "No volume details available for device %s (Volume: %s), using basic mapping data", devName, volID)
		norm["encrypted"] = false
	}

	if _, exists := norm["delete_on_termination"]; !exists {
		norm["delete_on_termination"] = isRoot
	}

	return norm
}

func extractEBSVolumeIDs(instance *types.Instance) []string {
	if instance == nil {
		return nil
	}
	volumeIDs := make([]string, 0)
	idSet := make(map[string]struct{})
	for _, bdm := range instance.BlockDeviceMappings {
		if bdm.Ebs != nil && bdm.Ebs.VolumeId != nil {
			volID := aws.ToString(bdm.Ebs.VolumeId)
			if _, exists := idSet[volID]; !exists && volID != "" {
				volumeIDs = append(volumeIDs, volID)
				idSet[volID] = struct{}{}
			}
		}
	}
	return volumeIDs
}
