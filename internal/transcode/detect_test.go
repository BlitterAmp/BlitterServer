package transcode

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFFmpegAvailableFollowsPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix PATH stub")
	}
	t.Setenv("PATH", t.TempDir())
	if FFmpegAvailable() {
		t.Fatal("empty PATH must mean unavailable")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "ffmpeg")
	os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", dir)
	if !FFmpegAvailable() {
		t.Fatal("stub on PATH must mean available")
	}
}
