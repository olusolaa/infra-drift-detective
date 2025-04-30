package mapping

import (
	"fmt"
	"strconv"
	// mapstructure needed if used
	// "github.com/mitchellh/mapstructure"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

// --- Type Mapping ---

// Maps Terraform type strings to domain Kinds
var tfTypeToDomainKindMap = map[string]domain.ResourceKind{
	"aws_instance":    domain.KindComputeInstance,
	"aws_s3_bucket":   domain.KindStorageBucket,
	"aws_db_instance": domain.KindDatabaseInstance,
	// Add all supported TF resource types here
}

func MapTfTypeToDomainKind(tfType string) (domain.ResourceKind, error) {
	kind, exists := tfTypeToDomainKindMap[tfType]
	if !exists {
		return "", fmt.Errorf("unsupported Terraform resource type: %s", tfType)
	}
	return kind, nil
}

// --- Attribute Mapping ---

// Defines mapping from TF state attribute keys to domain keys for a specific domain Kind
type attributeMapDefinition map[string]string // tfStateKey -> domainKey

var computeInstanceAttrMap = attributeMapDefinition{
	"instance_type":          domain.ComputeInstanceTypeKey,
	"ami":                    domain.ComputeImageIDKey,
	"subnet_id":              domain.ComputeSubnetIDKey,
	"vpc_security_group_ids": domain.ComputeSecurityGroupsKey,
	"iam_instance_profile":   domain.ComputeIAMInstanceProfileKey, // Assuming name from state for now
	"user_data":              domain.ComputeUserDataKey,           // Raw user data
	"availability_zone":      domain.ComputeAvailabilityZoneKey,
	"root_block_device":      domain.ComputeRootBlockDeviceKey, // Needs special handling
	"ebs_block_device":       domain.ComputeEBSBlockDevicesKey, // Needs special handling
	"tags":                   domain.KeyTags,                   // Common key
	"id":                     domain.KeyID,                     // Common key
	"arn":                    domain.KeyARN,                    // Common key
	// Add other aws_instance attributes to map
}

var s3BucketAttrMap = attributeMapDefinition{
	"acl":                                  domain.StorageBucketACLKey,
	"versioning":                           domain.StorageBucketVersioningKey, // Added mapping for versioning
	"lifecycle_rule":                       domain.StorageBucketLifecycleRulesKey,
	"logging":                              domain.StorageBucketLoggingKey,
	"website":                              domain.StorageBucketWebsiteKey,
	"cors_rule":                            domain.StorageBucketCorsRulesKey,
	"policy":                               domain.StorageBucketPolicyKey,
	"server_side_encryption_configuration": domain.StorageBucketEncryptionKey,
	"tags":                                 domain.KeyTags,   // Common key
	"id":                                   domain.KeyID,     // Common key (bucket name)
	"arn":                                  domain.KeyARN,    // Common key
	"region":                               domain.KeyRegion, // Common key
	// Add other aws_s3_bucket attributes to map
}

// --- Normalization Functions (Return Errors) ---

// GetAttributeMapForKind returns the appropriate attribute mapping definition.
func getAttributeMapForKind(kind domain.ResourceKind) attributeMapDefinition {
	switch kind {
	case domain.KindComputeInstance:
		return computeInstanceAttrMap
	case domain.KindStorageBucket:
		return s3BucketAttrMap
	// Add cases for other kinds
	default:
		return nil // Or an empty map? Returning nil indicates unsupported kind for mapping.
	}
}

// --- Normalization Functions ---

// NormalizeAndCopyAttributes uses the mappings and kind-specific logic.
func NormalizeAndCopyAttributes(kind domain.ResourceKind, rawAttrs map[string]any, targetAttrs map[string]any) error {
	attrMap := getAttributeMapForKind(kind)
	if attrMap == nil {
		return fmt.Errorf("no attribute mapping defined for kind: %s", kind)
	}

	var firstErr error // Collect first error encountered

	for tfKey, domainKey := range attrMap {
		rawValue, exists := rawAttrs[tfKey]
		if !exists {
			continue
		}

		var normalizedValue any
		var err error

		switch domainKey {
		case domain.KeyTags:
			normalizedValue, err = normalizeTags(rawValue)
		case domain.ComputeRootBlockDeviceKey:
			normalizedValue, err = normalizeSingleBlockDevice(rawValue, true)
		case domain.ComputeEBSBlockDevicesKey:
			normalizedValue, err = normalizeBlockDeviceSlice(rawValue)
		case domain.StorageBucketVersioningKey:
			normalizedValue, err = normalizeVersioning(rawValue)
		// Add other special handling cases here...
		default:
			// Default: direct copy - TODO: add type normalization if needed
			normalizedValue = rawValue
			err = nil
		}

		if err != nil {
			err = fmt.Errorf("error normalizing attribute '%s' (domain key '%s'): %w", tfKey, domainKey, err)
			if firstErr == nil {
				firstErr = err
			} // Store first error
			// Continue processing other attributes? Or fail fast? Let's continue for now.
			continue // Skip storing value if normalization failed
		}

		// Only store if normalization didn't error out
		if normalizedValue != nil { // Avoid storing explicit nils unless intended
			targetAttrs[domainKey] = normalizedValue
		}
	}

	// Ensure Name key populated after tags are processed
	if _, exists := targetAttrs[domain.KeyName]; !exists {
		if tags, ok := targetAttrs[domain.KeyTags].(map[string]string); ok {
			if nameVal, nameOk := tags["Name"]; nameOk {
				targetAttrs[domain.KeyName] = nameVal
			}
		}
	}

	return firstErr // Return the first error encountered during normalization
}

// normalizeTags converts map[string]any to map[string]string, handles simple types, returns error on bad input.
func normalizeTags(rawVal any) (map[string]string, error) {
	if rawVal == nil {
		return make(map[string]string), nil
	} // Handle nil input gracefully

	tagsVal, ok := rawVal.(map[string]any)
	if !ok {
		// If it's already the correct type, return it
		if tagsStrMap, okStr := rawVal.(map[string]string); okStr {
			return tagsStrMap, nil
		}
		return nil, fmt.Errorf("invalid type for tags: expected map[string]any or map[string]string, got %T", rawVal)
	}

	stringTags := make(map[string]string)
	for k, v := range tagsVal {
		if v == nil {
			stringTags[k] = "" // Treat nil value as empty string tag
			continue
		}
		// Convert common scalar types to string
		switch vt := v.(type) {
		case string:
			stringTags[k] = vt
		case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
			stringTags[k] = fmt.Sprintf("%v", vt)
		default:
			// Cannot convert complex type, return error or skip? Let's return error.
			return nil, fmt.Errorf("invalid type for tag value '%s': expected scalar, got %T", k, v)
		}
	}
	return stringTags, nil
}

// normalizeVersioning extracts boolean, returns error on bad input.
func normalizeVersioning(rawVal any) (bool, error) {
	if rawVal == nil {
		return false, nil
	} // Default if block missing

	versioningList, ok := rawVal.([]any)
	if !ok || len(versioningList) == 0 {
		// Maybe it's already a bool from HCL eval? Check that too.
		if bVal, okBool := rawVal.(bool); okBool {
			return bVal, nil
		}
		// Assume missing or empty list means disabled if not bool
		return false, nil
	}

	vMap, okMap := versioningList[0].(map[string]any)
	if !okMap {
		return false, fmt.Errorf("invalid structure for versioning block: expected map, got %T", versioningList[0])
	}

	enabledVal, enabledExists := vMap["enabled"]
	if !enabledExists {
		return false, nil // Default to false if 'enabled' key is missing
	}

	enabled, okEnable := enabledVal.(bool)
	if !okEnable {
		// Attempt string conversion
		if strVal, okStr := enabledVal.(string); okStr {
			if b, err := strconv.ParseBool(strVal); err == nil {
				return b, nil
			}
		}
		return false, fmt.Errorf("invalid type for versioning 'enabled': expected bool, got %T", enabledVal)
	}
	return enabled, nil
}

// normalizeSingleBlockDevice handles the structure from TF state, returns error.
func normalizeSingleBlockDevice(rawVal any, isRoot bool) (map[string]any, error) {
	if rawVal == nil {
		return nil, nil
	} // No block defined is valid

	blockList, ok := rawVal.([]any)
	if !ok || len(blockList) == 0 {
		// Could be a single map from HCL eval?
		if blockMap, okMap := rawVal.(map[string]any); okMap {
			return normalizeBlockDeviceMap(blockMap, isRoot) // Try normalizing directly
		}
		return nil, nil // Invalid structure or empty list is treated as no block
	}

	if len(blockList) > 1 && isRoot {
		// This check might be redundant if TF schema prevents it, but good defense
		return nil, fmt.Errorf("multiple root_block_device blocks found in state/HCL (only one allowed)")
	}

	blockMap, okMap := blockList[0].(map[string]any)
	if !okMap {
		return nil, fmt.Errorf("invalid structure for block device item: expected map, got %T", blockList[0])
	}
	return normalizeBlockDeviceMap(blockMap, isRoot)
}

// normalizeBlockDeviceSlice handles the list of EBS devices, returns error.
func normalizeBlockDeviceSlice(rawVal any) ([]map[string]any, error) {
	if rawVal == nil {
		return nil, nil
	} // No devices is valid

	blockList, ok := rawVal.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid type for ebs_block_device list: expected slice, got %T", rawVal)
	}

	if len(blockList) == 0 {
		return nil, nil
	} // Empty list is valid

	normalizedBlocks := make([]map[string]any, 0, len(blockList))
	for i, item := range blockList {
		blockMap, okMap := item.(map[string]any)
		if !okMap {
			return nil, fmt.Errorf("invalid structure for ebs_block_device item %d: expected map, got %T", i, item)
		}
		normMap, err := normalizeBlockDeviceMap(blockMap, false) // isRoot = false
		if err != nil {
			return nil, fmt.Errorf("error normalizing ebs_block_device item %d: %w", i, err)
		}
		if normMap != nil { // Only append if normalization succeeded
			normalizedBlocks = append(normalizedBlocks, normMap)
		}
	}

	if len(normalizedBlocks) == 0 {
		return nil, nil
	} // Return nil if no valid blocks found after normalization
	return normalizedBlocks, nil
}

