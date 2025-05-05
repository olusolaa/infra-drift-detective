// internal/adapters/platform/aws/provider.go
package aws

import (
	"context"
	stderrs "errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	aws_limiter "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/limiter"
	awstypes "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/shared"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/ec2"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/s3"
	"github.com/olusolaa/infra-drift-detector/internal/config"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"golang.org/x/sync/errgroup"
)

const (
	defaultRateLimitRPS          = 20
	defaultHTTPTimeout           = 30 * time.Second
	defaultMaxIdleConns          = 100
	defaultMaxIdleConnsPerHost   = 10
	defaultIdleConnTimeout       = 90 * time.Second
	defaultKeepAlive             = 30 * time.Second
	defaultTLSHandshakeTimeout   = 10 * time.Second
	defaultExpectContinueTimeout = 1 * time.Second
)

type Provider struct {
	awsConfig aws.Config
	handlers  map[domain.ResourceKind]AWSResourceHandler
	logger    ports.Logger
}

func NewProvider(ctx context.Context, appCfg *config.Config, logger ports.Logger) (*Provider, error) {
	if logger == nil {
		return nil, errors.New(errors.CodeConfigValidation, "logger cannot be nil for AWS Provider")
	}

	awsPlatformCfg := appCfg.Platform.AWS
	if awsPlatformCfg == nil {
		logger.Warnf(ctx, "Platform.aws configuration block missing, attempting AWS client setup with defaults.")
		awsPlatformCfg = &config.AWSPlatformConfig{}
	}

	effectiveRPS := awsPlatformCfg.APIRequestsPerSecond
	if effectiveRPS <= 0 {
		effectiveRPS = defaultRateLimitRPS
	}
	aws_limiter.Initialize(effectiveRPS, logger)

	httpClient := &http.Client{
		Timeout: defaultHTTPTimeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: defaultKeepAlive,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          defaultMaxIdleConns,
			MaxIdleConnsPerHost:   defaultMaxIdleConnsPerHost,
			IdleConnTimeout:       defaultIdleConnTimeout,
			TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
			ExpectContinueTimeout: defaultExpectContinueTimeout,
		},
	}

	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithHTTPClient(httpClient),
	}
	var specifiedRegion, specifiedProfile string

	if awsPlatformCfg.Region != "" {
		specifiedRegion = awsPlatformCfg.Region
		loadOpts = append(loadOpts, awsconfig.WithRegion(specifiedRegion))
		logger.Debugf(ctx, "AWS config: Using specified region", "region", specifiedRegion)
	}
	if awsPlatformCfg.Profile != "" {
		specifiedProfile = awsPlatformCfg.Profile
		loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(specifiedProfile))
		logger.Debugf(ctx, "AWS config: Using specified profile", "profile", specifiedProfile)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, errors.NewUserFacing(errors.CodePlatformAuthError, "Failed to load AWS configuration/credentials", "Ensure AWS credentials and region are configured correctly (environment variables, ~/.aws/credentials, ~/.aws/config, or IAM role).")
	}

	if awsCfg.Region == "" {
		return nil, errors.NewUserFacing(errors.CodeConfigValidation, "AWS region could not be determined", "Specify the AWS region via the 'region' key in platform.aws config, the AWS_REGION environment variable, or in your AWS profile.")
	}
	logger.Infof(ctx, "AWS provider configured successfully", "region", awsCfg.Region, "profile_source", specifiedProfile, "region_source", specifiedRegion)

	p := &Provider{
		awsConfig: awsCfg,
		handlers:  make(map[domain.ResourceKind]AWSResourceHandler),
		logger:    logger,
	}

	p.registerHandler(ec2.NewHandler(awsCfg))
	p.registerHandler(s3.NewHandler(awsCfg))

	if len(p.handlers) == 0 {
		return nil, errors.New(errors.CodeInternal, "no AWS resource handlers were registered")
	}

	logger.Infof(ctx, "AWS provider initialized", "handlers", p.getSupportedKinds())
	return p, nil
}

func (p *Provider) registerHandler(handler AWSResourceHandler) {
	if handler != nil {
		kind := handler.Kind()
		if _, exists := p.handlers[kind]; exists {
			p.logger.Warnf(context.Background(), "Overwriting existing handler for resource kind", "kind", kind)
		}
		p.handlers[kind] = handler
		p.logger.Debugf(context.Background(), "Registered AWS handler", "kind", kind)
	}
}

