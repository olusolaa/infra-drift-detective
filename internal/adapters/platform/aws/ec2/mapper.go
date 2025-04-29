package ec2

import (
	"context"
	"encoding/base64"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	awstypes "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/types"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

var ec2ClientFactory func(aws.Config) interface{}
var ec2VolumeClientFactory func(aws.Config) interface{}

func SetEC2ClientFactory(factory func(aws.Config) interface{}) {
	ec2ClientFactory = factory
}

func SetEC2VolumeClientFactory(factory func(aws.Config) interface{}) {
	ec2VolumeClientFactory = factory
}

// Define interfaces for AWS clients to make mocking easier
type DescribeInstanceAttributeClient interface {
	DescribeInstanceAttribute(ctx context.Context, params *awsec2.DescribeInstanceAttributeInput, optFns ...func(*awsec2.Options)) (*awsec2.DescribeInstanceAttributeOutput, error)
}

type DescribeVolumesClient interface {
	DescribeVolumes(ctx context.Context, params *awsec2.DescribeVolumesInput, optFns ...func(*awsec2.Options)) (*awsec2.DescribeVolumesOutput, error)
}

// ec2InstanceResource struct remains the same
type ec2InstanceResource struct {
	meta domain.ResourceMetadata
	attr map[string]any
}

func (r *ec2InstanceResource) Metadata() domain.ResourceMetadata {
	return r.meta
}

func (r *ec2InstanceResource) Attributes() map[string]any {
	return r.attr
}

// MapInstanceToDomain maps a single EC2 instance to the domain resource format
func MapInstanceToDomain(instance types.Instance, region string, accountID string, ctx context.Context, cfg aws.Config) (domain.PlatformResource, error) {
	if instance.InstanceId == nil {
		return nil, errors.New(errors.CodeInternal, "received EC2 instance with nil InstanceId")
	}
	instanceID := *instance.InstanceId

	attrs := make(map[string]any)
	tags := make(map[string]string)
	nameTag := "" // Default empty name

	// --- Map Core Attributes ---
	attrs[domain.ComputeInstanceTypeKey] = string(instance.InstanceType) // InstanceType is required by API

	if instance.ImageId != nil {
		attrs[domain.ComputeImageIDKey] = *instance.ImageId
	}
	if instance.SubnetId != nil {
		attrs[domain.ComputeSubnetIDKey] = *instance.SubnetId
	}
	if instance.Placement != nil && instance.Placement.AvailabilityZone != nil {
		attrs[domain.ComputeAvailabilityZoneKey] = *instance.Placement.AvailabilityZone
	}
	if instance.IamInstanceProfile != nil && instance.IamInstanceProfile.Arn != nil {
		// Sometimes only the name is needed, sometimes the ARN. Storing ARN.
		// Comparer needs to know how TF state represents this (name vs ARN).
		attrs[domain.ComputeIAMInstanceProfileKey] = *instance.IamInstanceProfile.Arn
	}

	// --- Handle User Data (Decode Base64) ---
	// UserData isn't directly available in the Instance struct
	// We need to make a separate API call to get it
	if instance.InstanceId != nil {
		var client interface{}
		if ec2ClientFactory != nil {
			client = ec2ClientFactory(cfg)
		} else {
			client = awsec2.NewFromConfig(cfg)
		}

		if attrClient, ok := client.(DescribeInstanceAttributeClient); ok {
			userDataInput := &awsec2.DescribeInstanceAttributeInput{
				Attribute:  types.InstanceAttributeNameUserData,
				InstanceId: instance.InstanceId,
			}

			userDataOutput, err := attrClient.DescribeInstanceAttribute(ctx, userDataInput)
			if err == nil && userDataOutput.UserData != nil && userDataOutput.UserData.Value != nil {
				// Successfully retrieved user data
				encodedUserData := *userDataOutput.UserData.Value
				decodedUserData, decodeErr := base64.StdEncoding.DecodeString(encodedUserData)
				if decodeErr != nil {
					attrs[domain.ComputeUserDataKey] = encodedUserData
				} else {
					attrs[domain.ComputeUserDataKey] = string(decodedUserData)
				}
			}
		}
	}

	// --- Handle Security Groups ---
	securityGroupIDs := make([]string, 0, len(instance.SecurityGroups))
	for _, sg := range instance.SecurityGroups {
		if sg.GroupId != nil {
			securityGroupIDs = append(securityGroupIDs, *sg.GroupId)
		}
	}
	// Store sorted list for consistent comparison? Comparer should handle order differences.
	// sort.Strings(securityGroupIDs)
	attrs[domain.ComputeSecurityGroupsKey] = securityGroupIDs

	// --- Handle Block Devices ---
	if instance.RootDeviceName != nil && instance.RootDeviceType == types.DeviceTypeEbs {
		rootDeviceAttrs := findBlockDeviceAttrs(*instance.RootDeviceName, instance.BlockDeviceMappings, ctx, cfg)
		if rootDeviceAttrs != nil {
			attrs[domain.ComputeRootBlockDeviceKey] = rootDeviceAttrs
		}
	}

	ebsDevicesAttrs := make([]map[string]any, 0, len(instance.BlockDeviceMappings))
	for _, bdm := range instance.BlockDeviceMappings {
		// Check if it's an EBS volume and *not* the root device (if root is EBS)
		if bdm.Ebs != nil && bdm.DeviceName != nil &&
			(instance.RootDeviceName == nil || *bdm.DeviceName != *instance.RootDeviceName) {
			deviceAttrs := mapBlockDeviceAttrs(bdm, ctx, cfg)
			ebsDevicesAttrs = append(ebsDevicesAttrs, deviceAttrs)
		}
	}
	if len(ebsDevicesAttrs) > 0 {
		// Store sorted list for consistent comparison? Comparer should handle order differences.
		attrs[domain.ComputeEBSBlockDevicesKey] = ebsDevicesAttrs
	}

	// --- Handle Tags ---
	for _, tag := range instance.Tags {
		if tag.Key != nil && tag.Value != nil {
			tagKey := *tag.Key
			tagValue := *tag.Value
			tags[tagKey] = tagValue
			if tagKey == "Name" { // Extract standard Name tag
				nameTag = tagValue
			}
		}
	}
	attrs[domain.KeyTags] = tags // Store the full tag map

	// --- Set Domain Keys ---
	attrs[domain.KeyID] = instanceID
	attrs[domain.KeyName] = nameTag // Use value of 'Name' tag, or "" if not present

	// ARN is less common for EC2 but include if present and needed
	// instance ARN format: arn:aws:ec2:region:account-id:instance/instance-id
	// Can construct it if needed, or check if API provides it (it usually doesn't directly on Instance object)
	// For now, omit unless comparison specifically requires it.
	// attrs[domain.KeyARN] = constructInstanceARN(region, accountID, instanceID)

	// --- Construct Metadata ---
	meta := domain.ResourceMetadata{
		Kind:               domain.KindComputeInstance,
		ProviderType:       awstypes.ProviderTypeAWS,
		ProviderAssignedID: instanceID,
		SourceIdentifier:   instanceID, // Use ID as identifier for actual resources
		Region:             region,
		AccountID:          accountID,
	}

	return &ec2InstanceResource{
		meta: meta,
		attr: attrs,
	}, nil
}

// findBlockDeviceAttrs remains the same as before
func findBlockDeviceAttrs(deviceName string, mappings []types.InstanceBlockDeviceMapping, ctx context.Context, cfg aws.Config) map[string]any {
	for _, bdm := range mappings {
		if bdm.DeviceName != nil && *bdm.DeviceName == deviceName && bdm.Ebs != nil {
			return mapBlockDeviceAttrs(bdm, ctx, cfg)
		}
	}
	return nil
}

func mapBlockDeviceAttrs(bdm types.InstanceBlockDeviceMapping, ctx context.Context, cfg aws.Config) map[string]any {
	attrs := make(map[string]any)
	if bdm.DeviceName != nil {
		attrs["device_name"] = *bdm.DeviceName
	}

	// Check if the Ebs field is populated
	if bdm.Ebs != nil {
		ebsSpec := bdm.Ebs // ebsSpec is of type *types.EbsInstanceBlockDevice

		// Safely access fields within ebsSpec
		if ebsSpec.VolumeId != nil {
			attrs["volume_id"] = *ebsSpec.VolumeId

			// Make an additional API call to get complete volume information
			var client interface{}
			if ec2VolumeClientFactory != nil {
				client = ec2VolumeClientFactory(cfg)
			} else {
				client = awsec2.NewFromConfig(cfg)
			}

			// Try to use client as volume client
			if volumeClient, ok := client.(DescribeVolumesClient); ok {
				volumeInput := &awsec2.DescribeVolumesInput{
					VolumeIds: []string{*ebsSpec.VolumeId},
				}

				volumeOutput, err := volumeClient.DescribeVolumes(ctx, volumeInput)
				if err == nil && len(volumeOutput.Volumes) > 0 {
					// Successfully retrieved volume info
					volume := volumeOutput.Volumes[0]

					if volume.VolumeType != "" {
						attrs["volume_type"] = string(volume.VolumeType)
					}

					if volume.Size != nil {
						attrs["volume_size"] = *volume.Size
					}

					if volume.Iops != nil {
						attrs["iops"] = *volume.Iops
					}

					if volume.Throughput != nil {
						attrs["throughput"] = *volume.Throughput
					}

					if volume.Encrypted != nil {
						attrs["encrypted"] = *volume.Encrypted
					}

					if volume.KmsKeyId != nil {
						attrs["kms_key_id"] = *volume.KmsKeyId
					}

					if volume.SnapshotId != nil {
						attrs["snapshot_id"] = *volume.SnapshotId
					}
				}
			}
			// If API call failed, we'll still have the basic volume_id,
			// but will miss the detailed attributes
		}

		if ebsSpec.DeleteOnTermination != nil {
			attrs["delete_on_termination"] = *ebsSpec.DeleteOnTermination
		}
	}
	return attrs
}
