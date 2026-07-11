package transcode

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
)

// Transcoder is the port the artifact pipeline drives; FFmpeg is the only
// adapter (external binary, never linked).
type Transcoder interface {
	// TranscodeAAC renders src into an AAC/m4a file at dst.
	TranscodeAAC(ctx context.Context, src io.Reader, dst string, bitrateKbps int) error
}

// FFmpeg shells out to the system ffmpeg.
type FFmpeg struct{}

func NewFFmpeg() *FFmpeg { return &FFmpeg{} }

// TranscodeAAC spools src to a temp file first: piped input breaks on
// containers with trailing metadata (m4a moov), and a file input lets ffmpeg
// seek freely.
func (f *FFmpeg) TranscodeAAC(ctx context.Context, src io.Reader, dst string, bitrateKbps int) error {
	tmp, err := os.CreateTemp("", "blitter-transcode-*.src")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return fmt.Errorf("spool source: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	out := dst + ".partial"
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-i", tmp.Name(), "-vn", "-c:a", "aac", "-b:a", strconv.Itoa(bitrateKbps)+"k",
		"-f", "ipod", "-movflags", "+faststart", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		os.Remove(out)
		return fmt.Errorf("ffmpeg: %w: %s", err, b)
	}
	return os.Rename(out, dst)
}

var _ Transcoder = (*FFmpeg)(nil)
