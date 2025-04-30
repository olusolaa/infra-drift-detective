package app

import (
	"context"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/util"
	"os"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/spf13/viper"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/matching/tag"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfstate"
	"github.com/olusolaa/infra-drift-detector/internal/config"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/core/service"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"github.com/olusolaa/infra-drift-detector/internal/log"
	"github.com/olusolaa/infra-drift-detector/internal/reporting/text"
	"github.com/olusolaa/infra-drift-detector/internal/resources/compute"
)

type Application struct {
	Engine ports.DriftAnalysisEngine
	Logger ports.Logger
	Config *config.Config
}

// this is doing a lot seperate this into smaller functions like
// - initialize config
// - initialize logger
// - initialize state provider
// - initialize platform provider
// - initialize matcher
// - initialize reporter
// - initialize engine
// alternatively you can use veradic function arguments
func BuildApplicationFromViper(ctx context.Context, v *viper.Viper) (*Application, error) {
	//accept logger as argument
	cfg := config.DefaultConfig()
	err := v.Unmarshal(cfg)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeConfigParseError, "failed to unmarshal configuration")
	}

	logCfg := log.Config{Level: cfg.Settings.LogLevel, Format: cfg.Settings.LogFormat}
	logger, err := log.NewLogger(logCfg)
	if err != nil {
		// user logger instead of FPrintf
		fmt.Fprintf(os.Stderr, "FATAL: Failed to initialize logger: %v\n", err)
		return nil, errors.Wrap(err, errors.CodeInternal, "logger initialization failed")
	}
	logger.Infof(ctx, "Logger initialized (Level: %s, Format: %s)", cfg.Settings.LogLevel, cfg.Settings.LogFormat)
	if v.ConfigFileUsed() != "" {
		logger.Debugf(ctx, "Using configuration file: %s", v.ConfigFileUsed())
	} else {
		logger.Debugf(ctx, "No configuration file found, using defaults/env/flags.")
	}

	attributesOverrideStr := v.GetString("attributes")
	if attributesOverrideStr != "" {
		logger.Debugf(ctx, "Applying attribute overrides from command line: %s", attributesOverrideStr)
		overrideMap := parseAttributesOverride(attributesOverrideStr)
		if overrideMap != nil {
			existingResources := make(map[domain.ResourceKind]int)
			for i, r := range cfg.Resources {
				existingResources[r.Kind] = i
			}
			for kind, attrs := range overrideMap {
				if index, exists := existingResources[kind]; exists {
					logger.Debugf(ctx, "Overriding attributes for kind '%s' with: %v", kind, attrs)
					// config should provide a method to override attributes
					// config should be read only
					cfg.Resources[index].Attributes = attrs
				} else {
					logger.Warnf(ctx, "Ignoring attribute override for undefined kind '%s'", kind)
				}
			}
		}
	}

	validate := validator.New(validator.WithRequiredStructEnabled())
	err = validate.StructCtx(ctx, cfg)
	if err != nil {
		var errorDetails strings.Builder
		errorDetails.WriteString("Configuration validation failed:")
		validationErrors := err.(validator.ValidationErrors)
		for _, fe := range validationErrors {
			errorDetails.WriteString(fmt.Sprintf("\n - Field '%s': Failed on '%s' validation (value: '%v')", fe.Namespace(), fe.Tag(), fe.Value()))
		}
		wrappedErr := errors.NewUserFacing(errors.CodeConfigValidation, errorDetails.String(), "Please check your configuration file or flags.")
		logger.Errorf(ctx, wrappedErr, "Configuration validation failed")
		return nil, wrappedErr
	}
	logger.Debugf(ctx, "Configuration validated successfully")

	registry := service.NewComponentRegistry()
	logger.Debugf(ctx, "Component registry initialized")

	var stateProvider ports.StateProvider
	switch cfg.State.ProviderType {
	case tfstate.ProviderTypeTFState:
		provLog := logger.WithFields(map[string]any{"provider": tfstate.ProviderTypeTFState})
		stateProvider, err = tfstate.NewProvider(*cfg.State.TFState)
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeConfigValidation, "failed to initialize TFState provider")
		}
		provLog.Infof(ctx, "Using TFState provider: %s", cfg.State.TFState.FilePath)
	case tfhcl.ProviderTypeTFHCL:
		provLog := logger.WithFields(map[string]any{"provider": tfhcl.ProviderTypeTFHCL})
		stateProvider, err = tfhcl.NewProvider(*cfg.State.TFHCL, provLog)
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeConfigValidation, "failed to initialize TFHCL provider")
		}
		provLog.Infof(ctx, "Using TFHCL provider: %s (Workspace: %s)", cfg.State.TFHCL.Directory, cfg.State.TFHCL.Workspace)
	default:
		return nil, errors.NewUserFacing(errors.CodeConfigValidation, fmt.Sprintf("invalid state provider type: %s", cfg.State.ProviderType), "")
	}
	if err = registry.RegisterStateProvider(stateProvider); err != nil {
		return nil, err
	}

	var platformProvider ports.PlatformProvider
	if cfg.Platform.AWS != nil {
		provLog := logger.WithFields(map[string]any{"provider": util.ProviderTypeAWS})
		platformProvider, err = aws.NewProvider(ctx, cfg, provLog)
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeConfigValidation, "failed to initialize AWS provider")
		}
		if err = registry.RegisterPlatformProvider(platformProvider); err != nil {
			return nil, err
		}
		provLog.Infof(ctx, "Using AWS platform provider")
	} else {
		return nil, errors.NewUserFacing(errors.CodeConfigValidation, "no supported platform provider configured", "Configure platform.aws section.")
	}

	var matcher ports.Matcher
	switch cfg.Settings.MatcherType {
	case tag.MatcherTypeTag:
		matchLog := logger.WithFields(map[string]any{"component": "matcher", "type": tag.MatcherTypeTag})
		matcher, err = tag.NewMatcher(*cfg.Settings.Matcher.Tag, matchLog)
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeConfigValidation, "failed to initialize Tag matcher")
		}
		matchLog.Infof(ctx, "Using Tag matcher with key: %s", cfg.Settings.Matcher.Tag.TagKey)
	default:
		return nil, errors.NewUserFacing(errors.CodeConfigValidation, fmt.Sprintf("unsupported matcher type: %s", cfg.Settings.MatcherType), "Supported: tag")
	}

	var reporter ports.Reporter
	switch cfg.Settings.ReporterType {
	case text.ReporterTypeText:
		reportLog := logger.WithFields(map[string]any{"component": "reporter", "type": text.ReporterTypeText})
		if cfg.Settings.Reporter.Text == nil {
			// config should be loaded only once
			cfg.Settings.Reporter.Text = config.DefaultConfig().Settings.Reporter.Text
		}
		reporter, err = text.NewReporter(*cfg.Settings.Reporter.Text, reportLog)
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeInternal, "failed to initialize Text reporter")
		}
		reportLog.Infof(ctx, "Using Text reporter (Color: %t)", !cfg.Settings.Reporter.Text.NoColor)
	default:
		return nil, errors.NewUserFacing(errors.CodeConfigValidation, fmt.Sprintf("unsupported reporter type: %s", cfg.Settings.ReporterType), "Supported: text")
	}

	logger.Debugf(ctx, "Registering resource comparers")
	computeComparer := compute.NewInstanceComparer()
	if err = registry.RegisterResourceComparer(computeComparer); err != nil {
		return nil, err
	}
	logger.Debugf(ctx, "Registered comparer for: %s", computeComparer.Kind())

	logger.Debugf(ctx, "Initializing analysis engine")
	engine, err := service.NewDriftAnalysisEngine(
		registry, matcher, reporter, logger.WithFields(map[string]any{"component": "engine"}),
		cfg, cfg.Settings.Concurrency, stateProvider, platformProvider,
	)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "failed to initialize drift analysis engine")
	}

	logger.Infof(ctx, "Application bootstrap complete")
	return &Application{Engine: engine, Logger: logger, Config: cfg}, nil
}

func parseAttributesOverride(override string) map[domain.ResourceKind][]string {
	if override == "" {
		return nil
	}
	parsed := make(map[domain.ResourceKind][]string)
	pairs := strings.Split(override, ";")
	for _, pair := range pairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			// accept logger as argument
			fmt.Fprintf(os.Stderr, "Warning: Skipping invalid attribute override format: %s\n", pair)
			continue
		}
		kind := domain.ResourceKind(strings.TrimSpace(parts[0]))
		attrsRaw := strings.Split(parts[1], ",")
		attrs := make([]string, 0, len(attrsRaw))
		for _, a := range attrsRaw {
			trimmed := strings.TrimSpace(a)
			if trimmed != "" {
				attrs = append(attrs, trimmed)
			}
		}
		if len(attrs) > 0 {
			parsed[kind] = attrs
		}
	}
	return parsed
}
