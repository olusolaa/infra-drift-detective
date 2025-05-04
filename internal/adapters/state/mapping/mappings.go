package mapping

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

var tfTypeToDomainKindMap = map[string]domain.ResourceKind{
	"aws_instance":    domain.KindComputeInstance,
	"aws_s3_bucket":   domain.KindStorageBucket,
	"aws_db_instance": domain.KindDatabaseInstance,
}

func MapTfTypeToDomainKind(tfType string) (domain.ResourceKind, error) {
	kind, exists := tfTypeToDomainKindMap[tfType]
	if !exists {
		return "", errors.New(errors.CodeNotImplemented, fmt.Sprintf("unsupported Terraform resource type: %s", tfType))
	}
	return kind, nil
}

type attributeMapDefinition map[string]string

var computeInstanceAttrMap = attributeMapDefinition{
	"instance_type":          domain.ComputeInstanceTypeKey,
	"ami":                    domain.ComputeImageIDKey,
	"subnet_id":              domain.ComputeSubnetIDKey,
	"vpc_security_group_ids": domain.ComputeSecurityGroupsKey,
	"iam_instance_profile":   domain.ComputeIAMInstanceProfileKey,
	"user_data":              domain.ComputeUserDataKey,
	"availability_zone":      domain.ComputeAvailabilityZoneKey,
	"root_block_device":      domain.ComputeRootBlockDeviceKey,
	"ebs_block_device":       domain.ComputeEBSBlockDevicesKey,
	"tags":                   domain.KeyTags,
	"id":                     domain.KeyID,
	"arn":                    domain.KeyARN,
}

var s3BucketAttrMap = attributeMapDefinition{
	"acl":                                  domain.StorageBucketACLKey,
	"versioning":                           domain.StorageBucketVersioningKey,
	"logging":                              domain.StorageBucketLoggingKey,
	"website":                              domain.StorageBucketWebsiteKey,
	"cors_rule":                            domain.StorageBucketCorsRulesKey,
	"lifecycle_rule":                       domain.StorageBucketLifecycleRulesKey,
	"policy":                               domain.StorageBucketPolicyKey,
	"server_side_encryption_configuration": domain.StorageBucketEncryptionKey,
	"tags":                                 domain.KeyTags,
	"id":                                   domain.KeyID,
	"arn":                                  domain.KeyARN,
	"region":                               domain.KeyRegion,
}

func getAttributeMapForKind(kind domain.ResourceKind) attributeMapDefinition {
	switch kind {
	case domain.KindComputeInstance:
		return computeInstanceAttrMap
	case domain.KindStorageBucket:
		return s3BucketAttrMap

	default:
		return nil
	}
}

func NormalizeAndCopyAttributes(kind domain.ResourceKind, rawAttrs map[string]any, targetAttrs map[string]any) error {
	attrMap := getAttributeMapForKind(kind)
	if attrMap == nil {
		return errors.New(errors.CodeNotImplemented, fmt.Sprintf("no attribute mapping defined for kind: %s", kind))
	}

	if rawAttrs == nil {
		rawAttrs = make(map[string]any)
	}

	for tfKey, domainKey := range attrMap {
		rawValue, exists := rawAttrs[tfKey]
		if !exists || rawValue == nil {
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
		case domain.StorageBucketEncryptionKey:
			normalizedValue, err = normalizeS3Encryption(rawValue)
		case domain.StorageBucketLoggingKey, domain.StorageBucketWebsiteKey:
			normalizedValue, err = normalizeSingleBlockMap(rawValue)
		case domain.StorageBucketLifecycleRulesKey, domain.StorageBucketCorsRulesKey:
			normalizedValue, err = normalizeGenericSliceOfMaps(rawValue)
		case domain.StorageBucketACLKey:
			if strValue, isString := rawValue.(string); isString {
				normalizedValue = strValue
				err = nil
			} else {
				normalizedValue, err = normalizeGenericSliceOfMaps(rawValue)
			}
		case domain.ComputeSecurityGroupsKey:
			normalizedValue, err = normalizeStringSlice(rawValue)
		default:
			normalizedValue = rawValue
			err = nil
		}

		if err != nil {
			return errors.Wrap(err, errors.CodeMappingError, fmt.Sprintf("failed to normalize attribute '%s' (tf key: '%s') for kind '%s'", domainKey, tfKey, kind))
		}

		if normalizedValue != nil {
			targetAttrs[domainKey] = normalizedValue
		}
	}

	if _, exists := targetAttrs[domain.KeyName]; !exists {
		if tags, ok := targetAttrs[domain.KeyTags].(map[string]string); ok {
			if nameVal, nameOk := tags["Name"]; nameOk {
				targetAttrs[domain.KeyName] = nameVal
			}
		}
	}

	if kind == domain.KindStorageBucket {
		if idVal, ok := targetAttrs[domain.KeyID]; ok {
			if _, nameExists := targetAttrs[domain.KeyName]; !nameExists {
				targetAttrs[domain.KeyName] = idVal
			}
		}
	}

	return nil
}

