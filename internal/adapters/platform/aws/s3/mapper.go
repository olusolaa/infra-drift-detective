package s3

import (
	"context"
	"errors"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/shared"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"golang.org/x/sync/errgroup"

	aws_errors "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/errors"
	aws_limiter "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/limiter"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	idderrors "github.com/olusolaa/infra-drift-detector/internal/errors"
)

type s3BucketResource struct {
	mu              sync.RWMutex
	meta            domain.ResourceMetadata
	awsConfig       aws.Config
	parentLogger    ports.Logger
	builtAttrs      map[string]any
	fetchErr        error
	attributesBuilt bool
}

// NewDefaultS3ResourceBuilder creates a new default S3 resource builder.
func NewDefaultS3ResourceBuilder(s3Factory func(aws.Config) S3ClientInterface) S3ResourceBuilder {
	return &defaultS3ResourceBuilder{
		s3ClientFactory: s3Factory,
	}
}

// defaultS3ResourceBuilder implements the S3ResourceBuilder interface.
type defaultS3ResourceBuilder struct {
	s3ClientFactory func(aws.Config) S3ClientInterface
}

// Build calls the underlying buildS3BucketResource function and returns its error.
func (b *defaultS3ResourceBuilder) Build(ctx context.Context, bucketName, accountID string, cfg aws.Config, logger ports.Logger) (domain.PlatformResource, error) {
	resource := buildS3BucketResource(ctx, bucketName, accountID, cfg, logger, b.s3ClientFactory)
	return resource, resource.fetchErr // Return the resource and the captured build error
}

func (r *s3BucketResource) Metadata() domain.ResourceMetadata { return r.meta }

func (r *s3BucketResource) Attributes(ctx context.Context) (map[string]any, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.attributesBuilt {
		return nil, idderrors.New(idderrors.CodeInternal, fmt.Sprintf("S3 resource %s attributes accessed before build", r.meta.ProviderAssignedID))
	}
	return r.copyAttributeMap(r.builtAttrs), r.fetchErr
}

func (r *s3BucketResource) copyAttributeMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func buildS3BucketResource(
	ctx context.Context,
	bucketName, accountID string,
	cfg aws.Config,
	logger ports.Logger,
	s3Factory func(aws.Config) S3ClientInterface,
) *s3BucketResource {
	logger = logger.WithFields(map[string]any{"bucket_name": bucketName})
	resource := &s3BucketResource{
		awsConfig:    cfg,
		parentLogger: logger,
		meta: domain.ResourceMetadata{
			Kind:               domain.KindStorageBucket,
			ProviderType:       shared.ProviderTypeAWS,
			ProviderAssignedID: bucketName,
			SourceIdentifier:   bucketName,
			AccountID:          accountID,
			Region:             "unknown",
		},
	}
	data, err := fetchAllBucketAttributes(ctx, bucketName, cfg, logger, s3Factory)

	resource.mu.Lock()
	resource.fetchErr = err
	if err == nil && data != nil {
		resource.meta.Region = data.Region
		resource.builtAttrs = mapAPIDataToDomainAttrs(data, logger)
	} else if err == nil && data == nil {
		resource.fetchErr = idderrors.New(idderrors.CodeInternal, "fetchAllBucketAttributes returned nil data without error")
	}
	resource.attributesBuilt = true
	resource.mu.Unlock()
	return resource
}

type s3BucketAttributesInput struct {
	BucketName       string
	Region           string
	TaggingOutput    *s3.GetBucketTaggingOutput
	AclOutput        *s3.GetBucketAclOutput
	VersioningOutput *s3.GetBucketVersioningOutput
	LifecycleOutput  *s3.GetBucketLifecycleConfigurationOutput
	LoggingOutput    *s3.GetBucketLoggingOutput
	WebsiteOutput    *s3.GetBucketWebsiteOutput
	CorsOutput       *s3.GetBucketCorsOutput
	PolicyOutput     *s3.GetBucketPolicyOutput
	EncryptionOutput *s3.GetBucketEncryptionOutput
}

