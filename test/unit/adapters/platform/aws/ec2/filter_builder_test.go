package ec2_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/ec2"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/stretchr/testify/assert"
)

func TestBuildEC2Filters(t *testing.T) {
	tests := []struct {
		name           string
		genericFilters map[string]string
		want           []types.Filter
	}{
		{
			name:           "empty filters returns only default state filter",
			genericFilters: map[string]string{},
			want: []types.Filter{
				{
					Name:   ptr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "name tag filter is correctly mapped",
			genericFilters: map[string]string{
				domain.KeyName: "test-instance",
			},
			want: []types.Filter{
				{
					Name:   ptr("tag:Name"),
					Values: []string{"test-instance"},
				},
				{
					Name:   ptr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "custom tag filter is correctly mapped",
			genericFilters: map[string]string{
				domain.TagPrefix + "Environment": "production",
			},
			want: []types.Filter{
				{
					Name:   ptr("tag:Environment"),
					Values: []string{"production"},
				},
				{
					Name:   ptr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "multiple tag filters are correctly mapped",
			genericFilters: map[string]string{
				domain.TagPrefix + "Environment": "production",
				domain.TagPrefix + "Project":     "infradetector",
			},
			want: []types.Filter{
				{
					Name:   ptr("tag:Environment"),
					Values: []string{"production"},
				},
				{
					Name:   ptr("tag:Project"),
					Values: []string{"infradetector"},
				},
				{
					Name:   ptr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "instance attributes are correctly mapped",
			genericFilters: map[string]string{
				domain.ComputeImageIDKey:      "ami-12345",
				domain.ComputeSubnetIDKey:     "subnet-12345",
				domain.ComputeInstanceTypeKey: "t2.micro",
			},
			want: []types.Filter{
				{
					Name:   ptr("image-id"),
					Values: []string{"ami-12345"},
				},
				{
					Name:   ptr("subnet-id"),
					Values: []string{"subnet-12345"},
				},
				{
					Name:   ptr("instance-type"),
					Values: []string{"t2.micro"},
				},
				{
					Name:   ptr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "security groups are correctly mapped to multiple filters",
			genericFilters: map[string]string{
				domain.ComputeSecurityGroupsKey: "sg-12345,sg-67890",
			},
			want: []types.Filter{
				{
					Name:   ptr("instance.group-id"),
					Values: []string{"sg-12345"},
				},
				{
					Name:   ptr("instance.group-id"),
					Values: []string{"sg-67890"},
				},
				{
					Name:   ptr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "multi-value filters are correctly split",
			genericFilters: map[string]string{
				domain.ComputeImageIDKey:  "ami-12345,ami-67890",
				domain.ComputeSubnetIDKey: "subnet-12345, subnet-67890",
			},
			want: []types.Filter{
				{
					Name:   ptr("image-id"),
					Values: []string{"ami-12345", "ami-67890"},
				},
				{
					Name:   ptr("subnet-id"),
					Values: []string{"subnet-12345", "subnet-67890"},
				},
				{
					Name:   ptr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "state filter provided by user overrides default",
			genericFilters: map[string]string{
				"instance-state-name": "running",
			},
			want: []types.Filter{
				{
					Name:   ptr("instance-state-name"),
					Values: []string{"running"},
				},
			},
		},
		{
			name: "unmapped filters are ignored",
			genericFilters: map[string]string{
				"non-existent-filter": "value",
			},
			want: []types.Filter{
				{
					Name:   ptr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "availability zone filter is correctly mapped",
			genericFilters: map[string]string{
				domain.ComputeAvailabilityZoneKey: "us-west-2a",
			},
			want: []types.Filter{
				{
					Name:   ptr("availability-zone"),
					Values: []string{"us-west-2a"},
				},
				{
					Name:   ptr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "mixed tag and attribute filters are correctly mapped",
			genericFilters: map[string]string{
				domain.TagPrefix + "Environment": "production",
				domain.ComputeInstanceTypeKey:    "t2.micro",
				domain.KeyID:                     "i-12345",
			},
			want: []types.Filter{
				{
					Name:   ptr("tag:Environment"),
					Values: []string{"production"},
				},
				{
					Name:   ptr("instance-type"),
					Values: []string{"t2.micro"},
				},
				{
					Name:   ptr("instance-id"),
					Values: []string{"i-12345"},
				},
				{
					Name:   ptr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "empty tag value is correctly mapped",
			genericFilters: map[string]string{
				domain.TagPrefix + "Empty": "",
			},
			want: []types.Filter{
				{
					Name:   ptr("tag:Empty"),
					Values: []string{""},
				},
				{
					Name:   ptr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ec2.BuildEC2Filters(tt.genericFilters)

			assert.True(t, compareFilters(got, tt.want), "BuildEC2Filters() returned unexpected filters")
		})
	}
}

func TestSplitFilterValue(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{
			name:  "single value",
			value: "single",
			want:  []string{"single"},
		},
		{
			name:  "multiple values",
			value: "value1,value2,value3",
			want:  []string{"value1", "value2", "value3"},
		},
		{
			name:  "values with spaces",
			value: "value1, value2 , value3",
			want:  []string{"value1", "value2", "value3"},
		},
		{
			name:  "empty values are skipped",
			value: "value1,,value3, ,value5",
			want:  []string{"value1", "value3", "value5"},
		},
		{
			name:  "single comma returns empty slice",
			value: ",",
			want:  []string{},
		},
		{
			name:  "empty string returns single empty string",
			value: "",
			want:  []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ec2.SplitFilterValue(tt.value)
			assert.Equal(t, tt.want, got, "SplitFilterValue() returned unexpected result")
		})
	}
}

func TestNilFilters(t *testing.T) {
	got := ec2.BuildEC2Filters(nil)
	stateFilterName := "instance-state-name"
	assert.Equal(t, 1, len(got), "BuildEC2Filters(nil) should return only the default state filter")
	assert.Equal(t, &stateFilterName, got[0].Name, "BuildEC2Filters(nil) should return the default state filter")
	assert.Equal(t, []string{"pending", "running", "shutting-down", "stopping", "stopped"}, got[0].Values,
		"BuildEC2Filters(nil) should return the default state values")
}

func ptr(s string) *string {
	return &s
}

func compareFilters(a, b []types.Filter) bool {
	if len(a) != len(b) {
		return false
	}

	aMap := make(map[string][]string)
	for _, filter := range a {
		if filter.Name != nil {
			aMap[*filter.Name] = filter.Values
		}
	}

	bMap := make(map[string][]string)
	for _, filter := range b {
		if filter.Name != nil {
			bMap[*filter.Name] = filter.Values
		}
	}

	aGroupIDs, aHasGroups := aMap["instance.group-id"]
	bGroupIDs, bHasGroups := bMap["instance.group-id"]

	if aHasGroups && bHasGroups {
		if !compareStringArrays(aGroupIDs, bGroupIDs) {
			return false
		}
		delete(aMap, "instance.group-id")
		delete(bMap, "instance.group-id")
	} else if aHasGroups != bHasGroups {
		return false
	}

	for name, values := range aMap {
		bValues, exists := bMap[name]
		if !exists {
			return false
		}
		if !compareStringArrays(values, bValues) {
			return false
		}
	}

	for name := range bMap {
		if _, exists := aMap[name]; !exists {
			return false
		}
	}

	return true
}

func compareStringArrays(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	aCount := make(map[string]int)
	for _, s := range a {
		aCount[s]++
	}

	bCount := make(map[string]int)
	for _, s := range b {
		bCount[s]++
	}

	for s, count := range aCount {
		if bCount[s] != count {
			return false
		}
	}

	for s, count := range bCount {
		if aCount[s] != count {
			return false
		}
	}

	return true
}
