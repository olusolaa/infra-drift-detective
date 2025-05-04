package ec2

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	aws_limiter "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/limiter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	// Use new mock paths
	ec2mocks "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/ec2/mocks"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	ports "github.com/olusolaa/infra-drift-detector/internal/core/ports"
	portsmocks "github.com/olusolaa/infra-drift-detector/internal/core/ports/mocks"
)

// TestMain sets up the test environment before running tests
func TestMain(m *testing.M) {
	// Replace the limiter's Wait function with a no-op for testing
	originalWait := aws_limiter.WaitFunc
	aws_limiter.WaitFunc = func(ctx context.Context, logger ports.Logger) error {
		return nil // Don't actually rate limit during tests
	}

	// Run tests
	code := m.Run()

	// Restore the original function when done
	aws_limiter.WaitFunc = originalWait

	os.Exit(code)
}

// Helper function to create a test resource with mocks
func createTestInstanceResource(
	t *testing.T,
	instance Instance, // Use aliased Instance type
) (*ec2InstanceResource, *ec2mocks.EC2ClientInterface, *portsmocks.Logger) {
	t.Helper()
	mockEC2Client := new(ec2mocks.EC2ClientInterface)
	mockLogger := new(portsmocks.Logger)

	// Default logger expectations
	mockLogger.On("Debugf", mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Infof", mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Infof", mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Warnf", mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Warnf", mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Errorf", mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("WithFields", mock.Anything).Maybe().Return(mockLogger)

	// Call newEc2InstanceResource with the single EC2 client mock
	res, err := newEc2InstanceResource(
		instance,
		"us-west-2",    // Example region
		"123456789012", // Example account ID
		mockLogger,
		mockEC2Client,
	)
	require.NoError(t, err)
	require.NotNil(t, res)

	// Perform type assertion to get the concrete type *ec2InstanceResource
	ec2Res, ok := res.(*ec2InstanceResource)
	require.True(t, ok, "newEc2InstanceResource should return *ec2InstanceResource")

	return ec2Res, mockEC2Client, mockLogger
}

func TestEC2InstanceResource_Attributes_LazyLoading(t *testing.T) {
	instanceID := "i-lazyload"
	userDataBase64 := "IyEvYmluL3NoCmVjaG8gJ2hlbGxvJwo=" // #!/bin/sh\necho 'hello'
	userDataDecoded := "#!/bin/sh\necho 'hello'\n"       // Note the trailing newline
	rootDeviceName := "/dev/xvda"
	ebsDeviceName := "/dev/xvdf"
	rootVolID := "vol-root123"
	ebsVolID := "vol-ebs456"

	baseInstance := Instance{ // Use aliased Instance type
		InstanceId:     aws.String(instanceID),
		InstanceType:   ec2types.InstanceTypeT3Small,
		RootDeviceName: aws.String(rootDeviceName),
		BlockDeviceMappings: []ec2types.InstanceBlockDeviceMapping{
			{DeviceName: aws.String(rootDeviceName), Ebs: &ec2types.EbsInstanceBlockDevice{VolumeId: aws.String(rootVolID), DeleteOnTermination: aws.Bool(true)}},
			{DeviceName: aws.String(ebsDeviceName), Ebs: &ec2types.EbsInstanceBlockDevice{VolumeId: aws.String(ebsVolID), DeleteOnTermination: aws.Bool(false)}},
		},
		Tags: []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("LazyLoader")}},
	}

	describeUserDataOutput := &ec2.DescribeInstanceAttributeOutput{
		UserData: &ec2types.AttributeValue{Value: aws.String(userDataBase64)},
	}
	describeVolumesOutput := &ec2.DescribeVolumesOutput{
		Volumes: []ec2types.Volume{
			{VolumeId: aws.String(rootVolID), VolumeType: ec2types.VolumeTypeGp3, Size: aws.Int32(20), Encrypted: aws.Bool(false)},
			{VolumeId: aws.String(ebsVolID), VolumeType: ec2types.VolumeTypeIo2, Size: aws.Int32(50), Iops: aws.Int32(5000), Encrypted: aws.Bool(true)},
		},
	}
	userDataAPIErr := errors.New("failed to access EC2 UserData")
	volumesAPIErr := errors.New("failed to access EC2 EBS Volumes")
	tests := []struct {
		name                 string
		setupMocks           func(mockEC2 *ec2mocks.EC2ClientInterface) // Updated mock type
		expectedAttributes   map[string]any
		expectedErrSubstring string                                                   // Check for substring instead of exact error
		verifyMocks          func(t *testing.T, mockEC2 *ec2mocks.EC2ClientInterface) // Updated mock type
	}{
		{
			name: "success first call",
			setupMocks: func(mockEC2 *ec2mocks.EC2ClientInterface) {
				mockEC2.On("DescribeInstanceAttribute", mock.Anything, mock.AnythingOfType("*ec2.DescribeInstanceAttributeInput"), mock.Anything).Return(describeUserDataOutput, nil).Once()
				mockEC2.On("DescribeVolumes", mock.Anything, mock.MatchedBy(func(i *ec2.DescribeVolumesInput) bool {
					return assert.ElementsMatch(t, []string{rootVolID, ebsVolID}, i.VolumeIds)
				}), mock.Anything).Return(describeVolumesOutput, nil).Once()
			},
			expectedAttributes: map[string]any{
				domain.KeyID:       instanceID,
				domain.KeyName:     "LazyLoader",
				"instance_type":    string(ec2types.InstanceTypeT3Small),
				domain.KeyTags:     map[string]string{"Name": "LazyLoader"},
				"user_data":        userDataDecoded,
				"root_device_name": rootDeviceName,
				"block_device_mappings": []map[string]any{
					{"device_name": rootDeviceName, "ebs": map[string]any{"volume_id": rootVolID, "status": "", "attach_time": "0001-01-01T00:00:00Z", "delete_on_termination": true, "size": int32(20), "encrypted": false, "kms_key_id": (*string)(nil)}},
					{"device_name": ebsDeviceName, "ebs": map[string]any{"volume_id": ebsVolID, "status": "", "attach_time": "0001-01-01T00:00:00Z", "delete_on_termination": false, "size": int32(50), "iops": int32(5000), "throughput": (*int32)(nil), "encrypted": true, "kms_key_id": (*string)(nil)}},
				},
			},
			verifyMocks: func(t *testing.T, mockEC2 *ec2mocks.EC2ClientInterface) {
				mockEC2.AssertExpectations(t)
			},
		},
		{
			name: "user data fetch fails",
			setupMocks: func(mockEC2 *ec2mocks.EC2ClientInterface) {
				mockEC2.On("DescribeInstanceAttribute", mock.Anything, mock.Anything, mock.Anything).Return(nil, userDataAPIErr).Once()
				mockEC2.On("DescribeVolumes", mock.Anything, mock.Anything, mock.Anything).Return(describeVolumesOutput, nil).Once()
			},
			expectedAttributes: map[string]any{ // Base attributes are still mapped
				domain.KeyID:       instanceID,
				domain.KeyName:     "LazyLoader",
				"instance_type":    string(ec2types.InstanceTypeT3Small),
				domain.KeyTags:     map[string]string{"Name": "LazyLoader"},
				"root_device_name": rootDeviceName,
				"block_device_mappings": []map[string]any{
					{"device_name": rootDeviceName, "ebs": map[string]any{"volume_id": rootVolID, "status": "", "attach_time": "0001-01-01T00:00:00Z", "delete_on_termination": true, "size": int32(20), "encrypted": false, "kms_key_id": (*string)(nil)}},
					{"device_name": ebsDeviceName, "ebs": map[string]any{"volume_id": ebsVolID, "status": "", "attach_time": "0001-01-01T00:00:00Z", "delete_on_termination": false, "size": int32(50), "iops": int32(5000), "throughput": (*int32)(nil), "encrypted": true, "kms_key_id": (*string)(nil)}},
				},
			},
			expectedErrSubstring: "failed to access EC2 UserData", // Check substring
			verifyMocks: func(t *testing.T, mockEC2 *ec2mocks.EC2ClientInterface) {
				mockEC2.AssertExpectations(t)
			},
		},
		{
			name: "volumes fetch fails",
			setupMocks: func(mockEC2 *ec2mocks.EC2ClientInterface) {
				mockEC2.On("DescribeInstanceAttribute", mock.Anything, mock.Anything, mock.Anything).Return(describeUserDataOutput, nil).Once()
				mockEC2.On("DescribeVolumes", mock.Anything, mock.Anything, mock.Anything).Return(nil, volumesAPIErr).Once()
			},
			expectedAttributes: map[string]any{ // Base + UserData are mapped
				domain.KeyID:       instanceID,
				domain.KeyName:     "LazyLoader",
				"instance_type":    string(ec2types.InstanceTypeT3Small),
				domain.KeyTags:     map[string]string{"Name": "LazyLoader"},
				"user_data":        userDataDecoded,
				"root_device_name": rootDeviceName,
				"block_device_mappings": []map[string]any{ // EBS details remain nil
					{"device_name": rootDeviceName, "ebs": map[string]any{"volume_id": rootVolID, "status": "", "attach_time": "0001-01-01T00:00:00Z", "delete_on_termination": true, "size": (*int32)(nil), "encrypted": (*bool)(nil), "kms_key_id": (*string)(nil)}},
					{"device_name": ebsDeviceName, "ebs": map[string]any{"volume_id": ebsVolID, "status": "", "attach_time": "0001-01-01T00:00:00Z", "delete_on_termination": false, "size": (*int32)(nil), "encrypted": (*bool)(nil), "kms_key_id": (*string)(nil)}},
				},
			},
			expectedErrSubstring: "failed to access EC2 EBS Volumes", // Match actual error format
			verifyMocks: func(t *testing.T, mockEC2 *ec2mocks.EC2ClientInterface) {
				mockEC2.AssertExpectations(t)
			},
		},
		{
			name: "both fetches fail",
			setupMocks: func(mockEC2 *ec2mocks.EC2ClientInterface) {
				mockEC2.On("DescribeInstanceAttribute", mock.Anything, mock.Anything, mock.Anything).Return(nil, userDataAPIErr).Once()
				mockEC2.On("DescribeVolumes", mock.Anything, mock.Anything, mock.Anything).Return(nil, volumesAPIErr).Once()
			},
			expectedAttributes: map[string]any{ // Only base attributes
				domain.KeyID:       instanceID,
				domain.KeyName:     "LazyLoader",
				"instance_type":    string(ec2types.InstanceTypeT3Small),
				domain.KeyTags:     map[string]string{"Name": "LazyLoader"},
				"root_device_name": rootDeviceName,
				"block_device_mappings": []map[string]any{
					{"device_name": rootDeviceName, "ebs": map[string]any{"volume_id": rootVolID, "status": "", "attach_time": "0001-01-01T00:00:00Z", "delete_on_termination": true, "size": (*int32)(nil), "encrypted": (*bool)(nil), "kms_key_id": (*string)(nil)}},
					{"device_name": ebsDeviceName, "ebs": map[string]any{"volume_id": ebsVolID, "status": "", "attach_time": "0001-01-01T00:00:00Z", "delete_on_termination": false, "size": (*int32)(nil), "encrypted": (*bool)(nil), "kms_key_id": (*string)(nil)}},
				},
			},
			expectedErrSubstring: "failed to access EC2 EBS Volumes", // Match actual error format
			verifyMocks: func(t *testing.T, mockEC2 *ec2mocks.EC2ClientInterface) {
				mockEC2.AssertExpectations(t)
			},
		},
		{
			name: "second call uses cache (success)",
			setupMocks: func(mockEC2 *ec2mocks.EC2ClientInterface) {
				mockEC2.On("DescribeInstanceAttribute", mock.Anything, mock.Anything, mock.Anything).Return(describeUserDataOutput, nil).Once()
				mockEC2.On("DescribeVolumes", mock.Anything, mock.Anything, mock.Anything).Return(describeVolumesOutput, nil).Once()
			},
			expectedAttributes: map[string]any{
				domain.KeyID:       instanceID,
				domain.KeyName:     "LazyLoader",
				"instance_type":    string(ec2types.InstanceTypeT3Small),
				domain.KeyTags:     map[string]string{"Name": "LazyLoader"},
				"user_data":        userDataDecoded,
				"root_device_name": rootDeviceName,
				"block_device_mappings": []map[string]any{
					{"device_name": rootDeviceName, "ebs": map[string]any{"volume_id": rootVolID, "status": "", "attach_time": "0001-01-01T00:00:00Z", "delete_on_termination": true, "size": int32(20), "encrypted": false, "kms_key_id": (*string)(nil)}},
					{"device_name": ebsDeviceName, "ebs": map[string]any{"volume_id": ebsVolID, "status": "", "attach_time": "0001-01-01T00:00:00Z", "delete_on_termination": false, "size": int32(50), "iops": int32(5000), "throughput": (*int32)(nil), "encrypted": true, "kms_key_id": (*string)(nil)}},
				},
			},
			verifyMocks: func(t *testing.T, mockEC2 *ec2mocks.EC2ClientInterface) {
				mockEC2.AssertNumberOfCalls(t, "DescribeInstanceAttribute", 1)
				mockEC2.AssertNumberOfCalls(t, "DescribeVolumes", 1)
			},
		},
		{
			name: "second call uses cache (error)",
			setupMocks: func(mockEC2 *ec2mocks.EC2ClientInterface) {
				mockEC2.On("DescribeInstanceAttribute", mock.Anything, mock.Anything, mock.Anything).Return(nil, userDataAPIErr).Once()
				mockEC2.On("DescribeVolumes", mock.Anything, mock.Anything, mock.Anything).Return(describeVolumesOutput, nil).Once()
			},
			expectedAttributes: map[string]any{
				domain.KeyID:       instanceID,
				domain.KeyName:     "LazyLoader",
				"instance_type":    string(ec2types.InstanceTypeT3Small),
				domain.KeyTags:     map[string]string{"Name": "LazyLoader"},
				"root_device_name": rootDeviceName,
				"block_device_mappings": []map[string]any{
					{"device_name": rootDeviceName, "ebs": map[string]any{"volume_id": rootVolID, "status": "", "attach_time": "0001-01-01T00:00:00Z", "delete_on_termination": true, "size": int32(20), "encrypted": false, "kms_key_id": (*string)(nil)}},
					{"device_name": ebsDeviceName, "ebs": map[string]any{"volume_id": ebsVolID, "status": "", "attach_time": "0001-01-01T00:00:00Z", "delete_on_termination": false, "size": int32(50), "iops": int32(5000), "throughput": (*int32)(nil), "encrypted": true, "kms_key_id": (*string)(nil)}},
				},
			},
			expectedErrSubstring: "failed to access EC2 UserData",
			verifyMocks: func(t *testing.T, mockEC2 *ec2mocks.EC2ClientInterface) {
				mockEC2.AssertNumberOfCalls(t, "DescribeInstanceAttribute", 1)
				mockEC2.AssertNumberOfCalls(t, "DescribeVolumes", 1)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use updated helper
			res, mockEC2, mockLogger := createTestInstanceResource(t, baseInstance)
			tt.setupMocks(mockEC2)
			ctx := context.Background()

			// Simulate limiter wait - needed by Attributes -> fetchAndMap
			// Use shared limiter mock if needed, or assume limiter isn't called directly in tested code
			// mockLimiter := new(sharedmocks.RateLimiter)
			// mockLimiter.On("Wait", mock.Anything, mockLogger).Return(nil).Maybe()

			attrs1, err1 := res.Attributes(ctx)
			attrs2, err2 := res.Attributes(ctx)

			if tt.expectedErrSubstring != "" {
				require.Error(t, err1)
				assert.Contains(t, err1.Error(), tt.expectedErrSubstring)
			} else {
				assert.NoError(t, err1)
			}

			// Normalize the attributes map before comparison if necessary
			// This involves handling potential nil pointers in the expected map
			normalizedExpected := normalizeMap(tt.expectedAttributes)
			normalizedActual := normalizeMap(attrs1)

			assert.Equal(t, normalizedExpected, normalizedActual)

			assert.Equal(t, err1, err2)
			assert.Equal(t, normalizedActual, normalizeMap(attrs2))

			if tt.verifyMocks != nil {
				tt.verifyMocks(t, mockEC2)
			}
			mockLogger.AssertExpectations(t)
		})
	}
}

