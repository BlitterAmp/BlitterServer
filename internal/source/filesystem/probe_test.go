package filesystem

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// genFixture renders a 2-second 440Hz sine into the requested container with
// a full tag set. Tests skip when ffmpeg is unavailable.
func genFixture(t *testing.T, dir, name string, extraArgs ...string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH; fixture tests skipped")
	}
	out := filepath.Join(dir, name)
	args := []string{"-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-metadata", "title=Sine Song", "-metadata", "artist=Test Artist",
		"-metadata", "album_artist=Test Artist", "-metadata", "album=Test Album",
		"-metadata", "genre=Electronic", "-metadata", "date=2021",
		"-metadata", "track=3", "-metadata", "disc=1"}
	args = append(args, extraArgs...)
	args = append(args, out)
	cmd := exec.Command("ffmpeg", args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %s: %v\n%s", name, err, b)
	}
	return out
}

func durationClose(gotMs int) bool { return gotMs > 1700 && gotMs < 2400 }

func TestProbeFormats(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		file      string
		args      []string
		container string
		codec     string
	}{
		{"sine.flac", nil, "flac", "flac"},
		{"sine.mp3", []string{"-b:a", "192k"}, "mp3", "mp3"},
		{"sine-vbr.mp3", []string{"-q:a", "4"}, "mp3", "mp3"},
		{"sine.m4a", []string{"-c:a", "aac"}, "m4a", "aac"},
		{"sine.ogg", []string{"-ac", "2", "-c:a", "vorbis", "-strict", "experimental"}, "ogg", "vorbis"},
		{"sine.opus", []string{"-c:a", "libopus"}, "opus", "opus"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			path := genFixture(t, dir, tc.file, tc.args...)
			info, err := Probe(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Container != tc.container || info.Codec != tc.codec {
				t.Fatalf("want %s/%s, got %s/%s", tc.container, tc.codec, info.Container, info.Codec)
			}
			if !durationClose(info.DurationMs) {
				t.Fatalf("duration: want ~2000ms, got %d", info.DurationMs)
			}
			if info.BitrateKbps <= 0 {
				t.Fatalf("bitrate must be positive, got %d", info.BitrateKbps)
			}
		})
	}
}

func TestProbeCBRMp3WithoutXing(t *testing.T) {
	dir := t.TempDir()
	// -write_xing 0 forces a bare CBR stream — exercises the estimate path.
	path := genFixture(t, dir, "cbr.mp3", "-b:a", "128k", "-write_xing", "0")
	info, err := Probe(path)
	if err != nil {
		t.Fatal(err)
	}
	if !durationClose(info.DurationMs) {
		t.Fatalf("CBR estimate: want ~2000ms, got %d", info.DurationMs)
	}
	if info.BitrateKbps != 128 {
		t.Fatalf("CBR bitrate: want 128, got %d", info.BitrateKbps)
	}
}

func TestProbeRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "junk.mp3")
	os.WriteFile(bad, []byte("this is not audio at all, sorry"), 0o644)
	if _, err := Probe(bad); err == nil {
		t.Fatal("garbage must not probe successfully")
	}
	if _, err := Probe(filepath.Join(dir, "missing.flac")); err == nil {
		t.Fatal("missing file must error")
	}
}

func TestProbeUnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "cover.jpg")
	os.WriteFile(f, []byte{0xff, 0xd8, 0xff}, 0o644)
	if _, err := Probe(f); err == nil {
		t.Fatal("unsupported extension must error")
	}
}
