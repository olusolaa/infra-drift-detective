package config

import (
	"github.com/olusolaa/infra-drift-detector/internal/adapters/matching/tag"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfstate"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/log"
	"github.com/olusolaa/infra-drift-detector/internal/reporting/text"
)

type Config struct {
	Settings  SettingsConfig   `yaml:"settings"`
	State     StateConfig      `yaml:"state"`
	Platform  PlatformConfig   `yaml:"platform"`
	Resources []ResourceConfig `yaml:"resources"`
}

type SettingsConfig struct {
	LogLevel     log.Level       `yaml:"log_level"`
	LogFormat    log.Format      `yaml:"log_format"`
	Concurrency  int             `yaml:"concurrency"`
	MatcherType  string          `yaml:"matcher"`
	ReporterType string          `yaml:"reporter"`
	Matcher      MatcherConfigs  `yaml:"matcher_config"`
	Reporter     ReporterConfigs `yaml:"reporter_config"`
}

type StateConfig struct {
	// Type implicitly determines which sub-config is used (e.g., "tfstate")
	// We'll likely determine this based on which sub-field is non-nil during loading.
	TFState *tfstate.Config `yaml:"tfstate,omitempty"`
	// TFHCL *tfhcl.Config `yaml:"tfhcl,omitempty"` // Future
}

type PlatformConfig struct {
	// Type string `yaml:"type"` // e.g., "aws" - maybe implicit if only AWS supported initially
	AWS *AWSPlatformConfig `yaml:"aws,omitempty"`
	// GCP *GCPPlatformConfig `yaml:"gcp,omitempty"` // Future
}

type AWSPlatformConfig struct {
	// Region string `yaml:"region"` // Often determined by env/profile, but allow override
	// Profile string `yaml:"profile"` // Allow specifying AWS profile
	// Credentials can be handled via standard AWS SDK chain (env, shared creds, role)
}

type ResourceConfig struct {
	Kind            domain.ResourceKind `yaml:"kind"`
	PlatformFilters map[string]string   `yaml:"platform_filters"`
	Attributes      []string            `yaml:"attributes"`
}

type MatcherConfigs struct {
	Tag *tag.Config `yaml:"tag,omitempty"`
	// Explicit *explicit.Config `yaml:"explicit,omitempty"` // Future
}

type ReporterConfigs struct {
	Text *text.Config `yaml:"text,omitempty"`
	// JSON *json.Config `yaml:"json,omitempty"` // Future
}

func (c *Config) GetFiltersForKind(kind domain.ResourceKind) map[string]string {
	for _, rc := range c.Resources {
		if rc.Kind == kind {
			return rc.PlatformFilters
		}
	}
	return make(map[string]string)
}
func (c *Config) GetAttributesForKind(kind domain.ResourceKind) []string {
	for _, rc := range c.Resources {
		if rc.Kind == kind {
			return rc.Attributes
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

func DefaultConfig() *Config {
	return &Config{
		Settings: SettingsConfig{
			LogLevel:     log.LevelInfo,
			LogFormat:    log.FormatText,
			Concurrency:  10,
			MatcherType:  tag.MatcherTypeTag,
			ReporterType: text.ReporterTypeText,
			Matcher: MatcherConfigs{
				Tag: &tag.Config{TagKey: "TFResourceAddress"},
			},
			Reporter: ReporterConfigs{
				Text: &text.Config{NoColor: false},
			},
		},
		State: StateConfig{
			TFState: &tfstate.Config{FilePath: "terraform.tfstate"},
		},
		Platform: PlatformConfig{
			AWS: &AWSPlatformConfig{},
		},
		Resources: []ResourceConfig{},
	}
}