func (p *Provider) getSupportedKinds() []string {
	kinds := make([]string, 0, len(p.handlers))
	for k := range p.handlers {
		kinds = append(kinds, string(k))
	}
	return kinds
}

func (p *Provider) Type() string {
	return awstypes.ProviderTypeAWS
}

func (p *Provider) ListResources(
	ctx context.Context,
	requestedKinds []domain.ResourceKind,
	filters map[string]string,
	out chan<- domain.PlatformResource,
) error {
	// The channel is now closed by the engine, not here
	// No defer close(out) here anymore

	g, childCtx := errgroup.WithContext(ctx)
	foundHandler := false

	p.logger.Debugf(ctx, "Initiating AWS ListResources", "requested_kinds", requestedKinds)

	for _, kind := range requestedKinds {
		handler, found := p.handlers[kind]
		if !found {
			p.logger.Warnf(childCtx, "Resource kind not supported by AWS provider, skipping", "kind", kind)
			continue
		}
		foundHandler = true

		currentKind := kind
		currentHandler := handler
		currentFilters := filters

		g.Go(func() error {
			handlerLogger := p.logger.WithFields(map[string]any{"resource_kind": currentKind})
			handlerLogger.Debugf(childCtx, "Starting ListResources via handler")
			err := currentHandler.ListResources(childCtx, p.awsConfig, currentFilters, handlerLogger, out)
			if err != nil {
				handlerLogger.Errorf(childCtx, err, "Handler ListResources failed")
				if err == context.Canceled || err == context.DeadlineExceeded {
					return err
				}
				var appErr *errors.AppError
				isNotFound := stderrs.As(err, &appErr)
				if isNotFound && appErr != nil && appErr.Code == errors.CodeResourceNotFound {
					handlerLogger.Warnf(childCtx, "Handler reported resource not found during list (non-fatal for group)", "error", err)
					return nil
				}
				return errors.Wrap(err, errors.CodePlatformAPIError, fmt.Sprintf("handler for kind '%s' failed", currentKind))
			}
			handlerLogger.Debugf(childCtx, "Handler ListResources finished successfully")
			return nil
		})
	}

	if !foundHandler && len(requestedKinds) > 0 {
		return errors.New(errors.CodeNotImplemented, "no supported resource kinds found for AWS provider among requested kinds")
	}

	err := g.Wait()
	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			p.logger.Warnf(ctx, "AWS ListResources operation cancelled or timed out", "error", err)
		} else {
			p.logger.Errorf(ctx, err, "Error occurred during AWS ListResources execution")
		}
		return err
	}

	return nil
}

func (p *Provider) GetResource(ctx context.Context, kind domain.ResourceKind, id string) (domain.PlatformResource, error) {
	p.logger.Debugf(ctx, "Getting AWS resource", "kind", kind, "id", id)
	handler, found := p.handlers[kind]
	if !found {
		err := errors.New(errors.CodeNotImplemented, fmt.Sprintf("resource kind '%s' not supported by AWS provider", kind))
		p.logger.Errorf(ctx, err, "Unsupported kind requested")
		return nil, err
	}

	handlerLogger := p.logger.WithFields(map[string]any{"resource_kind": kind, "resource_id": id})
	resource, err := handler.GetResource(ctx, p.awsConfig, id, handlerLogger)
	if err != nil {
		handlerLogger.Errorf(ctx, err, "Handler GetResource failed")
		if err == context.Canceled || err == context.DeadlineExceeded {
			return nil, err
		}
		var appErr *errors.AppError
		if stderrs.As(err, &appErr) && appErr.Code == errors.CodeResourceNotFound {
			fmt.Printf("Resource not found: %s\n", id)
			return nil, err
		}
		fmt.Printf("Error getting resource: %s\n", err)
		return nil, errors.Wrap(err, errors.CodePlatformAPIError, fmt.Sprintf("failed to get resource '%s' of kind '%s'", id, kind))
	}

	handlerLogger.Debugf(ctx, "Handler GetResource finished successfully")
	return resource, nil
}

func NewProviderWithHandlers(cfg aws.Config, logger ports.Logger, handlers ...AWSResourceHandler) *Provider {
	p := &Provider{
		awsConfig: cfg,
		handlers:  make(map[domain.ResourceKind]AWSResourceHandler),
		logger:    logger,
	}
	for _, handler := range handlers {
		p.registerHandler(handler)
	}
	return p
}
