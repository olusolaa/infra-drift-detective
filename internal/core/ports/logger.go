package ports

import "context"

type Logger interface {
	Debugf(ctx context.Context, format string, args ...any)
	Infof(ctx context.Context, format string, args ...any)
	Warnf(ctx context.Context, format string, args ...any)
	Errorf(ctx context.Context, err error, format string, args ...any)
	WithFields(fields map[string]any) Logger // Returns a new logger with added context
}
