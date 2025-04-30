package aws

import (
	"context"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/util"
	"net"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/ec2"
	"github.com/olusolaa/infra-drift-detector/internal/config"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"golang.org/x/sync/errgroup"
)

const (
	defaultRateLimitRPS        = 20
	defaultHTTPTimeout         = 30 * time.Second
	defaultMaxIdleConns        = 100
	defaultMaxIdleConnsPerHost = 100
	defaultIdleConnTimeout     = 90 * time.Second
	defaultKeepAlive           = 30 * time.Second
)

type Provider struct {
	awsConfig aws.Config
	handlers  map[domain.ResourceKind]AWSResourceHandler
	logger    ports.Logger
}

func (p *Provider) Type() string {
	return util.ProviderTypeAWS
}

func NewProvider(ctx context.Context, appCfg *config.Config, logger ports.Logger) (*Provider, error) {
	if logger == nil {
		return nil, errors.New(errors.CodeConfigValidation, "logger cannot be nil")
	}

	awsPlatformCfg := appCfg.Platform.AWS
	if awsPlatformCfg == nil {
		awsPlatformCfg = &config.AWSPlatformConfig{}
	}
	if awsPlatformCfg.APIRequestsPerSecond == 0 {
		awsPlatformCfg.APIRequestsPerSecond = defaultRateLimitRPS
	}

	loadOpts := []func(*awsconfig.LoadOptions) error{}

	if awsPlatformCfg.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(awsPlatformCfg.Region))
		logger.Debugf(ctx, "Using specified AWS region", "region", awsPlatformCfg.Region)
	}

	if awsPlatformCfg.Profile != "" {
		loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(awsPlatformCfg.Profile))
		logger.Debugf(ctx, "Using specified AWS profile", "profile", awsPlatformCfg.Profile)
	}

	util.InitializeLimiter(awsPlatformCfg.APIRequestsPerSecond, logger) // Initialize global limiter

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
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
	loadOpts = append(loadOpts, awsconfig.WithHTTPClient(httpClient))

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeConfigValidation, "failed to load default AWS config")
	}

	if awsCfg.Region == "" {
		return nil, errors.NewUserFacing(errors.CodeConfigValidation, "AWS region not configured", "Set AWS_REGION env var or configure in AWS profile.")
	}
	logger.Infof(ctx, "AWS Client configured for region: %s", awsCfg.Region)

	p := &Provider{
		awsConfig: awsCfg,
		handlers:  make(map[domain.ResourceKind]AWSResourceHandler),
		logger:    logger,
	}

	ec2Handler := ec2.NewHandler(awsCfg)
	p.registerHandler(ec2Handler)

	if len(p.handlers) == 0 {
		return nil, errors.New(errors.CodeInternal, "no AWS resource handlers registered")
	}

	return p, nil
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

// registerHandler remains the same
func (p *Provider) registerHandler(handler AWSResourceHandler) {
	if handler != nil {
		p.handlers[handler.Kind()] = handler
	}
}

func (p *Provider) ListResources(
	ctx context.Context,
	requestedKinds []domain.ResourceKind,
	filters map[string]string,
	out chan<- domain.PlatformResource,
) error {
	g, childCtx := errgroup.WithContext(ctx)

	foundHandler := false
	for _, kind := range requestedKinds {
		handler, found := p.handlers[kind]
		if !found {
			p.logger.Warnf(childCtx, "Resource kind '%s' not supported by AWS provider, skipping", kind)
			continue
		}
		foundHandler = true
		currentKind := kind
		currentHandler := handler

		g.Go(func() error {
			handlerLogger := p.logger.WithFields(map[string]any{"resource_kind": currentKind})
			err := currentHandler.ListResources(childCtx, p.awsConfig, filters, handlerLogger, out)
			if err != nil {
				handlerLogger.Errorf(childCtx, err, "Handler failed")
				return err
			}
			handlerLogger.Debugf(childCtx, "Finished ListResources for handler")
			return nil
		})
	}

	if !foundHandler && len(requestedKinds) > 0 {
		return errors.New(errors.CodeNotImplemented, "no supported resource kinds found for AWS provider among requested kinds")
	}

	if err := g.Wait(); err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			p.logger.Warnf(ctx, "AWS ListResources cancelled or timed out: %v", err)
		} else {
			p.logger.Errorf(ctx, err, "Error occurred during AWS ListResources execution")
		}
		return err
	}

	p.logger.Debugf(ctx, "All AWS resource handlers finished successfully.")
	return nil
}

func (p *Provider) GetResource(ctx context.Context, kind domain.ResourceKind, id string) (domain.PlatformResource, error) {
	handler, found := p.handlers[kind]
	if !found {
		return nil, errors.New(errors.CodeNotImplemented, fmt.Sprintf("resource kind '%s' not supported by AWS provider", kind))
	}
	handlerLogger := p.logger.WithFields(map[string]any{"resource_kind": kind, "resource_id": id})
	return handler.GetResource(ctx, p.awsConfig, id, handlerLogger)
}
