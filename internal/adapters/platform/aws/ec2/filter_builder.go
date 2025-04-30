package ec2

import (
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

const awsTagFilterPrefix = "tag:"

// Defines mapping from generic domain filter keys to EC2 API filter names.
var ec2FilterNameMap = map[string]string{
	domain.KeyName:            "tag:Name",
	domain.ComputeImageIDKey:  "image-id",
	domain.ComputeSubnetIDKey: "subnet-id",
	// Map security group IDs requires different logic (below)
	// domain.ComputeSecurityGroupsKey: "instance.group-id", // This takes single value
	domain.ComputeInstanceTypeKey:     "instance-type",
	domain.KeyID:                      "instance-id", // Allow filtering by specific ID
	domain.ComputeAvailabilityZoneKey: "availability-zone",
	// Add other direct mappings as needed
}

// Filters that inherently support multiple values in the EC2 API
var multiValueFilters = map[string]struct{}{
	"instance-id":       {},
	"image-id":          {},
	"subnet-id":         {},
	"instance.group-id": {},
	"availability-zone": {},
	"instance-type":     {},
	// Add others like 'instance-state-name' etc. if needed
}

func BuildEC2Filters(genericFilters map[string]string) []types.Filter {
	ec2Filters := make([]types.Filter, 0, len(genericFilters)+1) // +1 for default state filter
	processedFilterNames := make(map[string]struct{})            // Track filters added to handle overrides

	for key, value := range genericFilters {
		filterName := ""
		filterValues := []string{value} // Start with single value assumption

		if strings.HasPrefix(key, domain.TagPrefix) {
			tagName := strings.TrimPrefix(key, domain.TagPrefix)
			filterName = awsTagFilterPrefix + tagName
			// Tag filters generally support multiple values if comma-separated,
			// but DescribeInstances API usually expects one value per tag filter entry.
			// For simplicity, treat tags as single value unless requirements dictate otherwise.
		} else if mappedName, ok := ec2FilterNameMap[key]; ok {
			filterName = mappedName
			// Check if this filter supports multiple values based on our list
			// and split the input value if it does.
			if _, supportsMulti := multiValueFilters[filterName]; supportsMulti {
				filterValues = SplitFilterValue(value)
			}
		} else if key == domain.ComputeSecurityGroupsKey {
			// Special handling for security groups - needs multiple filters
			sgIDs := SplitFilterValue(value)
			sgFilterName := "instance.group-id"
			for _, sgID := range sgIDs {
				if sgID != "" {
					ec2Filters = append(ec2Filters, types.Filter{
						Name:   &sgFilterName,
						Values: []string{sgID},
					})
				}
			}
			processedFilterNames[sgFilterName] = struct{}{} // Mark as processed
			continue                                        // Skip adding the main filter below for this key
		} else if key == "instance-state-name" {
			filterName = key
			filterValues = SplitFilterValue(value)
		} else {
			// Key is not a tag and not explicitly mapped, ignore it.
			// TODO: Add logging here for ignored filter keys?
			continue
		}

		if filterName != "" {
			ec2Filters = append(ec2Filters, types.Filter{
				Name:   &filterName,
				Values: filterValues,
			})
			processedFilterNames[filterName] = struct{}{}
		}
	}

	// --- Robust Default Instance State Filter ---
	// Only add default non-terminated filter if 'instance-state-name' wasn't provided by user.
	stateFilterName := "instance-state-name"
	if _, stateProvided := processedFilterNames[stateFilterName]; !stateProvided {
		ec2Filters = append(ec2Filters, types.Filter{
			Name:   &stateFilterName,
			Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
		})
	}

	if len(ec2Filters) == 0 {
		return nil
	}
	return ec2Filters
}

// SplitFilterValue handles comma-separated values for filters that support multiple values.
func SplitFilterValue(value string) []string {
	if !strings.Contains(value, ",") {
		return []string{value}
	}
	parts := strings.Split(value, ",")
	trimmedParts := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			trimmedParts = append(trimmedParts, trimmed)
		}
	}
	return trimmedParts
}
