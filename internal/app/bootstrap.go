package app

import (
	"context"
	stderrs "errors"
	"fmt"
	awsshared "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/shared"
	"github.com/olusolaa/infra-drift-detector/internal/reporting/json"
	"os"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/spf13/viper"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/matching/tag"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/limiter"
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
}

func BuildApplication(ctx context.Context, v *viper.Viper) (*Application, error) {
	cfg, err := initConfig(ctx, v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Configuration failed: %v\n", err)
		if appErr := (*errors.AppError)(nil); stderrs.As(err, &appErr) && appErr.IsUserFacing {
			fmt.Fprintf(os.Stderr, "Details: %s\n", appErr.Message)
			if appErr.SuggestedAction != "" {
				fmt.Fprintf(os.Stderr, "Suggestion: %s\n", appErr.SuggestedAction)
			}
		}
		return nil, err
	}

	logger, err := initLogger(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Failed to initialize logger: %v\n", err)
		return nil, err
	}
	logConfigFileUsage(ctx, v, logger)

	err = initServices(ctx, cfg, logger)
	if err != nil {
		logger.Errorf(ctx, err, "Failed to initialize shared services")
		return nil, err
	}
	registry := service.NewComponentRegistry()
	logger.Debugf(ctx, "Component registry initialized")

	stateProvider, err := initStateProvider(ctx, cfg, registry, logger)
	if err != nil {
		logger.Errorf(ctx, err, "Failed to initialize state provider")
		return nil, err
	}

	platformProvider, err := initPlatformProvider(ctx, cfg, registry, logger)
	if err != nil {
		logger.Errorf(ctx, err, "Failed to initialize platform provider")
		return nil, err
	}

	matcher, err := initMatcher(ctx, cfg, logger)
	if err != nil {
		logger.Errorf(ctx, err, "Failed to initialize matcher")
		return nil, err
	}

	reporter, err := initReporter(ctx, cfg, logger)
	if err != nil {
		logger.Errorf(ctx, err, "Failed to initialize reporter")
		return nil, err
	}

	err = initComparers(ctx, registry, logger)
	if err != nil {
		logger.Errorf(ctx, err, "Failed to initialize comparers")
		return nil, err
	}

	attributeOverrides := parseAttributesOverride(v.GetString("attributes"))
	if len(attributeOverrides) > 0 {
		logger.Infof(ctx, "Applying command-line attribute overrides")
	}

	engine, err := initEngine(
		ctx, cfg, registry, matcher, reporter, logger,
		stateProvider, platformProvider, attributeOverrides,
	)
	if err != nil {
		logger.Errorf(ctx, err, "Failed to initialize engine")
		return nil, err
	}

	logger.Infof(ctx, "Application bootstrap complete")
	return &Application{
		Engine: engine,
		Logger: logger,
	}, nil
}

func initConfig(ctx context.Context, v *viper.Viper) (*config.Config, error) {
	cfg := config.DefaultConfig()
	err := v.Unmarshal(cfg)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeConfigParseError, "failed to unmarshal configuration")
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
		return nil, errors.NewUserFacing(errors.CodeConfigValidation, errorDetails.String(), "Please check your configuration file or flags.")
	}
	return cfg, nil
}

func initLogger(ctx context.Context, cfg *config.Config) (ports.Logger, error) {
	logCfg := log.Config{Level: cfg.Settings.LogLevel, Format: cfg.Settings.LogFormat}
	logger, err := log.NewLogger(logCfg)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "logger initialization failed")
	}
	logger.Infof(ctx, "Logger initialized (Level: %s, Format: %s)", cfg.Settings.LogLevel, cfg.Settings.LogFormat)
	return logger, nil
}

func logConfigFileUsage(ctx context.Context, v *viper.Viper, logger ports.Logger) {
	if v.ConfigFileUsed() != "" {
		logger.Debugf(ctx, "Using configuration file: %s", v.ConfigFileUsed())
	} else {
		logger.Debugf(ctx, "No configuration file found, using defaults, env vars, and flags.")
	}
}

func initServices(ctx context.Context, cfg *config.Config, logger ports.Logger) error {
	awsPlatformCfg := cfg.Platform.AWS
	if awsPlatformCfg == nil {
		awsPlatformCfg = config.DefaultConfig().Platform.AWS
	}
	limiter.Initialize(awsPlatformCfg.APIRequestsPerSecond, logger)
	return nil
}

func initStateProvider(ctx context.Context, cfg *config.Config, registry *service.ComponentRegistry, logger ports.Logger) (ports.StateProvider, error) {
	var stateProvider ports.StateProvider
	var err error

	switch cfg.State.ProviderType {
	case tfstate.ProviderTypeTFState:
		provLog := logger.WithFields(map[string]any{"provider": tfstate.ProviderTypeTFState})
		stateProvider, err = tfstate.NewProvider(*cfg.State.TFState, provLog)
		if err == nil {
			provLog.Infof(ctx, "Using TFState provider: %s", cfg.State.TFState.FilePath)
		}
	case tfhcl.ProviderTypeTFHCL:
		provLog := logger.WithFields(map[string]any{"provider": tfhcl.ProviderTypeTFHCL})
		stateProvider, err = tfhcl.NewProvider(*cfg.State.TFHCL, provLog)
		if err == nil {
			provLog.Infof(ctx, "Using TFHCL provider: %s (Workspace: %s)", cfg.State.TFHCL.Directory, cfg.State.TFHCL.Workspace)
		}
	default:
		err = errors.NewUserFacing(errors.CodeConfigValidation, fmt.Sprintf("invalid state provider type: %s", cfg.State.ProviderType), "Supported: tfstate, tfhcl")
	}

	if err != nil {
		return nil, err
	}
	if errReg := registry.RegisterStateProvider(stateProvider); errReg != nil {
		logger.Errorf(ctx, errReg, "Failed to register state provider")
		return nil, errReg
	}
	return stateProvider, nil
}