// normalizeBlockDeviceMap standardizes a single block device map, returns error.
func normalizeBlockDeviceMap(rawMap map[string]any, isRoot bool) (map[string]any, error) {
	if rawMap == nil {
		return nil, nil
	} // Handle nil input map gracefully

	normMap := make(map[string]any)
	var firstErr error

	copyIfPresentMap(rawMap, normMap, "device_name", "device_name")
	copyIfPresentMap(rawMap, normMap, "volume_type", "volume_type")
	copyIfPresentMap(rawMap, normMap, "kms_key_id", "kms_key_id")
	copyIfPresentMap(rawMap, normMap, "snapshot_id", "snapshot_id") // Added snapshot_id

	// Fields needing type normalization
	if err := normalizeBooleanField(rawMap, normMap, "encrypted"); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("field 'encrypted': %w", err)
	}
	if err := normalizeBooleanField(rawMap, normMap, "delete_on_termination"); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("field 'delete_on_termination': %w", err)
	}
	if err := normalizeIntFieldMap(rawMap, normMap, "volume_size"); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("field 'volume_size': %w", err)
	}
	if err := normalizeIntFieldMap(rawMap, normMap, "iops"); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("field 'iops': %w", err)
	}
	if err := normalizeIntFieldMap(rawMap, normMap, "throughput"); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("field 'throughput': %w", err)
	}

	// Apply defaults AFTER copying and normalization attempts
	if _, exists := normMap["delete_on_termination"]; !exists {
		normMap["delete_on_termination"] = isRoot
	}
	if _, exists := normMap["encrypted"]; !exists {
		// Should we default encrypted? AWS default depends on factors.
		// Let's NOT default it, compare only if present in source.
	}

	return normMap, firstErr // Return map even if errors occurred, along with first error
}