// normalizeMap recursively removes nil pointer values from maps and slices within maps.
// This helps in comparing attribute maps where the actual map might have nil pointers
// for fields that were not set or fetched, while the expected map might use (*type)(nil).
func normalizeMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	normalized := make(map[string]any)
	for k, v := range m {
		switch val := v.(type) {
		case map[string]any:
			normalized[k] = normalizeMap(val)
		case []map[string]any:
			normalizedSlice := make([]map[string]any, len(val))
			for i, item := range val {
				normalizedSlice[i] = normalizeMap(item)
			}
			normalized[k] = normalizedSlice
		case *int32:
			if val != nil {
				normalized[k] = *val
			} // else omit nil pointer
		case *bool:
			if val != nil {
				normalized[k] = *val
			} // else omit nil pointer
		case *string:
			if val != nil {
				normalized[k] = *val
			} // else omit nil pointer
		default:
			if v != nil { // Keep non-nil values
				normalized[k] = v
			}
		}
	}
	return normalized
}

func TestEC2InstanceResource_Attributes_ContextCancellation(t *testing.T) {
	instanceID := "i-cancel"
	rootDeviceName := "/dev/xvda"
	rootVolID := "vol-root123"
	baseInstance := Instance{
		InstanceId:     aws.String(instanceID),
		InstanceType:   ec2types.InstanceTypeT3Small,
		RootDeviceName: aws.String(rootDeviceName),
		BlockDeviceMappings: []ec2types.InstanceBlockDeviceMapping{
			{DeviceName: aws.String(rootDeviceName), Ebs: &ec2types.EbsInstanceBlockDevice{VolumeId: aws.String(rootVolID)}},
		},
	}

	res, mockEC2, _ := createTestInstanceResource(t, baseInstance)

	ctx, cancel := context.WithCancel(context.Background())

	// Mock DescribeInstanceAttribute to be slow and then cancel context
	mockEC2.On("DescribeInstanceAttribute", mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			time.Sleep(50 * time.Millisecond) // Simulate work
			cancel()
		}).Return(nil, context.Canceled).Maybe() // Use Maybe as it might be called or context might cancel first

	// Mock DescribeVolumes to also be potentially slow
	mockEC2.On("DescribeVolumes", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, context.Canceled).Maybe() // Return canceled or let context handle it

	// Call Attributes with the cancelable context
	_, err := res.Attributes(ctx)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestEC2InstanceResource_Metadata(t *testing.T) {
	instanceID := "i-metadata"
	region := "eu-central-1"
	accountID := "987654321098"
	instanceType := ec2types.InstanceTypeM5Large
	nameTag := "MetaTestInstance"

	instance := Instance{
		InstanceId:   aws.String(instanceID),
		InstanceType: instanceType,
		Tags:         []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(nameTag)}},
	}

	// Use helper, but we don't need the mocks for this test
	res, _, _ := createTestInstanceResource(t, instance)
	// Override region and account ID set by helper if needed for test clarity
	res.meta.Region = region
	res.meta.AccountID = accountID

	meta := res.Metadata()

	assert.Equal(t, domain.KindComputeInstance, meta.Kind)
	assert.Equal(t, "aws", meta.ProviderType)
	assert.Equal(t, instanceID, meta.ProviderAssignedID)
	assert.Equal(t, instanceID, meta.SourceIdentifier)
	assert.Equal(t, accountID, meta.AccountID)
	assert.Equal(t, region, meta.Region)
	// Name is not part of metadata, it's an attribute derived from tags
	// assert.Equal(t, nameTag, meta.Name)
}

