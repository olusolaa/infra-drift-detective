package ec2

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/stretchr/testify/assert"
)

func TestSplitFilterValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: []string{""},
		},
		{
			name:     "single value",
			input:    "value1",
			expected: []string{"value1"},
		},
		{
			name:     "multiple values",
			input:    "value1,value2,value3",
			expected: []string{"value1", "value2", "value3"},
		},
		{
			name:     "values with spaces",
			input:    "value1, value2 , value3",
			expected: []string{"value1", "value2", "value3"},
		},
		{
			name:     "values with empty parts",
			input:    "value1,,value3",
			expected: []string{"value1", "value3"},
		},
		{
			name:     "all empty parts",
			input:    ",,",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SplitFilterValue(tt.input)
			assert.Equal(t, tt.expected, result, "SplitFilterValue(%q) = %v, want %v", tt.input, result, tt.expected)
		})
	}
}

func TestBuildEC2Filters(t *testing.T) {
	tests := []struct {
		name           string
		genericFilters map[string]string
		expected       []types.Filter
	}{
		{
			name:           "nil filters",
			genericFilters: nil,
			expected: []types.Filter{
				{
					Name:   stringPtr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name:           "empty filters",
			genericFilters: map[string]string{},
			expected: []types.Filter{
				{
					Name:   stringPtr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "tag filter",
			genericFilters: map[string]string{
				domain.TagPrefix + "Environment": "production",
			},
			expected: []types.Filter{
				{
					Name:   stringPtr("tag:Environment"),
					Values: []string{"production"},
				},
				{
					Name:   stringPtr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "mapped filters",
			genericFilters: map[string]string{
				domain.ComputeImageIDKey:      "ami-12345",
				domain.ComputeInstanceTypeKey: "t2.micro",
			},
			expected: []types.Filter{
				{
					Name:   stringPtr("image-id"),
					Values: []string{"ami-12345"},
				},
				{
					Name:   stringPtr("instance-type"),
					Values: []string{"t2.micro"},
				},
				{
					Name:   stringPtr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "security groups filter",
			genericFilters: map[string]string{
				domain.ComputeSecurityGroupsKey: "sg-123,sg-456",
			},
			expected: []types.Filter{
				{
					Name:   stringPtr("instance.group-id"),
					Values: []string{"sg-123"},
				},
				{
					Name:   stringPtr("instance.group-id"),
					Values: []string{"sg-456"},
				},
				{
					Name:   stringPtr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "instance-state-name filter",
			genericFilters: map[string]string{
				"instance-state-name": "running",
			},
			expected: []types.Filter{
				{
					Name:   stringPtr("instance-state-name"),
					Values: []string{"running"},
				},
			},
		},
		{
			name: "multi-value filter",
			genericFilters: map[string]string{
				domain.KeyID: "i-123,i-456",
			},
			expected: []types.Filter{
				{
					Name:   stringPtr("instance-id"),
					Values: []string{"i-123", "i-456"},
				},
				{
					Name:   stringPtr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "unsupported filter key",
			genericFilters: map[string]string{
				"unsupported-key": "value",
			},
			expected: []types.Filter{
				{
					Name:   stringPtr("instance-state-name"),
					Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
				},
			},
		},
		{
			name: "mixed filters",
			genericFilters: map[string]string{
				domain.TagPrefix + "Name":       "web-server",
				domain.ComputeInstanceTypeKey:   "t2.micro,t3.small",
				domain.ComputeSecurityGroupsKey: "sg-123",
				"unsupported-key":               "value",
				"instance-state-name":           "running,stopped",
			},
			expected: []types.Filter{
				{
					Name:   stringPtr("tag:Name"),
					Values: []string{"web-server"},
				},
				{
					Name:   stringPtr("instance-type"),
					Values: []string{"t2.micro", "t3.small"},
				},
				{
					Name:   stringPtr("instance.group-id"),
					Values: []string{"sg-123"},
				},
				{
					Name:   stringPtr("instance-state-name"),
					Values: []string{"running", "stopped"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildEC2Filters(tt.genericFilters)
			assertFiltersEqual(t, tt.expected, result)
		})
	}
}

// Helper function to compare filters since the order might be different
func assertFiltersEqual(t *testing.T, expected, actual []types.Filter) {
	assert.Equal(t, len(expected), len(actual), "Filter count doesn't match")

	// Create maps to make comparison easier
	expectedMap := make(map[string][]string)
	for _, filter := range expected {
		expectedMap[*filter.Name] = filter.Values
	}

	actualMap := make(map[string][]string)
	for _, filter := range actual {
		actualMap[*filter.Name] = filter.Values
	}

	for name, values := range expectedMap {
		actualValues, exists := actualMap[name]
		if !assert.True(t, exists, "Expected filter %s not found", name) {
			continue
		}

		// For filters that can have multiple values in different order
		if name == "instance.group-id" {
			assert.ElementsMatch(t, values, actualValues, "Values for filter %s don't match", name)
		} else {
			assert.Equal(t, values, actualValues, "Values for filter %s don't match", name)
		}
	}
}

// Helper function to create string pointers
func stringPtr(s string) *string {
	return &s
}