func initPlatformProvider(ctx context.Context, cfg *config.Config, registry *service.ComponentRegistry, logger ports.Logger) (ports.PlatformProvider, error) {
	var platformProvider ports.PlatformProvider
	var err error

	if cfg.Platform.AWS != nil {
		provLog := logger.WithFields(map[string]any{"provider": awsshared.ProviderTypeAWS})
		platformProvider, err = aws.NewProvider(ctx, cfg, provLog)
		if err == nil {
			provLog.Infof(ctx, "Using AWS platform provider")
		}
	} else {
		err = errors.NewUserFacing(errors.CodeConfigValidation, "no supported platform provider configured", "Configure platform.aws section.")
	}

	if err != nil {
		return nil, err
	}
	if errReg := registry.RegisterPlatformProvider(platformProvider); errReg != nil {
		logger.Errorf(ctx, errReg, "Failed to register platform provider")
		return nil, errReg
	}
	return platformProvider, nil
}

func initMatcher(ctx context.Context, cfg *config.Config, logger ports.Logger) (ports.Matcher, error) {
	var matcher ports.Matcher
	var err error

	switch cfg.Settings.MatcherType {
	case tag.MatcherTypeTag:
		if cfg.Settings.Matcher.Tag == nil {
			return nil, errors.NewUserFacing(errors.CodeConfigValidation, "tag matcher selected but 'matcher_config.tag' section is missing", "Add matcher_config.tag.key.")
		}
		matchLog := logger.WithFields(map[string]any{"component": "matcher", "type": tag.MatcherTypeTag})
		matcher, err = tag.NewMatcher(*cfg.Settings.Matcher.Tag, matchLog)
		if err == nil {
			matchLog.Infof(ctx, "Using Tag matcher with key: %s", cfg.Settings.Matcher.Tag.TagKey)
		}
	default:
		err = errors.NewUserFacing(errors.CodeConfigValidation, fmt.Sprintf("unsupported matcher type: %s", cfg.Settings.MatcherType), "Supported: tag")
	}
	return matcher, err
}

func initReporter(ctx context.Context, cfg *config.Config, logger ports.Logger) (ports.Reporter, error) {
	var reporter ports.Reporter
	var err error

	switch cfg.Settings.ReporterType {
	case text.ReporterTypeText:
		reporterCfg := cfg.Settings.Reporter.Text
		if reporterCfg == nil {
			reporterCfg = config.DefaultConfig().Settings.Reporter.Text
		}
		reportLog := logger.WithFields(map[string]any{"component": "reporter", "type": text.ReporterTypeText})
		reporter, err = text.NewReporter(*reporterCfg, reportLog)
		if err == nil {
			reportLog.Infof(ctx, "Using Text reporter (Color: %t)", !reporterCfg.NoColor)
		}
	case json.ReporterTypeJSON:
		reporterCfg := cfg.Settings.Reporter.JSON
		if reporterCfg == nil {
			reporterCfg = config.DefaultConfig().Settings.Reporter.JSON
		}
		reportLog := logger.WithFields(map[string]any{"component": "reporter", "type": json.ReporterTypeJSON})
		reporter, err = json.NewReporter(*reporterCfg, reportLog)
		if err == nil {
			reportLog.Infof(ctx, "Using JSON reporter")
		}
	default:
		err = errors.NewUserFacing(errors.CodeConfigValidation, fmt.Sprintf("unsupported reporter type: %s", cfg.Settings.ReporterType), "Supported: text")
	}
	return reporter, err
}

func initComparers(ctx context.Context, registry *service.ComponentRegistry, logger ports.Logger) error {
	logger.Debugf(ctx, "Registering resource comparers")
	var err error

	computeComparer := compute.NewInstanceComparer()
	err = registry.RegisterResourceComparer(computeComparer)
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "failed to register ComputeInstance comparer")
	}
	logger.Debugf(ctx, "Registered comparer for: %s", computeComparer.Kind())

	return nil
}

func initEngine(
	ctx context.Context,
	cfg *config.Config,
	registry *service.ComponentRegistry,
	matcher ports.Matcher,
	reporter ports.Reporter,
	logger ports.Logger,
	stateProvider ports.StateProvider,
	platformProvider ports.PlatformProvider,
	attributeOverrides map[domain.ResourceKind][]string,
) (ports.DriftAnalysisEngine, error) {

	logger.Debugf(ctx, "Initializing analysis engine")

	finalAttributesToCheck := make(map[domain.ResourceKind][]string)
	for _, rCfg := range cfg.Resources {
		finalAttributesToCheck[rCfg.Kind] = rCfg.Attributes
	}
	for kind, attrs := range attributeOverrides {
		if _, exists := finalAttributesToCheck[kind]; exists {
			logger.Debugf(ctx, "Engine using overridden attributes for kind '%s': %v", kind, attrs)
			finalAttributesToCheck[kind] = attrs
		} else {
			logger.Debugf(ctx, "CLI override for kind '%s' ignored by engine as kind not in config 'resources'", kind)
		}
	}

	engineConfig := service.EngineRunConfig{
		ResourceKindsToProcess: cfg.GetResourceKinds(),
		AttributesToCheck:      finalAttributesToCheck,
		Concurrency:            cfg.Settings.Concurrency,
	}

	engine, err := service.NewDriftAnalysisEngine(
		registry,
		matcher,
		reporter,
		logger.WithFields(map[string]any{"component": "engine"}),
		engineConfig,
		stateProvider,
		platformProvider,
	)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "failed to initialize drift analysis engine")
	}
	return engine, nil
}