func fetchAllBucketAttributes(
	ctx context.Context,
	bucketName string,
	cfg aws.Config,
	logger ports.Logger,
	s3Factory func(aws.Config) S3ClientInterface,
) (*s3BucketAttributesInput, error) {
	input := &s3BucketAttributesInput{BucketName: bucketName}

	baseClient := s3Factory(cfg)

	if err := aws_limiter.Wait(ctx, logger); err != nil {
		return nil, idderrors.Wrap(err, idderrors.CodePlatformAPIError, "rate limit error before GetBucketLocation")
	}
	loc, err := baseClient.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: &bucketName})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "AccessDenied" {
			if headErr := aws_limiter.Wait(ctx, logger); headErr != nil {
				return nil, headErr
			}
			head, headErr := baseClient.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &bucketName})
			if headErr == nil && head != nil && head.BucketRegion != nil {
				input.Region = *head.BucketRegion
			} else {
				return nil, aws_errors.HandleAWSError("S3 bucket", bucketName, err, ctx)
			}
		} else if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchBucket" {
			// If the bucket doesn't exist, return a clear error
			return nil, aws_errors.HandleAWSError("S3 bucket", bucketName, err, ctx)
		} else {
			return nil, aws_errors.HandleAWSError("S3 bucket", bucketName, err, ctx)
		}
	} else {
		if loc.LocationConstraint == "" {
			input.Region = "us-east-1"
		} else {
			input.Region = string(loc.LocationConstraint)
		}
	}

	regionalCfg := cfg.Copy()
	regionalCfg.Region = input.Region
	client := s3Factory(regionalCfg)

	g, childCtx := errgroup.WithContext(ctx)
	var mu sync.Mutex

	run := func(name string, call func(context.Context) error) {
		g.Go(func() error {
			if err := aws_limiter.Wait(childCtx, logger); err != nil {
				return err
			}
			if err := call(childCtx); err != nil {
				return err
			}
			return nil
		})
	}

	run("GetBucketTagging", func(c context.Context) error {
		out, err := client.GetBucketTagging(c, &s3.GetBucketTaggingInput{Bucket: &bucketName})
		if err == nil {
			mu.Lock()
			input.TaggingOutput = out
			mu.Unlock()
			return nil
		}
		if isS3NotFoundError(err, "NoSuchTagSet") {
			return nil
		}
		return aws_errors.HandleAWSError("S3 bucket", bucketName, err, c)
	})

	run("GetBucketVersioning", func(c context.Context) error {
		out, err := client.GetBucketVersioning(c, &s3.GetBucketVersioningInput{Bucket: &bucketName})
		if err == nil {
			mu.Lock()
			input.VersioningOutput = out
			mu.Unlock()
			return nil
		}
		return aws_errors.HandleAWSError("S3 bucket", bucketName, err, c)
	})

	run("GetBucketLifecycleConfiguration", func(c context.Context) error {
		out, err := client.GetBucketLifecycleConfiguration(c, &s3.GetBucketLifecycleConfigurationInput{Bucket: &bucketName})
		if err == nil {
			mu.Lock()
			input.LifecycleOutput = out
			mu.Unlock()
			return nil
		}
		if isS3NotFoundError(err, "NoSuchLifecycleConfiguration") {
			return nil
		}
		return aws_errors.HandleAWSError("S3 bucket", bucketName, err, c)
	})

	run("GetBucketAcl", func(c context.Context) error {
		out, err := client.GetBucketAcl(c, &s3.GetBucketAclInput{Bucket: &bucketName})
		if err == nil {
			mu.Lock()
			input.AclOutput = out
			mu.Unlock()
			return nil
		}
		return aws_errors.HandleAWSError("S3 bucket", bucketName, err, c)
	})

	run("GetBucketLogging", func(c context.Context) error {
		out, err := client.GetBucketLogging(c, &s3.GetBucketLoggingInput{Bucket: &bucketName})
		if err == nil {
			mu.Lock()
			input.LoggingOutput = out
			mu.Unlock()
			return nil
		}
		return aws_errors.HandleAWSError("S3 bucket", bucketName, err, c)
	})

	run("GetBucketWebsite", func(c context.Context) error {
		out, err := client.GetBucketWebsite(c, &s3.GetBucketWebsiteInput{Bucket: &bucketName})
		if err == nil {
			mu.Lock()
			input.WebsiteOutput = out
			mu.Unlock()
			return nil
		}
		if isS3NotFoundError(err, "NoSuchWebsiteConfiguration") {
			return nil
		}
		return aws_errors.HandleAWSError("S3 bucket", bucketName, err, c)
	})

	run("GetBucketCors", func(c context.Context) error {
		out, err := client.GetBucketCors(c, &s3.GetBucketCorsInput{Bucket: &bucketName})
		if err == nil {
			mu.Lock()
			input.CorsOutput = out
			mu.Unlock()
			return nil
		}
		if isS3NotFoundError(err, "NoSuchCORSConfiguration") {
			return nil
		}
		return aws_errors.HandleAWSError("S3 bucket", bucketName, err, c)
	})

	run("GetBucketPolicy", func(c context.Context) error {
		out, err := client.GetBucketPolicy(c, &s3.GetBucketPolicyInput{Bucket: &bucketName})
		if err == nil {
			mu.Lock()
			input.PolicyOutput = out
			mu.Unlock()
			return nil
		}
		if isS3NotFoundError(err, "NoSuchBucketPolicy") {
			return nil
		}
		return aws_errors.HandleAWSError("S3 bucket", bucketName, err, c)
	})

	run("GetBucketEncryption", func(c context.Context) error {
		out, err := client.GetBucketEncryption(c, &s3.GetBucketEncryptionInput{Bucket: &bucketName})
		if err == nil {
			mu.Lock()
			input.EncryptionOutput = out
			mu.Unlock()
			return nil
		}
		if isS3NotFoundError(err, "ServerSideEncryptionConfigurationNotFoundError") {
			return nil
		}
		return aws_errors.HandleAWSError("S3 bucket", bucketName, err, c)
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return input, nil
}

func mapAPIDataToDomainAttrs(in *s3BucketAttributesInput, logger ports.Logger) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	attrs := map[string]any{
		domain.KeyID:     in.BucketName,
		domain.KeyName:   in.BucketName,
		domain.KeyRegion: in.Region,
		domain.KeyARN:    fmt.Sprintf("arn:aws:s3:::%s", in.BucketName),
	}

	tags := map[string]string{}
	if in.TaggingOutput != nil {
		for _, t := range in.TaggingOutput.TagSet {
			tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
	}
	// Only add tags if there are any
	if len(tags) > 0 {
		attrs[domain.KeyTags] = tags
		if name, ok := tags["Name"]; ok {
			attrs[domain.KeyName] = name
		}
	}

	if in.AclOutput != nil {
		grants := make([]map[string]any, 0, len(in.AclOutput.Grants))
		for _, g := range in.AclOutput.Grants {
			m := map[string]any{"permission": string(g.Permission)}
			if g.Grantee != nil {
				m["type"] = string(g.Grantee.Type)
				if id := aws.ToString(g.Grantee.ID); id != "" && g.Grantee.Type != s3types.TypeGroup {
					m["id"] = id
				}
				if uri := aws.ToString(g.Grantee.URI); uri != "" {
					m["uri"] = uri
				}
			}
			grants = append(grants, m)
		}
		if len(grants) > 0 {
			attrs[domain.StorageBucketACLKey] = grants
		}
	}

	if in.VersioningOutput != nil && in.VersioningOutput.Status != "" {
		attrs[domain.StorageBucketVersioningKey] = in.VersioningOutput.Status == s3types.BucketVersioningStatusEnabled
	}

	if in.LifecycleOutput != nil && len(in.LifecycleOutput.Rules) > 0 {
		if rules := mapLifecycleRules(in.LifecycleOutput.Rules); rules != nil {
			attrs[domain.StorageBucketLifecycleRulesKey] = rules
		}
	}

	if in.LoggingOutput != nil && in.LoggingOutput.LoggingEnabled != nil {
		if m := mapLogging(in.LoggingOutput.LoggingEnabled); m != nil {
			attrs[domain.StorageBucketLoggingKey] = m
		}
	}

	if in.WebsiteOutput != nil {
		if m := mapWebsite(in.WebsiteOutput); m != nil {
			attrs[domain.StorageBucketWebsiteKey] = m
		}
	}

	if in.CorsOutput != nil && len(in.CorsOutput.CORSRules) > 0 {
		if rules := mapCorsRules(in.CorsOutput.CORSRules); rules != nil {
			attrs[domain.StorageBucketCorsRulesKey] = rules
		}
	}

	if in.PolicyOutput != nil && in.PolicyOutput.Policy != nil {
		attrs[domain.StorageBucketPolicyKey] = aws.ToString(in.PolicyOutput.Policy)
	}

	if in.EncryptionOutput != nil && in.EncryptionOutput.ServerSideEncryptionConfiguration != nil {
		if m := mapEncryption(in.EncryptionOutput.ServerSideEncryptionConfiguration); m != nil {
			attrs[domain.StorageBucketEncryptionKey] = m
		}
	}

	return attrs
}

func mapLifecycleRules(rules []s3types.LifecycleRule) []map[string]any {
	result := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		ruleMap := map[string]any{}
		if id := aws.ToString(rule.ID); id != "" {
			ruleMap["id"] = id
		}
		ruleMap["status"] = string(rule.Status)

		if filter := mapLifecycleRuleFilter(rule.Filter); filter != nil {
			ruleMap["filter"] = filter
		}
		if exp := mapLifecycleExpiration(rule.Expiration); exp != nil {
			ruleMap["expiration"] = exp
		}
		if trans := mapLifecycleTransitions(rule.Transitions); trans != nil {
			ruleMap["transition"] = trans
		}
		if nctrans := mapNoncurrentVersionTransitions(rule.NoncurrentVersionTransitions); nctrans != nil {
			ruleMap["noncurrent_version_transition"] = nctrans
		}
		if ncexp := mapNoncurrentVersionExpiration(rule.NoncurrentVersionExpiration); ncexp != nil {
			ruleMap["noncurrent_version_expiration"] = ncexp
		}
		if abort := mapAbortIncompleteMultipartUpload(rule.AbortIncompleteMultipartUpload); abort != nil {
			ruleMap["abort_incomplete_multipart_upload"] = abort
		}

		if len(ruleMap) > 1 || (len(ruleMap) == 1 && ruleMap["status"] != nil) {
			result = append(result, ruleMap)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func mapLifecycleRuleFilter(filter *s3types.LifecycleRuleFilter) map[string]any {
	if filter == nil {
		return nil
	}

	filterMap := make(map[string]any)

	if filter.And != nil {
		andMap := map[string]any{}
		andVal := filter.And
		if andVal.ObjectSizeGreaterThan != nil {
			andMap["object_size_greater_than"] = *andVal.ObjectSizeGreaterThan
		}
		if andVal.ObjectSizeLessThan != nil {
			andMap["object_size_less_than"] = *andVal.ObjectSizeLessThan
		}
		if pfx := aws.ToString(andVal.Prefix); pfx != "" {
			andMap["prefix"] = pfx
		}
		tags := make(map[string]string)
		for _, t := range andVal.Tags {
			tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
		if len(tags) > 0 {
			andMap["tags"] = tags
		}
		if len(andMap) > 0 {
			filterMap["and"] = andMap
		}
	} else if filter.Prefix != nil {
		filterMap["prefix"] = aws.ToString(filter.Prefix)
	} else if filter.Tag != nil {
		filterMap["tag"] = map[string]string{
			aws.ToString(filter.Tag.Key): aws.ToString(filter.Tag.Value),
		}
	}

	if len(filterMap) == 0 {
		return nil
	}

	return filterMap
}

func mapLifecycleExpiration(exp *s3types.LifecycleExpiration) map[string]any {
	if exp == nil {
		return nil
	}
	expMap := map[string]any{}
	if days := aws.ToInt32(exp.Days); days != 0 {
		expMap["days"] = days
	} else if exp.Date != nil {
		expMap["date"] = exp.Date.UTC().Format(time.RFC3339)
	}
	if aws.ToBool(exp.ExpiredObjectDeleteMarker) {
		expMap["expired_object_delete_marker"] = true
	}
	if len(expMap) == 0 {
		return nil
	}
	return expMap
}

func mapLifecycleTransitions(trans []s3types.Transition) []map[string]any {
	result := make([]map[string]any, 0, len(trans))
	for _, t := range trans {
		tMap := map[string]any{}
		if days := aws.ToInt32(t.Days); days != 0 {
			tMap["days"] = days
		} else if t.Date != nil {
			tMap["date"] = t.Date.UTC().Format(time.RFC3339)
		}
		tMap["storage_class"] = string(t.StorageClass)
		if _, hasDays := tMap["days"]; hasDays {
			result = append(result, tMap)
		} else if _, hasDate := tMap["date"]; hasDate {
			result = append(result, tMap)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func mapNoncurrentVersionTransitions(trans []s3types.NoncurrentVersionTransition) []map[string]any {
	result := make([]map[string]any, 0, len(trans))
	for _, t := range trans {
		tMap := map[string]any{}
		if days := aws.ToInt32(t.NoncurrentDays); days != 0 {
			tMap["noncurrent_days"] = days
		}
		tMap["storage_class"] = string(t.StorageClass)
		if _, hasDays := tMap["noncurrent_days"]; hasDays {
			result = append(result, tMap)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func mapNoncurrentVersionExpiration(exp *s3types.NoncurrentVersionExpiration) map[string]any {
	if exp == nil {
		return nil
	}
	expMap := map[string]any{}
	if days := aws.ToInt32(exp.NoncurrentDays); days != 0 {
		expMap["noncurrent_days"] = days
	}
	if len(expMap) == 0 {
		return nil
	}
	return expMap
}

func mapAbortIncompleteMultipartUpload(abort *s3types.AbortIncompleteMultipartUpload) map[string]any {
	if abort == nil {
		return nil
	}
	abortMap := map[string]any{}
	if days := aws.ToInt32(abort.DaysAfterInitiation); days != 0 {
		abortMap["days_after_initiation"] = days
	}
	if len(abortMap) == 0 {
		return nil
	}
	return abortMap
}

func mapLogging(log *s3types.LoggingEnabled) map[string]any {
	if log == nil {
		return nil
	}
	logMap := map[string]any{}
	if targetBucket := aws.ToString(log.TargetBucket); targetBucket != "" {
		logMap["target_bucket"] = targetBucket
	} else {
		return nil
	}
	if targetPrefix := aws.ToString(log.TargetPrefix); targetPrefix != "" {
		logMap["target_prefix"] = targetPrefix
	} else {
		logMap["target_prefix"] = ""
	}
	return logMap
}

func mapWebsite(web *s3.GetBucketWebsiteOutput) map[string]any {
	if web == nil {
		return nil
	}
	webMap := map[string]any{}
	if web.IndexDocument != nil && web.IndexDocument.Suffix != nil {
		webMap["index_document"] = aws.ToString(web.IndexDocument.Suffix)
	}
	if web.ErrorDocument != nil && web.ErrorDocument.Key != nil {
		webMap["error_document"] = aws.ToString(web.ErrorDocument.Key)
	}
	if web.RedirectAllRequestsTo != nil {
		redirectMap := map[string]any{}
		if host := aws.ToString(web.RedirectAllRequestsTo.HostName); host != "" {
			redirectMap["host_name"] = host
		}
		if proto := web.RedirectAllRequestsTo.Protocol; proto != "" {
			redirectMap["protocol"] = strings.ToUpper(string(proto))
		}
		if len(redirectMap) > 0 {
			webMap["redirect_all_requests_to"] = redirectMap
		}
	}
	if len(web.RoutingRules) > 0 {
		rules := make([]map[string]any, 0, len(web.RoutingRules))
		for _, r := range web.RoutingRules {
			ruleMap := map[string]any{}
			if r.Condition != nil {
				condMap := map[string]any{}
				if errCode := aws.ToString(r.Condition.HttpErrorCodeReturnedEquals); errCode != "" {
					condMap["http_error_code_returned_equals"] = errCode
				}
				if keyPrefix := aws.ToString(r.Condition.KeyPrefixEquals); keyPrefix != "" {
					condMap["key_prefix_equals"] = keyPrefix
				}
				if len(condMap) > 0 {
					ruleMap["condition"] = condMap
				}
			}
			if r.Redirect != nil {
				redirMap := map[string]any{}
				if host := aws.ToString(r.Redirect.HostName); host != "" {
					redirMap["host_name"] = host
				}
				if code := aws.ToString(r.Redirect.HttpRedirectCode); code != "" {
					redirMap["http_redirect_code"] = code
				}
				if proto := r.Redirect.Protocol; proto != "" {
					redirMap["protocol"] = strings.ToUpper(string(proto))
				}
				if replacePre := aws.ToString(r.Redirect.ReplaceKeyPrefixWith); replacePre != "" {
					redirMap["replace_key_prefix_with"] = replacePre
				}
				if replaceKey := aws.ToString(r.Redirect.ReplaceKeyWith); replaceKey != "" {
					redirMap["replace_key_with"] = replaceKey
				}
				if len(redirMap) > 0 {
					ruleMap["redirect"] = redirMap
				}
			}
			if len(ruleMap) > 0 {
				rules = append(rules, ruleMap)
			}
		}
		if len(rules) > 0 {
			webMap["routing_rules"] = rules
		}
	}
	if len(webMap) == 0 {
		return nil
	}
	return webMap
}

func mapCorsRules(rules []s3types.CORSRule) []map[string]any {
	result := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		ruleMap := map[string]any{}
		if len(rule.AllowedMethods) > 0 {
			ruleMap["allowed_methods"] = rule.AllowedMethods
		} else {
			continue
		}
		if len(rule.AllowedOrigins) > 0 {
			ruleMap["allowed_origins"] = rule.AllowedOrigins
		} else {
			continue
		}
		if id := aws.ToString(rule.ID); id != "" {
			ruleMap["id"] = id
		}
		if len(rule.AllowedHeaders) > 0 {
			ruleMap["allowed_headers"] = rule.AllowedHeaders
		}
		if len(rule.ExposeHeaders) > 0 {
			ruleMap["expose_headers"] = rule.ExposeHeaders
		}
		if maxAge := aws.ToInt32(rule.MaxAgeSeconds); maxAge != 0 {
			ruleMap["max_age_seconds"] = maxAge
		}
		result = append(result, ruleMap)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func mapEncryption(enc *s3types.ServerSideEncryptionConfiguration) map[string]any {
	if enc == nil || len(enc.Rules) == 0 {
		return nil
	}
	firstRule := enc.Rules[0]
	if firstRule.ApplyServerSideEncryptionByDefault == nil {
		return nil
	}

	encMap := map[string]any{}
	ruleMap := map[string]any{}

	defaultEnc := firstRule.ApplyServerSideEncryptionByDefault
	applyDefaultMap := map[string]any{
		"sse_algorithm": string(defaultEnc.SSEAlgorithm),
	}
	if kmsKey := aws.ToString(defaultEnc.KMSMasterKeyID); kmsKey != "" {
		applyDefaultMap["kms_master_key_id"] = kmsKey
	}
	ruleMap["apply_server_side_encryption_by_default"] = applyDefaultMap

	if firstRule.BucketKeyEnabled != nil {
		ruleMap["bucket_key_enabled"] = *firstRule.BucketKeyEnabled
	} else {
		ruleMap["bucket_key_enabled"] = false
	}

	encMap["rule"] = []any{ruleMap}

	return encMap
}

func isS3NotFoundError(err error, errorCodes ...string) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorCode() == "NoSuchBucket" {
			return true
		}
		for _, code := range errorCodes {
			if apiErr.ErrorCode() == code {
				return true
			}
		}
	}

	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) && respErr.Response != nil {
		if respErr.Response.StatusCode == http.StatusNotFound {
			return true
		}
	}
	return false
}
