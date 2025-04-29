package log

import (
	"context"
	"errors"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	apperrors "github.com/olusolaa/infra-drift-detector/internal/errors"
	"io"
	"log/slog"
	"os"
)

type slogAdapter struct {
	logger *slog.Logger
}

func NewLogger(cfg Config) (ports.Logger, error) {
	var level slog.Level
	switch cfg.Level {
	case LevelDebug:
		level = slog.LevelDebug
	case LevelInfo:
		level = slog.LevelInfo
	case LevelWarn:
		level = slog.LevelWarn
	case LevelError:
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
		// AddSource: true, // Adds source file and line number - potentially useful
	}

	var handler slog.Handler
	outputWriter := io.Writer(os.Stderr) // Default to stderr

	switch cfg.Format {
	case FormatJSON:
		handler = slog.NewJSONHandler(outputWriter, opts)
	case FormatText:
		fallthrough
	default:
		handler = slog.NewTextHandler(outputWriter, opts)
	}

	logger := slog.New(handler)
	return &slogAdapter{logger: logger}, nil
}

func (s *slogAdapter) log(ctx context.Context, level slog.Level, err error, format string, args ...any) {
	if !s.logger.Enabled(ctx, level) {
		return
	}
	// Removed unused variable 'pcs'
	// skip [runtime.Callers, this function, helper function]
	// This depth might need adjustment depending on call stack.
	// We primarily want the caller of Debugf/Infof etc.
	// Let's try depth 3 for now. AddSource in HandlerOptions might be simpler.
	// runtime.Callers(3, pcs[:])
	// r := slog.NewRecord(time.Now(), level, fmt.Sprintf(format, args...), pcs[0])

	// Simpler approach without explicit caller info for now. AddSource=true handles it.
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}

	attrs := []slog.Attr{}
	if err != nil {
		// Add structured error info if available
		var appErr *apperrors.AppError
		if errors.As(err, &appErr) {
			attrs = append(attrs, slog.String("error_code", string(appErr.Code)))
			if appErr.InternalDetails != "" {
				attrs = append(attrs, slog.String("error_details", appErr.InternalDetails))
			}
			if appErr.WrappedError != nil {
				attrs = append(attrs, slog.String("error_wrapped", appErr.WrappedError.Error()))
			}
		} else {
			attrs = append(attrs, slog.String("error", err.Error()))
		}
	}

	s.logger.LogAttrs(ctx, level, msg, attrs...)
}

func (s *slogAdapter) Debugf(ctx context.Context, format string, args ...any) {
	s.log(ctx, slog.LevelDebug, nil, format, args...)
}

func (s *slogAdapter) Infof(ctx context.Context, format string, args ...any) {
	s.log(ctx, slog.LevelInfo, nil, format, args...)
}

func (s *slogAdapter) Warnf(ctx context.Context, format string, args ...any) {
	s.log(ctx, slog.LevelWarn, nil, format, args...)
}

func (s *slogAdapter) Errorf(ctx context.Context, err error, format string, args ...any) {
	s.log(ctx, slog.LevelError, err, format, args...)
}

func (s *slogAdapter) WithFields(fields map[string]any) ports.Logger {
	attrs := make([]slog.Attr, 0, len(fields))
	for k, v := range fields {
		attrs = append(attrs, slog.Any(k, v))
	}
	anyAttrs := make([]any, len(attrs))
	for i, attr := range attrs {
		anyAttrs[i] = attr
	}
	newLogger := s.logger.With(anyAttrs...)
	return &slogAdapter{logger: newLogger}
}
