// Package filesystem is the MusicSource adapter for a plain directory of
// audio files — the product's primary source: files are a complete library.
package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/dhowden/tag"
	"github.com/dhowden/tag/mbz"
)

var audioExts = map[string]bool{
	".flac": true, ".mp3": true, ".m4a": true, ".mp4": true,
	".ogg": true, ".opus": true, ".oga": true,
}

// Source scans and serves audio files under a root directory. Native ids are
// root-relative paths.
type Source struct {
	root string
}

func New(root string) (*Source, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("filesystem source: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("filesystem source: %s is not a directory", root)
	}
	return &Source{root: root}, nil
}

func (s *Source) Kind() string { return "filesystem" }

// resolve maps a native id back to an absolute path, refusing escapes.
func (s *Source) resolve(nativeID string) (string, error) {
	p := filepath.Join(s.root, filepath.FromSlash(nativeID))
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(s.root)
	if err != nil {
		return "", err
	}
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("native id %q escapes the source root", nativeID)
	}
	return abs, nil
}

func (s *Source) Scan(ctx context.Context, emit func(source.TrackMeta) error) error {
	return filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() || !audioExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		meta, err := s.read(path)
		if err != nil {
			// A single unreadable file must not kill the scan; it simply
			// isn't library material until it parses.
			return nil
		}
		return emit(meta)
	})
}

func (s *Source) read(path string) (source.TrackMeta, error) {
	rel, err := filepath.Rel(s.root, path)
	if err != nil {
		return source.TrackMeta{}, err
	}
	probe, err := Probe(path)
	if err != nil {
		return source.TrackMeta{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return source.TrackMeta{}, err
	}

	meta := source.TrackMeta{
		NativeID:    rel,
		Title:       strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		DurationMs:  probe.DurationMs,
		Container:   probe.Container,
		Codec:       probe.Codec,
		BitrateKbps: probe.BitrateKbps,
		SizeBytes:   probe.SizeBytes,
		Version:     st.ModTime().Unix(),
	}

	f, err := os.Open(path)
	if err != nil {
		return source.TrackMeta{}, err
	}
	defer f.Close()
	if t, err := tag.ReadFrom(f); err == nil {
		if v := strings.TrimSpace(t.Title()); v != "" {
			meta.Title = v
		}
		artist := strings.TrimSpace(t.Artist())
		albumArtist := strings.TrimSpace(t.AlbumArtist())
		ids := mbz.Extract(t)
		meta.RecordingMBID = validUUID(ids.Get(mbz.Recording))
		meta.ReleaseMBID = validUUID(ids.Get(mbz.Album))
		meta.ReleaseGroupMBID = validUUID(ids.Get(mbz.ReleaseGroup))
		meta.TrackCredits = []source.ArtistCredit{{Name: artist, MBID: validUUID(ids.Get(mbz.Artist))}}
		meta.AlbumCredits = []source.ArtistCredit{{Name: albumArtist, MBID: validUUID(ids.Get(mbz.AlbumArtist))}}
		meta.PrimaryArtist.Name = albumArtist
		meta.PrimaryArtist.MBID = validUUID(ids.Get(mbz.AlbumArtist))
		meta.Album = strings.TrimSpace(t.Album())
		meta.Genre = strings.TrimSpace(t.Genre())
		meta.Year = t.Year()
		meta.Index, _ = t.Track()
		meta.Disc, _ = t.Disc()
		if pic := t.Picture(); pic != nil && len(pic.Data) > 0 {
			sum := sha256.Sum256(pic.Data)
			meta.ArtHash = hex.EncodeToString(sum[:])
		}
	}
	// Fallbacks: pathless libraries still get sensible grouping.
	if len(meta.TrackCredits) == 0 || meta.TrackCredits[0].Name == "" {
		meta.TrackCredits = []source.ArtistCredit{{Name: "Unknown Artist"}}
	}
	if meta.PrimaryArtist.Name == "" {
		meta.PrimaryArtist.Name = meta.TrackCredits[0].Name
		meta.PrimaryArtist.MBID = meta.TrackCredits[0].MBID
	}
	if len(meta.AlbumCredits) == 0 || meta.AlbumCredits[0].Name == "" {
		meta.AlbumCredits = []source.ArtistCredit{{Name: meta.PrimaryArtist.Name, MBID: meta.PrimaryArtist.MBID}}
	}
	if meta.Album == "" {
		meta.Album = "Unknown Album"
	}
	return meta, nil
}

func validUUID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return ""
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", r) {
			return ""
		}
	}
	return value
}

func (s *Source) Open(_ context.Context, nativeID string) (io.ReadSeekCloser, error) {
	path, err := s.resolve(nativeID)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (s *Source) Art(_ context.Context, nativeID string) ([]byte, string, error) {
	path, err := s.resolve(nativeID)
	if err != nil {
		return nil, "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	t, err := tag.ReadFrom(f)
	if err != nil {
		return nil, "", err
	}
	pic := t.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return nil, "", fmt.Errorf("no embedded art in %s", nativeID)
	}
	mime := pic.MIMEType
	if mime == "" {
		mime = "image/jpeg"
	}
	return pic.Data, mime, nil
}

var _ source.MusicSource = (*Source)(nil)