// --- Mapping Helper Tests ---

func TestMapBaseInstanceAttributes(t *testing.T) {
	launchTime := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)
	instanceID := "i-mapbase"

	instance := Instance{
		InstanceId:          aws.String(instanceID),
		ImageId:             aws.String("ami-123"),
		InstanceType:        ec2types.InstanceTypeT2Micro,
		KeyName:             aws.String("my-key"),
		LaunchTime:          aws.Time(launchTime),
		PrivateDnsName:      aws.String("ip-10-0-1-10.internal"),
		PrivateIpAddress:    aws.String("10.0.1.10"),
		PublicDnsName:       aws.String("ec2-54-0-0-1.compute-1.amazonaws.com"),
		PublicIpAddress:     aws.String("54.0.0.1"),
		SubnetId:            aws.String("subnet-abc"),
		VpcId:               aws.String("vpc-xyz"),
		Architecture:        ec2types.ArchitectureValuesX8664,
		RootDeviceName:      aws.String("/dev/sda1"),
		RootDeviceType:      ec2types.DeviceTypeEbs,
		State:               &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
		EnaSupport:          aws.Bool(true),
		Hypervisor:          ec2types.HypervisorTypeXen,
		IamInstanceProfile:  &ec2types.IamInstanceProfile{Arn: aws.String("arn:aws:iam::111:instance-profile/role")},
		VirtualizationType:  ec2types.VirtualizationTypeHvm,
		CpuOptions:          &ec2types.CpuOptions{CoreCount: aws.Int32(2), ThreadsPerCore: aws.Int32(1)},
		HibernationOptions:  &ec2types.HibernationOptions{Configured: aws.Bool(false)},
		SecurityGroups:      []ec2types.GroupIdentifier{{GroupId: aws.String("sg-1"), GroupName: aws.String("web-sg")}},
		BlockDeviceMappings: []ec2types.InstanceBlockDeviceMapping{ /* Tested separately */ },
		Tags:                []ec2types.Tag{{Key: aws.String("Env"), Value: aws.String("dev")}},
	}

	mockLogger := new(portsmocks.Logger)
	mockLogger.On("WithFields", mock.Anything).Return(mockLogger)

	attrs := mapInstanceToAttributes(instance, mockLogger)

	assert.Equal(t, instanceID, attrs[domain.KeyID])
	assert.Equal(t, "ami-123", attrs["image_id"])
	assert.Equal(t, string(ec2types.InstanceTypeT2Micro), attrs["instance_type"])
	assert.Equal(t, "my-key", attrs["key_name"])
	assert.Equal(t, launchTime.UTC().Format(time.RFC3339), attrs["launch_time"])
	assert.Equal(t, "ip-10-0-1-10.internal", attrs["private_dns_name"])
	assert.Equal(t, "10.0.1.10", attrs["private_ip_address"])
	assert.Equal(t, "ec2-54-0-0-1.compute-1.amazonaws.com", attrs["public_dns_name"])
	assert.Equal(t, "54.0.0.1", attrs["public_ip_address"])
	assert.Equal(t, "subnet-abc", attrs["subnet_id"])
	assert.Equal(t, "vpc-xyz", attrs["vpc_id"])
	assert.Equal(t, string(ec2types.ArchitectureValuesX8664), attrs["architecture"])
	assert.Equal(t, "/dev/sda1", attrs["root_device_name"])
	assert.Equal(t, string(ec2types.DeviceTypeEbs), attrs["root_device_type"])
	assert.Equal(t, string(ec2types.InstanceStateNameRunning), attrs["state"])
	assert.True(t, attrs["ena_support"].(bool))
	assert.Equal(t, string(ec2types.HypervisorTypeXen), attrs["hypervisor"])
	assert.Equal(t, "arn:aws:iam::111:instance-profile/role", attrs["iam_instance_profile_arn"])
	assert.Equal(t, string(ec2types.VirtualizationTypeHvm), attrs["virtualization_type"])
	assert.Equal(t, map[string]any{"core_count": int32(2), "threads_per_core": int32(1)}, attrs["cpu_options"])
	assert.False(t, attrs["hibernation_enabled"].(bool))
	assert.Equal(t, []map[string]string{{"id": "sg-1", "name": "web-sg"}}, attrs["security_groups"])
	assert.Equal(t, map[string]string{"Env": "dev"}, attrs[domain.KeyTags])
	assert.Equal(t, instanceID, attrs[domain.KeyName]) // Defaults to ID as no Name tag
}

func TestMapBaseInstanceAttributes_WithNameTag(t *testing.T) {
	instanceID := "i-nametag"
	name := "MyWebServer"
	instance := Instance{
		InstanceId: aws.String(instanceID),
		Tags: []ec2types.Tag{
			{Key: aws.String("Name"), Value: aws.String(name)},
			{Key: aws.String("Other"), Value: aws.String("Value")},
		},
	}
	mockLogger := new(portsmocks.Logger)
	mockLogger.On("WithFields", mock.Anything).Return(mockLogger)
	attrs := mapInstanceToAttributes(instance, mockLogger)
	assert.Equal(t, instanceID, attrs[domain.KeyID])
	assert.Equal(t, name, attrs[domain.KeyName]) // Name tag should override default
	assert.Equal(t, map[string]string{"Name": name, "Other": "Value"}, attrs[domain.KeyTags])
}

// ... (rest of the mapping tests) ...
