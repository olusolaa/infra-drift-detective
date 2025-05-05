package config

import (
	"github.com/olusolaa/infra-drift-detector/internal/adapters/matching/tag"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfstate"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/log"
	"github.com/olusolaa/infra-drift-detector/internal/reporting/json"
	"github.com/olusolaa/infra-drift-detector/internal/reporting/text"
)

type Config struct {
	Settings  SettingsConfig   `yaml:"settings" mapstructure:"settings" validate:"required"`
	State     StateConfig      `yaml:"state" mapstructure:"state" validate:"required"`
	Platform  PlatformConfig   `yaml:"platform" mapstructure:"platform" validate:"required"`
	Resources []ResourceConfig `yaml:"resources" mapstructure:"resources" validate:"required,min=1,dive"`
}

type SettingsConfig struct {
	LogLevel     log.Level       `yaml:"log_level" mapstructure:"log_level" validate:"required,oneof=debug info warn error"`
	LogFormat    log.Format      `yaml:"log_format" mapstructure:"log_format" validate:"required,oneof=text json"`
	Concurrency  int             `yaml:"concurrency" mapstructure:"concurrency" validate:"required,min=1"`
	MatcherType  string          `yaml:"matcher" mapstructure:"matcher" validate:"required,oneof=tag"`
	ReporterType string          `yaml:"reporter" mapstructure:"reporter" validate:"required,oneof=text"`
	Matcher      MatcherConfigs  `yaml:"matcher_config" mapstructure:"matcher_config" validate:"required"`
	Reporter     ReporterConfigs `yaml:"reporter_config" mapstructure:"reporter_config"`
}

type StateConfig struct {
	ProviderType string          `yaml:"provider_type" mapstructure:"provider_type" validate:"required,oneof=tfstate tfhcl"`
	TFState      *tfstate.Config `yaml:"tfstate,omitempty" mapstructure:"tfstate,omitempty" validate:"required_if=ProviderType tfstate"`
	TFHCL        *tfhcl.Config   `yaml:"tfhcl,omitempty" mapstructure:"tfhcl,omitempty" validate:"required_if=ProviderType tfhcl"`
}

type PlatformConfig struct {
	AWS *AWSPlatformConfig `yaml:"aws,omitempty" mapstructure:"aws,omitempty"`
}

type AWSPlatformConfig struct {
	APIRequestsPerSecond int    `yaml:"api_rps" mapstructure:"api_rps" validate:"omitempty,min=1,max=100"`
	Region               string `yaml:"region" mapstructure:"region" validate:"required"`
	Profile              string `yaml:"profile" mapstructure:"profile" validate:"required"`
}

type ResourceConfig struct {
	Kind            domain.ResourceKind `yaml:"kind" mapstructure:"kind" validate:"required"`
	PlatformFilters map[string]string   `yaml:"platform_filters" mapstructure:"platform_filters"`
	Attributes      []string            `yaml:"attributes" mapstructure:"attributes" validate:"required,min=1,dive,required"`
}

type MatcherConfigs struct {
	Tag *tag.Config `yaml:"tag,omitempty" mapstructure:"tag,omitempty" validate:"required_if=../MatcherType tag"`
}

type ReporterConfigs struct {
	Text *text.Config `yaml:"text,omitempty" mapstructure:"text,omitempty"`
	JSON *json.Config `yaml:"json,omitempty" mapstructure:"json,omitempty"`
}

type TFHCLConfig struct {
	Directory string   `yaml:"directory" mapstructure:"directory" validate:"required,dir"`
	VarFiles  []string `yaml:"var_files" mapstructure:"var_files" validate:"omitempty,dive,file"`
	Workspace string   `yaml:"workspace" mapstructure:"workspace" validate:"required"`
}

func DefaultConfig() *Config {
	return &Config{
		Settings: SettingsConfig{
			LogLevel:     log.LevelInfo,
			LogFormat:    log.FormatText,
			Concurrency:  10,
			MatcherType:  "tag",
			ReporterType: "text",
			Matcher: MatcherConfigs{
				Tag: &tag.Config{TagKey: "TFResourceAddress"},
			},
			Reporter: ReporterConfigs{
				Text: &text.Config{NoColor: false},
				JSON: &json.Config{},
			},
		},
		State: StateConfig{
			ProviderType: tfstate.ProviderTypeTFState,
			TFState:      &tfstate.Config{FilePath: ""},
			TFHCL:        &tfhcl.Config{Directory: ".", Workspace: "default"},
		},
		Platform: PlatformConfig{
			AWS: &AWSPlatformConfig{APIRequestsPerSecond: 20},
		},
		Resources: []ResourceConfig{},
	}
}

func (c *Config) GetFiltersForKind(kind domain.ResourceKind) map[string]string {
	for _, rc := range c.Resources {
		if rc.Kind == kind {
			if rc.PlatformFilters == nil {
				return make(map[string]string)
			}
			filters := make(map[string]string, len(rc.PlatformFilters))
			for k, v := range rc.PlatformFilters {
				filters[k] = v
			}
			return filters
		}
	}
	return make(map[string]string)
}

func (c *Config) GetAttributesForKind(kind domain.ResourceKind) []string {
	for _, rc := range c.Resources {
		if rc.Kind == kind {
			if rc.Attributes == nil {
				return nil
			}
			attrs := make([]string, len(rc.Attributes))
			copy(attrs, rc.Attributes)
			return attrs
		}
	}
	return nil
}

func (c *Config) GetResourceKinds() []domain.ResourceKind {
	kindsMap := make(map[domain.ResourceKind]struct{})
	for _, rc := range c.Resources {
		kindsMap[rc.Kind] = struct{}{}
	}
	kinds := make([]domain.ResourceKind, 0, len(kindsMap))
	for k := range kindsMap {
		kinds = append(kinds, k)
	}
	return kinds
}
