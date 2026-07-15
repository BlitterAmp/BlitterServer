package source

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

var (
	discDirectoryPattern = regexp.MustCompile(`(?i)^(?:cd|disc|disk)\s*0*([1-9]\d*)$`)
	directoryYearPattern = regexp.MustCompile(`\s*[\(\[]\d{4}[\)\]]\s*$`)
)

// FilesystemAlbumPathEvidence extracts a conservative album directory and
// optional disc number from a root-relative native id.
func FilesystemAlbumPathEvidence(nativeID string) (string, int, bool) {
	nativeID = strings.ReplaceAll(nativeID, `\`, "/")
	if nativeID == "" || path.IsAbs(nativeID) {
		return "", 0, false
	}
	clean := path.Clean(nativeID)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", 0, false
	}
	parts := strings.Split(clean, "/")
	if len(parts) < 3 {
		return "", 0, false
	}
	parts = parts[:len(parts)-1]
	disc := 0
	if match := discDirectoryPattern.FindStringSubmatch(strings.TrimSpace(parts[len(parts)-1])); match != nil {
		if len(parts) < 3 {
			return "", 0, false
		}
		disc, _ = strconv.Atoi(match[1])
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 {
		return "", 0, false
	}
	title := strings.TrimSpace(directoryYearPattern.ReplaceAllString(parts[len(parts)-1], ""))
	if title == "" {
		return "", 0, false
	}
	return title, disc, true
}