func normalizeTags(rawVal any) (map[string]string, error) {
	tagsMap, ok := rawVal.(map[string]any)
	if !ok {
		_, isStringMap := rawVal.(map[string]string)
		if isStringMap {
			return rawVal.(map[string]string), nil
		}
		return nil, fmt.Errorf("invalid type for tags, expected map[string]any or map[string]string, got %T", rawVal)
	}
	stringTags := make(map[string]string, len(tagsMap))
	for k, v := range tagsMap {
		if v == nil {
			stringTags[k] = ""
			continue
		}
		switch typedV := v.(type) {
		case string:
			stringTags[k] = typedV
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			stringTags[k] = fmt.Sprintf("%d", typedV)
		case float32, float64:
			stringTags[k] = fmt.Sprintf("%f", typedV)
		case bool:
			stringTags[k] = strconv.FormatBool(typedV)
		default:
			return nil, fmt.Errorf("unsupported tag value type for key '%s': %T", k, v)
		}
	}
	return stringTags, nil
}

func normalizeVersioning(rawVal any) (bool, error) {
	list, ok := rawVal.([]any)
	if !ok || len(list) == 0 {
		return false, nil
	}
	blockMap, ok := list[0].(map[string]any)
	if !ok {
		return false, fmt.Errorf("versioning block item is not a map, got %T", list[0])
	}
	enabledVal, exists := blockMap["enabled"]
	if !exists {
		return false, nil
	}
	enabledBool, ok := enabledVal.(bool)
	if !ok {
		return false, fmt.Errorf("versioning 'enabled' attribute is not a boolean, got %T", enabledVal)
	}
	return enabledBool, nil
}

func normalizeSingleBlockDevice(rawVal any, isRoot bool) (map[string]any, error) {
	list, ok := rawVal.([]any)
	if !ok || len(list) == 0 {
		return nil, nil
	}
	blockMap, ok := list[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("block device item is not a map, got %T", list[0])
	}
	return normalizeBlockDeviceMap(blockMap, isRoot)
}

func normalizeBlockDeviceSlice(rawVal any) ([]any, error) {
	list, ok := rawVal.([]any)
	if !ok || len(list) == 0 {
		return nil, nil
	}
	normalizedBlocks := make([]any, 0, len(list))
	for i, item := range list {
		blockMap, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("block device item at index %d is not a map, got %T", i, item)
		}
		normMap, err := normalizeBlockDeviceMap(blockMap, false)
		if err != nil {
			return nil, fmt.Errorf("failed to normalize block device item at index %d: %w", i, err)
		}
		if normMap != nil {
			normalizedBlocks = append(normalizedBlocks, normMap)
		}
	}
	if len(normalizedBlocks) == 0 {
		return nil, nil
	}
	return normalizedBlocks, nil
}

func normalizeBlockDeviceMap(rawMap map[string]any, isRoot bool) (map[string]any, error) {
	normMap := make(map[string]any)
	copyIfPresentMap(rawMap, normMap, "device_name")
	copyIfPresentMap(rawMap, normMap, "volume_type")
	copyIfPresentMap(rawMap, normMap, "kms_key_id")
	copyIfPresentMap(rawMap, normMap, "snapshot_id")

	if err := normalizeNumericField(rawMap, normMap, "volume_size"); err != nil {
		return nil, err
	}
	if err := normalizeNumericField(rawMap, normMap, "iops"); err != nil {
		return nil, err
	}
	if err := normalizeNumericField(rawMap, normMap, "throughput"); err != nil {
		return nil, err
	}
	if err := normalizeBoolField(rawMap, normMap, "encrypted"); err != nil {
		return nil, err
	}
	if err := normalizeBoolField(rawMap, normMap, "delete_on_termination"); err != nil {
		return nil, err
	}

	if _, exists := normMap["delete_on_termination"]; !exists {
		normMap["delete_on_termination"] = isRoot
	}
	return normMap, nil
}

