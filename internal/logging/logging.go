// Package logging owns slog construction and request-scoped logger plumbing.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

type Options struct {
	Level, Format string
	FileEnabled   bool
	FilePath      string
	MaxSizeMB     int
	MaxBackups    int
	MaxAgeDays    int
	Compress      bool
}

type ctxKey struct{}

// With returns ctx carrying l; From retrieves it anywhere down the stack.
func With(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From never returns nil: absent a request logger it yields the process default.
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// Setup builds the process root logger (stdout + optional rotating file),
// installs it as slog default, and returns it.
func Setup(o Options) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(orDefault(o.Level, "info"))); err != nil {
		return nil, fmt.Errorf("log level %q: %w", o.Level, err)
	}
	var w io.Writer = os.Stdout
	if o.FileEnabled {
		if err := os.MkdirAll(filepath.Dir(o.FilePath), 0o755); err != nil {
			return nil, fmt.Errorf("log dir: %w", err)
		}
		w = io.MultiWriter(os.Stdout, &lumberjack.Logger{
			Filename: o.FilePath, MaxSize: o.MaxSizeMB,
			MaxBackups: o.MaxBackups, MaxAge: o.MaxAgeDays, Compress: o.Compress,
		})
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	switch orDefault(o.Format, "text") {
	case "json":
		h = slog.NewJSONHandler(w, opts)
	case "text":
		h = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("log format %q: want text or json", o.Format)
	}
	l := slog.New(h)
	slog.SetDefault(l)
	return l, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
