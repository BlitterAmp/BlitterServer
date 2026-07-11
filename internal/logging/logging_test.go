package logging

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFromFallsBackToDefault(t *testing.T) {
	if From(context.Background()) == nil {
		t.Fatal("From must never return nil")
	}
}

func TestWithFromRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil)).With("request_id", "req_test1")
	ctx := With(context.Background(), l)
	From(ctx).Info("deep in the stack")
	if !strings.Contains(buf.String(), "request_id=req_test1") {
		t.Fatalf("context attrs lost: %q", buf.String())
	}
}

func TestSetupWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "blitterserver.log")
	l, err := Setup(Options{Level: "info", Format: "text", FileEnabled: true, FilePath: path, MaxSizeMB: 1, MaxBackups: 1, MaxAgeDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	l.Info("hello file")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "hello file") {
		t.Fatalf("log file missing entry: %v %q", err, data)
	}
}

func TestSetupRejectsBadLevel(t *testing.T) {
	if _, err := Setup(Options{Level: "loud"}); err == nil {
		t.Fatal("want error for unknown level")
	}
}