func normalizeS3Encryption(rawVal any) (map[string]any, error) {
	list, ok := rawVal.([]any)
	if !ok || len(list) == 0 {
		return nil, nil
	}
	ruleMap, ok := list[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("encryption rule item is not a map, got %T", list[0])
	}

	resultMap := make(map[string]any)
	if defBlockListRaw, okDef := ruleMap["apply_server_side_encryption_by_default"]; okDef {
		defBlockList, okList := defBlockListRaw.([]any)
		if okList && len(defBlockList) > 0 {
			defMap, okMap := defBlockList[0].(map[string]any)
			if okMap {
				normDefMap := make(map[string]any)
				copyIfPresentMap(defMap, normDefMap, "sse_algorithm")
				copyIfPresentMap(defMap, normDefMap, "kms_master_key_id")
				if len(normDefMap) > 0 {
					resultMap["apply_server_side_encryption_by_default"] = normDefMap
				}
			}
		}
	}

	if err := normalizeBoolField(ruleMap, resultMap, "bucket_key_enabled"); err != nil {
		return nil, err
	}
	if len(resultMap) == 0 {
		return nil, nil
	}
	return resultMap, nil
}

func normalizeSingleBlockMap(rawVal any) (map[string]any, error) {
	list, ok := rawVal.([]any)
	if !ok || len(list) == 0 {
		return nil, nil
	}
	blockMap, ok := list[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("single block item is not a map, got %T", list[0])
	}
	return blockMap, nil
}

func normalizeGenericSliceOfMaps(rawVal any) ([]any, error) {
	list, ok := rawVal.([]any)
	if !ok || len(list) == 0 {
		return nil, nil
	}
	resultList := make([]any, 0, len(list))
	for i, item := range list {
		mapItem, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("item at index %d is not a map, got %T", i, item)
		}
		resultList = append(resultList, mapItem)
	}
	if len(resultList) == 0 {
		return nil, nil
	}
	return resultList, nil
}

func normalizeStringSlice(rawVal any) ([]string, error) {
	list, ok := rawVal.([]any)
	if !ok || len(list) == 0 {
		return nil, nil
	}
	resultSlice := make([]string, 0, len(list))
	for i, item := range list {
		strItem, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("item at index %d is not a string, got %T", i, item)
		}
		resultSlice = append(resultSlice, strItem)
	}
	return resultSlice, nil
}

func copyIfPresentMap(src, dest map[string]any, key string) {
	if val, ok := src[key]; ok && val != nil {
		dest[key] = val
	}
}

func normalizeNumericField(src, dest map[string]any, key string) error {
	val, ok := src[key]
	if !ok || val == nil {
		return nil
	}
	switch v := val.(type) {
	case float64:
		if float64(int64(v)) == v {
			dest[key] = int64(v)
		} else {
			dest[key] = v
		}
	case float32:
		if float32(int64(v)) == v {
			dest[key] = int64(v)
		} else {
			dest[key] = float64(v)
		}
	case int:
		dest[key] = int64(v)
	case int8:
		dest[key] = int64(v)
	case int16:
		dest[key] = int64(v)
	case int32:
		dest[key] = int64(v)
	case int64:
		dest[key] = v
	case uint:
		dest[key] = int64(v)
	case uint8:
		dest[key] = int64(v)
	case uint16:
		dest[key] = int64(v)
	case uint32:
		dest[key] = int64(v)
	case uint64:
		dest[key] = int64(v)
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			if float64(int64(f)) == f {
				dest[key] = int64(f)
			} else {
				dest[key] = f
			}
		} else {
			return fmt.Errorf("attribute '%s' has non-numeric string value '%s'", key, v)
		}
	default:
		return fmt.Errorf("attribute '%s' has unexpected numeric type %T", key, v)
	}
	return nil
}

func normalizeBoolField(src, dest map[string]any, key string) error {
	val, ok := src[key]
	if !ok || val == nil {
		return nil
	}
	switch v := val.(type) {
	case bool:
		dest[key] = v
	case string:
		if b, err := strconv.ParseBool(strings.ToLower(v)); err == nil {
			dest[key] = b
		} else {
			return fmt.Errorf("attribute '%s' has non-boolean string value '%s'", key, v)
		}
	default:
		return fmt.Errorf("attribute '%s' has unexpected boolean type %T", key, v)
	}
	return nil
}
