// Package transcode will own the ffmpeg Transcoder port (artifacts arc).
// For now it only answers: is ffmpeg present on this host?
package transcode

import "os/exec"

func FFmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}
