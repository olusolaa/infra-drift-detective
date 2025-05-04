package log

type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

type Config struct {
	Level  Level  `yaml:"level"`
	Format Format `yaml:"format"`
	// OutputPath string `yaml:"output_path"` // Future: support file output
}

func DefaultConfig() Config {
	return Config{
		Level:  LevelInfo,
		Format: FormatText,
	}
}
