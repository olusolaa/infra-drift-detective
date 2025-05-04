package ec2

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

//go:generate mockery --name EC2ClientInterface --output ./mocks --outpkg mocks --case underscore
//go:generate mockery --name EC2InstancesPaginator --output ./mocks --outpkg mocks --case underscore

type EC2ClientInterface interface {
	DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeInstanceAttribute(ctx context.Context, params *ec2.DescribeInstanceAttributeInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstanceAttributeOutput, error)
	DescribeVolumes(ctx context.Context, params *ec2.DescribeVolumesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
}

type EC2InstancesPaginator interface {
	HasMorePages() bool
	NextPage(ctx context.Context, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

// These types are aliased for potential use in mocks or tests if direct struct matching is needed.
// type DescribeInstanceAttributeInput = ec2.DescribeInstanceAttributeInput
// type DescribeVolumesInput = ec2.DescribeVolumesInput
type Instance = ec2types.Instance // Alias ec2types.Instance for easier use