// copyIfPresentMap helper
func copyIfPresentMap(src, dest map[string]any, srcKey, destKey string) {
	if val, ok := src[srcKey]; ok && val != nil { // Also check for nil
		dest[destKey] = val
	}
}

// normalizeIntFieldMap attempts conversion, returns error on failure.
func normalizeIntFieldMap(src, dest map[string]any, key string) error {
	if val, ok := src[key]; ok && val != nil {
		switch v := val.(type) {
		case float64:
			dest[key] = int64(v)
			return nil
		case float32:
			dest[key] = int64(v)
			return nil
		case int:
			dest[key] = int64(v)
			return nil
		case int8:
			dest[key] = int64(v)
			return nil
		case int16:
			dest[key] = int64(v)
			return nil
		case int32:
			dest[key] = int64(v)
			return nil
		case int64:
			dest[key] = v
			return nil // Already int64
		case uint:
			dest[key] = int64(v)
			return nil
		case uint8:
			dest[key] = int64(v)
			return nil
		case uint16:
			dest[key] = int64(v)
			return nil
		case uint32:
			dest[key] = int64(v)
			return nil
		case uint64:
			dest[key] = int64(v)
			return nil // Potential overflow, but common case
		case string:
			if i, err := strconv.ParseInt(v, 10, 64); err == nil {
				dest[key] = i
				return nil
			} else if f, err := strconv.ParseFloat(v, 64); err == nil {
				// If string represents float, convert to int64
				dest[key] = int64(f)
				return nil
			} else {
				return fmt.Errorf("cannot convert string '%s' to number", v)
			}
		default:
			return fmt.Errorf("unhandled type %T for numeric conversion", v)
		}
	}
	// Key not present or nil, no error
	return nil
}

// normalizeBooleanField attempts conversion, returns error on failure.
func normalizeBooleanField(src, dest map[string]any, key string) error {
	if val, ok := src[key]; ok && val != nil {
		switch v := val.(type) {
		case bool:
			dest[key] = v
			return nil
		case string:
			if b, err := strconv.ParseBool(v); err == nil {
				dest[key] = b
				return nil
			} else {
				return fmt.Errorf("cannot convert string '%s' to bool", v)
			}
		// Add cases for numbers (0=false, non-zero=true)? Or be strict? Let's be strict.
		default:
			return fmt.Errorf("unhandled type %T for boolean conversion", v)
		}
	}
	// Key not present or nil, no error
	return nil
}
