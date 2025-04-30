package ec2_test

import (
	"context"
	"errors"
	awstypes "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/util"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	ec2adapter "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/ec2"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/mocks"
)

func TestMapInstanceToDomain(t *testing.T) {
	instanceID := "i-1234567890abcdef0"
	region := "us-west-2"
	accountID := "123456789012"
	imageID := "ami-12345"
	subnetID := "subnet-12345"
	sgID1 := "sg-12345"
	sgID2 := "sg-67890"
	az := "us-west-2a"
	deviceName := "/dev/sda1"
	volumeID := "vol-12345"
	iamProfileArn := "arn:aws:iam::123456789012:instance-profile/test-profile"
	userData := "IyEvYmluL2Jhc2gKZWNobyAiaGVsbG8gd29ybGQi" // Base64 encoded "#!/bin/bash\necho \"hello world\""

	tests := []struct {
		name               string
		instance           types.Instance
		setupMocks         func(mockEC2AttrClient *mocks.MockEC2InstanceAttributeClient, mockEC2VolumeClient *mocks.MockEC2VolumeClient)
		expectedID         string
		expectedAttributes map[string]interface{}
		expectError        bool
	}{
		{
			name: "complete instance mapping",
			instance: types.Instance{
				InstanceId:   aws.String(instanceID),
				InstanceType: types.InstanceTypeT2Micro,
				ImageId:      aws.String(imageID),
				SubnetId:     aws.String(subnetID),
				SecurityGroups: []types.GroupIdentifier{
					{GroupId: aws.String(sgID1)},
					{GroupId: aws.String(sgID2)},
				},
				Placement: &types.Placement{
					AvailabilityZone: aws.String(az),
				},
				Tags: []types.Tag{
					{Key: aws.String("Name"), Value: aws.String("test-instance")},
					{Key: aws.String("Environment"), Value: aws.String("test")},
				},
				IamInstanceProfile: &types.IamInstanceProfile{
					Arn: aws.String(iamProfileArn),
				},
				RootDeviceName: aws.String(deviceName),
				RootDeviceType: types.DeviceTypeEbs,
				BlockDeviceMappings: []types.InstanceBlockDeviceMapping{
					{
						DeviceName: aws.String(deviceName),
						Ebs: &types.EbsInstanceBlockDevice{
							VolumeId: aws.String(volumeID),
						},
					},
				},
			},
			setupMocks: func(mockEC2AttrClient *mocks.MockEC2InstanceAttributeClient, mockEC2VolumeClient *mocks.MockEC2VolumeClient) {
				// Setup user data mock
				userDataOutput := &ec2.DescribeInstanceAttributeOutput{
					UserData: &types.AttributeValue{
						Value: aws.String(userData),
					},
				}
				mockEC2AttrClient.On("DescribeInstanceAttribute", mock.Anything, mock.Anything, mock.Anything).Return(userDataOutput, nil)

				// Setup volume mock
				volumeOutput := &ec2.DescribeVolumesOutput{
					Volumes: []types.Volume{
						{
							VolumeId:   aws.String(volumeID),
							VolumeType: types.VolumeTypeGp2,
							Size:       aws.Int32(8),
							Iops:       aws.Int32(100),
							Throughput: aws.Int32(125),
							Encrypted:  aws.Bool(true),
						},
					},
				}
				mockEC2VolumeClient.On("DescribeVolumes", mock.Anything, mock.Anything, mock.Anything).Return(volumeOutput, nil)
			},
			expectedID: instanceID,
			expectedAttributes: map[string]interface{}{
				domain.KeyID:                        instanceID,
				domain.KeyName:                      "test-instance",
				domain.ComputeInstanceTypeKey:       "t2.micro",
				domain.ComputeImageIDKey:            imageID,
				domain.ComputeSubnetIDKey:           subnetID,
				domain.ComputeAvailabilityZoneKey:   az,
				domain.ComputeIAMInstanceProfileKey: iamProfileArn,
				domain.ComputeUserDataKey:           "#!/bin/bash\necho \"hello world\"",
				domain.ComputeSecurityGroupsKey:     []string{sgID1, sgID2},
				domain.KeyTags: map[string]string{
					"Name":        "test-instance",
					"Environment": "test",
				},
				domain.ComputeRootBlockDeviceKey: map[string]interface{}{
					"device_name": deviceName,
					"volume_id":   volumeID,
					"volume_type": "gp2",
					"volume_size": int32(8),
					"iops":        int32(100),
					"throughput":  int32(125),
					"encrypted":   true,
				},
			},
			expectError: false,
		},
		{
			name: "instance without ID",
			instance: types.Instance{
				InstanceType: types.InstanceTypeT2Micro,
			},
			setupMocks: func(mockEC2AttrClient *mocks.MockEC2InstanceAttributeClient, mockEC2VolumeClient *mocks.MockEC2VolumeClient) {
				// No mocks needed as function should return early with error
			},
			expectedID:         "",
			expectedAttributes: nil,
			expectError:        true,
		},
		{
			name: "instance with minimal attributes",
			instance: types.Instance{
				InstanceId:   aws.String(instanceID),
				InstanceType: types.InstanceTypeT2Micro,
			},
			setupMocks: func(mockEC2AttrClient *mocks.MockEC2InstanceAttributeClient, mockEC2VolumeClient *mocks.MockEC2VolumeClient) {
				// Setup user data mock to return error
				mockEC2AttrClient.On("DescribeInstanceAttribute", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("AWS error"))
			},
			expectedID: instanceID,
			expectedAttributes: map[string]interface{}{
				domain.KeyID:                  instanceID,
				domain.KeyName:                "",
				domain.ComputeInstanceTypeKey: "t2.micro",
				domain.KeyTags:                map[string]string{},
			},
			expectError: false,
		},
		{
			name: "instance with block devices but no volume info",
			instance: types.Instance{
				InstanceId:     aws.String(instanceID),
				InstanceType:   types.InstanceTypeT2Micro,
				RootDeviceName: aws.String(deviceName),
				RootDeviceType: types.DeviceTypeEbs,
				BlockDeviceMappings: []types.InstanceBlockDeviceMapping{
					{
						DeviceName: aws.String(deviceName),
						Ebs: &types.EbsInstanceBlockDevice{
							VolumeId: aws.String(volumeID),
						},
					},
					{
						DeviceName: aws.String("/dev/sdb"),
						Ebs: &types.EbsInstanceBlockDevice{
							VolumeId: aws.String("vol-67890"),
						},
					},
				},
			},
			setupMocks: func(mockEC2AttrClient *mocks.MockEC2InstanceAttributeClient, mockEC2VolumeClient *mocks.MockEC2VolumeClient) {
				// Setup user data mock to return error
				mockEC2AttrClient.On("DescribeInstanceAttribute", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("AWS error"))

				// Setup volume mock to return error
				mockEC2VolumeClient.On("DescribeVolumes", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("AWS error"))
			},
			expectedID: instanceID,
			expectedAttributes: map[string]interface{}{
				domain.KeyID:                  instanceID,
				domain.KeyName:                "",
				domain.ComputeInstanceTypeKey: "t2.micro",
				domain.KeyTags:                map[string]string{},
				domain.ComputeRootBlockDeviceKey: map[string]interface{}{
					"device_name": deviceName,
					"volume_id":   volumeID,
				},
				domain.ComputeEBSBlockDevicesKey: []map[string]interface{}{
					{
						"device_name": "/dev/sdb",
						"volume_id":   "vol-67890",
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mocks
			mockEC2AttrClient := new(mocks.MockEC2InstanceAttributeClient)
			mockEC2VolumeClient := new(mocks.MockEC2VolumeClient)

			// Create AWS config with client factories
			ctx := context.Background()
			awsCfg := aws.Config{
				Region: region,
			}

			// Setup factory in the EC2 adapter package to return our mocks
			ec2adapter.SetEC2ClientFactory(func(cfg aws.Config) interface{} {
				return mockEC2AttrClient
			})
			ec2adapter.SetEC2VolumeClientFactory(func(cfg aws.Config) interface{} {
				return mockEC2VolumeClient
			})

			// Setup mocks
			tt.setupMocks(mockEC2AttrClient, mockEC2VolumeClient)

			// Call function
			resource, err := ec2adapter.MapInstanceToDomain(tt.instance, region, accountID, ctx, awsCfg)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, resource)
				return
			}

			assert.NoError(t, err)
			assert.NotNil(t, resource)

			// Verify metadata
			meta := resource.Metadata()
			assert.Equal(t, domain.KindComputeInstance, meta.Kind)
			assert.Equal(t, awstypes.ProviderTypeAWS, meta.ProviderType)
			assert.Equal(t, tt.expectedID, meta.ProviderAssignedID)
			assert.Equal(t, region, meta.Region)
			assert.Equal(t, accountID, meta.AccountID)

			// Verify attributes
			attrs := resource.Attributes()

			// Check mandatory fields
			assert.Equal(t, tt.expectedAttributes[domain.KeyID], attrs[domain.KeyID])
			assert.Equal(t, tt.expectedAttributes[domain.KeyName], attrs[domain.KeyName])
			assert.Equal(t, tt.expectedAttributes[domain.ComputeInstanceTypeKey], attrs[domain.ComputeInstanceTypeKey])

			// Check optional fields if expected
			for k, v := range tt.expectedAttributes {
				if k == domain.KeyTags {
					// Special handling for tags map
					expectedTags := v.(map[string]string)
					actualTags, exists := attrs[domain.KeyTags].(map[string]string)
					assert.True(t, exists)
					for tagKey, tagValue := range expectedTags {
						assert.Equal(t, tagValue, actualTags[tagKey])
					}
				} else if k == domain.ComputeRootBlockDeviceKey {
					// Special handling for nested map
					if v != nil {
						expectedDevice := v.(map[string]interface{})
						actualDevice, exists := attrs[domain.ComputeRootBlockDeviceKey].(map[string]interface{})
						assert.True(t, exists)
						for deviceKey, deviceValue := range expectedDevice {
							assert.Equal(t, deviceValue, actualDevice[deviceKey])
						}
					}
				} else if k == domain.ComputeEBSBlockDevicesKey {
					// Special handling for slice of maps
					if v != nil {
						expectedDevices := v.([]map[string]interface{})
						actualDevices, exists := attrs[domain.ComputeEBSBlockDevicesKey].([]map[string]interface{})
						assert.True(t, exists)
						assert.Equal(t, len(expectedDevices), len(actualDevices))
						if len(expectedDevices) > 0 {
							assert.Equal(t, expectedDevices[0]["device_name"], actualDevices[0]["device_name"])
							assert.Equal(t, expectedDevices[0]["volume_id"], actualDevices[0]["volume_id"])
						}
					}
				} else if k == domain.ComputeSecurityGroupsKey {
					// Special handling for slices
					expectedGroups := v.([]string)
					actualGroups, exists := attrs[domain.ComputeSecurityGroupsKey].([]string)
					assert.True(t, exists)
					assert.ElementsMatch(t, expectedGroups, actualGroups)
				} else {
					// Standard field comparison
					if v != nil {
						assert.Equal(t, v, attrs[k])
					}
				}
			}
		})
	}

	// Reset the client factory after tests
	ec2adapter.SetEC2ClientFactory(nil)
	ec2adapter.SetEC2VolumeClientFactory(nil)
}
