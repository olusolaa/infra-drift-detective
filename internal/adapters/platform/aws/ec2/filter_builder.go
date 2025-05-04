package ec2

import (
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

const awsTagFilterPrefix = "tag:"

var ec2FilterNameMap = map[string]string{
	domain.KeyName:                      "tag:Name",
	domain.ComputeImageIDKey:            "image-id",
	domain.ComputeSubnetIDKey:           "subnet-id",
	domain.ComputeInstanceTypeKey:       "instance-type",
	domain.KeyID:                        "instance-id",
	domain.ComputeAvailabilityZoneKey:   "availability-zone",
	domain.ComputeIAMInstanceProfileKey: "iam-instance-profile.arn", // Note: Uses ARN for filtering
}

var multiValueFilters = map[string]struct{}{
	"instance-id":       {},
	"image-id":          {},
	"subnet-id":         {},
	"instance.group-id": {},
	"availability-zone": {},
	"instance-type":     {},
	"tag-key":           {},
}

func BuildEC2Filters(genericFilters map[string]string) []types.Filter {
	ec2Filters := make([]types.Filter, 0, len(genericFilters)+1)
	processedFilterNames := make(map[string]struct{})

	if genericFilters == nil {
		genericFilters = make(map[string]string)
	}

	for key, value := range genericFilters {
		filterName := ""
		filterValues := []string{value}

		if strings.HasPrefix(key, domain.TagPrefix) {
			tagName := strings.TrimPrefix(key, domain.TagPrefix)
			filterName = awsTagFilterPrefix + tagName
		} else if mappedName, ok := ec2FilterNameMap[key]; ok {
			filterName = mappedName
			if _, supportsMulti := multiValueFilters[filterName]; supportsMulti {
				filterValues = SplitFilterValue(value)
			}
		} else if key == domain.ComputeSecurityGroupsKey {
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
			processedFilterNames[sgFilterName] = struct{}{}
			continue
		} else if key == "instance-state-name" {
			filterName = key
			filterValues = SplitFilterValue(value)
		} else {
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

	stateFilterName := "instance-state-name"
	if _, stateProvided := processedFilterNames[stateFilterName]; !stateProvided {
		ec2Filters = append(ec2Filters, types.Filter{
			Name:   &stateFilterName,
			Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"},
		})
	}

	return ec2Filters
}

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
	if len(trimmedParts) == 0 {
		return []string{}
	}
	return trimmedParts
}
