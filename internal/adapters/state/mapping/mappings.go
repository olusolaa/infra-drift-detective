package mapping

import (
	"fmt"

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

// Add maps for other supported kinds (e.g., dbInstanceAttrMap)

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
		// Kind not specifically handled, maybe perform generic copy?
		// For robustness, let's only copy explicitly mapped fields for known kinds.
		// Log warning?
		return fmt.Errorf("no attribute mapping defined for kind: %s", kind)
	}

	// Use mapstructure for more robust decoding if needed, but complicates simple copies.
	// Let's stick to direct access + specific handlers for now.

	for tfKey, domainKey := range attrMap {
		rawValue, exists := rawAttrs[tfKey]
		if !exists {
			continue // Skip attributes not present in the state
		}

		// Apply specific normalization/handling based on domain key or kind
		switch domainKey {
		case domain.KeyTags:
			targetAttrs[domainKey] = normalizeTags(rawValue)
		case domain.ComputeRootBlockDeviceKey:
			targetAttrs[domainKey] = normalizeSingleBlockDevice(rawValue, true)
		case domain.ComputeEBSBlockDevicesKey:
			targetAttrs[domainKey] = normalizeBlockDeviceSlice(rawValue)
		case domain.StorageBucketVersioningKey: // Example for S3
			targetAttrs[domainKey] = normalizeVersioning(rawValue)
		// Add other special handling cases here...
		default:
			// Default: direct copy (potentially add type normalization later if needed)
			targetAttrs[domainKey] = rawValue
		}
	}

	// Ensure required domain keys like Name are populated (e.g., from tags)
	if _, exists := targetAttrs[domain.KeyName]; !exists {
		if tags, ok := targetAttrs[domain.KeyTags].(map[string]string); ok {
			if nameVal, nameOk := tags["Name"]; nameOk {
				targetAttrs[domain.KeyName] = nameVal
			}
		}
	}

	return nil
}

// normalizeTags converts map[string]any to map[string]string
func normalizeTags(rawVal any) map[string]string {
	stringTags := make(map[string]string)
	if tagsVal, ok := rawVal.(map[string]any); ok {
		for k, v := range tagsVal {
			if vStr, vOk := v.(string); vOk {
				stringTags[k] = vStr
			}
		}
	}
	return stringTags
}

// normalizeVersioning extracts the boolean 'enabled' field.
func normalizeVersioning(rawVal any) bool {
	// TF state often represents blocks as lists of maps, even if only one is allowed.
	if versioningList, ok := rawVal.([]any); ok && len(versioningList) > 0 {
		if vMap, okMap := versioningList[0].(map[string]any); okMap {
			if enabled, okEnable := vMap["enabled"].(bool); okEnable {
				return enabled
			}
		}
	}
	return false // Default if structure doesn't match or enabled not found/bool
}

// normalizeSingleBlockDevice handles the structure from TF state (list of maps).
func normalizeSingleBlockDevice(rawVal any, isRoot bool) map[string]any {
	if blockList, ok := rawVal.([]any); ok && len(blockList) > 0 {
		if blockMap, okMap := blockList[0].(map[string]any); okMap {
			// Reuse the normalization logic from the comparer (or move it to domain/util?)
			// For now, keep the logic here.
			return normalizeBlockDeviceMap(blockMap, isRoot)
		}
	}
	return nil // Return nil if structure invalid or list empty
}

func normalizeBlockDeviceSlice(rawVal any) []map[string]any {
	if blockList, ok := rawVal.([]any); ok && len(blockList) > 0 {
		normalizedBlocks := make([]map[string]any, 0, len(blockList))
		for _, item := range blockList {
			if blockMap, okMap := item.(map[string]any); okMap {
				normMap := normalizeBlockDeviceMap(blockMap, false) // isRoot = false
				if normMap != nil {
					normalizedBlocks = append(normalizedBlocks, normMap)
				}
			}
		}
		return normalizedBlocks
	}
	return nil // Return nil or empty slice? Let's return nil.
}

// normalizeBlockDeviceMap standardizes a single block device map from state.
// (Similar logic to the one previously in comparer, potentially move to shared location)
func normalizeBlockDeviceMap(rawMap map[string]any, isRoot bool) map[string]any {
	normMap := make(map[string]any)
	// Copy explicitly known/comparable fields using state keys
	copyIfPresentMap(rawMap, normMap, "device_name", "device_name")
	copyIfPresentMap(rawMap, normMap, "volume_type", "volume_type")
	copyIfPresentMap(rawMap, normMap, "volume_size", "volume_size")
	copyIfPresentMap(rawMap, normMap, "iops", "iops")
	copyIfPresentMap(rawMap, normMap, "throughput", "throughput")
	copyIfPresentMap(rawMap, normMap, "encrypted", "encrypted")
	copyIfPresentMap(rawMap, normMap, "kms_key_id", "kms_key_id")
	copyIfPresentMap(rawMap, normMap, "delete_on_termination", "delete_on_termination")
	// copyIfPresentMap(rawMap, normMap, "snapshot_id", "snapshot_id")
	// copyIfPresentMap(rawMap, normMap, "volume_id", "volume_id") // Usually ignored

	// Type normalization (reuse logic or keep here)
	// normalizeIntField(normMap, "volume_size") ... etc.

	// Apply defaults AFTER copying
	if _, exists := normMap["delete_on_termination"]; !exists {
		normMap["delete_on_termination"] = isRoot
	}
	// Add other defaults if needed

	return normMap
}

// copyIfPresentMap helper
func copyIfPresentMap(src, dest map[string]any, srcKey, destKey string) {
	if val, ok := src[srcKey]; ok {
		dest[destKey] = val
	}
}
