package compute

import (
	"context"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/pkg/compare"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"github.com/olusolaa/infra-drift-detector/internal/resources/helper"
)

type InstanceComparer struct {
	compareFuncs map[string]helper.AttributeComparerFunc
}

func NewInstanceComparer() *InstanceComparer {
	c := &InstanceComparer{}
	c.compareFuncs = map[string]helper.AttributeComparerFunc{
		domain.KeyTags:                   c.compareTags,
		domain.ComputeSecurityGroupsKey:  helper.CompareStringSlicesUnordered, // Use generic helper directly
		domain.ComputeRootBlockDeviceKey: c.compareRootBlockDevice,
		domain.ComputeEBSBlockDevicesKey: c.compareEBSBlockDevices,
		domain.ComputeUserDataKey:        helper.DefaultAttributeCompare, // Default is suitable
	}
	return c
}

func (c *InstanceComparer) Kind() domain.ResourceKind {
	return domain.KindComputeInstance
}

func (c *InstanceComparer) Compare(
	ctx context.Context,
	desired domain.StateResource,
	actual domain.PlatformResource,
	attributesToCheck []string,
) ([]domain.AttributeDiff, error) {
	if desired == nil || actual == nil {
		return nil, errors.New(errors.CodeInternal, "compute compare called with nil desired or actual resource")
	}

	desiredAttrs := desired.Attributes()
	actualAttrs, err := actual.Attributes(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "failed to get attributes from actual resource")
	}
	diffs := make([]domain.AttributeDiff, 0)

	for _, attrKey := range attributesToCheck {
		// Check context at the beginning of each attribute comparison
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		desiredVal, dExists := desiredAttrs[attrKey]
		actualVal, aExists := actualAttrs[attrKey]

		var isEqual bool
		var details string
		var compareErr error

		if compareFunc, ok := c.compareFuncs[attrKey]; ok {
			isEqual, details, compareErr = compareFunc(ctx, desiredVal, actualVal, dExists, aExists)
		} else {
			isEqual, details, compareErr = helper.DefaultAttributeCompare(ctx, desiredVal, actualVal, dExists, aExists)
		}

		// Check context again after potentially long comparison function
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if compareErr != nil {
			diffs = append(diffs, domain.AttributeDiff{
				AttributeName: attrKey,
				ExpectedValue: desiredVal,
				ActualValue:   actualVal,
				Details:       fmt.Sprintf("Comparison error: %v", compareErr),
			})
			continue
		}

		if !isEqual {
			diffs = append(diffs, domain.AttributeDiff{
				AttributeName: attrKey,
				ExpectedValue: desiredVal,
				ActualValue:   actualVal,
				Details:       details,
			})
		}
	}

	return diffs, nil
}

func (c *InstanceComparer) compareTags(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
	return helper.CompareTags(ctx, desired, actual, dExists, aExists, "aws:")
}

func (c *InstanceComparer) compareRootBlockDevice(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
	if !dExists && !aExists {
		return true, "", nil
	}
	if !dExists {
		return false, "Root block device exists only in actual state", nil
	}
	if !aExists {
		return false, "Root block device exists only in desired state", nil
	}

	// Assume values are already normalized map[string]any during mapping phase
	normDesired, dOk := desired.(map[string]any)
	normActual, aOk := actual.(map[string]any)

	if !dOk || !aOk {
		// Fallback if types are unexpected after mapping
		return helper.DefaultAttributeCompare(ctx, desired, actual, dExists, aExists)
	}
	if normDesired == nil && normActual == nil {
		return true, "", nil
	}
	if normDesired == nil {
		return false, "Root block device configuration mismatch (desired is nil)", nil
	}
	if normActual == nil {
		return false, "Root block device configuration mismatch (actual is nil)", nil
	}

	// Use internal diff generator which respects context
	details := compare.GenerateDetailedMapDiff(ctx, normDesired, normActual)
	if ctx.Err() != nil {
		return false, "", ctx.Err()
	}

	isEqual := details == ""
	return isEqual, details, nil
}

func (c *InstanceComparer) compareEBSBlockDevices(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
	// Use generic unordered map slice comparison helper
	return helper.CompareSliceOfMapsUnordered(ctx, desired, actual, dExists, aExists, "device_name", "EBS Block Device")
}
