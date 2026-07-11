package transcode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/source/filesystem"
)

func sineFLAC(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH; fixture tests skipped")
	}
	out := filepath.Join(t.TempDir(), "sine.flac")
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fixture: %v\n%s", err, b)
	}
	return out
}

func TestFFmpegTranscodesToAAC(t *testing.T) {
	srcPath := sineFLAC(t)
	src, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	dst := filepath.Join(t.TempDir(), "out.m4a")
	if err := NewFFmpeg().TranscodeAAC(context.Background(), src, dst, 128); err != nil {
		t.Fatal(err)
	}

	// The output must be a real m4a our own prober understands.
	info, err := filesystem.Probe(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Container != "m4a" || info.Codec != "aac" {
		t.Fatalf("output: %+v", info)
	}
	if info.DurationMs < 1700 || info.DurationMs > 2400 {
		t.Fatalf("duration drift: %d", info.DurationMs)
	}
	st, _ := os.Stat(dst)
	if st.Size() == 0 {
		t.Fatal("empty output")
	}
}

func TestFFmpegFailsOnGarbage(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	dst := filepath.Join(t.TempDir(), "out.m4a")
	err := NewFFmpeg().TranscodeAAC(context.Background(), strings.NewReader("not audio"), dst, 128)
	if err == nil {
		t.Fatal("garbage input must fail")
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Fatal("failed transcode must not leave a destination file")
	}
}
