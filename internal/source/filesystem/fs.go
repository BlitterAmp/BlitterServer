// Package filesystem is the MusicSource adapter for a plain directory of
// audio files — the product's primary source: files are a complete library.
package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
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

const parserVersion = 2

// ErrCandidateChanged means a file changed while a scan was considering it.
var ErrCandidateChanged = errors.New("track candidate changed during parse")

type enumerationError struct {
	operation string
	cause     error
}

func (e *enumerationError) Error() string { return "filesystem " + e.operation + " failed" }
func (e *enumerationError) Unwrap() error { return e.cause }

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
	normalized, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("filesystem source: %w", err)
	}
	st, err := os.Stat(normalized)
	if err != nil {
		return nil, fmt.Errorf("filesystem source: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("filesystem source: %s is not a directory", normalized)
	}
	return &Source{root: normalized}, nil
}

func (s *Source) Kind() string { return "filesystem" }

func (s *Source) ParserVersion() int { return parserVersion }

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
	realRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if realPath != realRoot && !strings.HasPrefix(realPath, realRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("native id %q escapes the source root", nativeID)
	}
	return realPath, nil
}

// Enumerate walks supported files and reports only stat-derived fingerprints.
func (s *Source) Enumerate(ctx context.Context, emit func(source.TrackCandidate) error) error {
	return filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return &enumerationError{operation: "walk", cause: err}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() || !audioExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		resolved, err := s.resolvePath(path)
		if err != nil {
			return &enumerationError{operation: "resolve", cause: err}
		}
		st, err := os.Stat(resolved)
		if err != nil {
			return &enumerationError{operation: "stat", cause: err}
		}
		if !st.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		return emit(source.TrackCandidate{NativeID: filepath.ToSlash(rel), SizeBytes: st.Size(), MtimeNS: st.ModTime().UnixNano()})
	})
}

func (s *Source) resolvePath(path string) (string, error) {
	rel, err := filepath.Rel(s.root, path)
	if err != nil {
		return "", err
	}
	return s.resolve(filepath.ToSlash(rel))
}

// Parse probes and reads tags only after verifying the enumerated fingerprint.
func (s *Source) Parse(ctx context.Context, candidate source.TrackCandidate) (source.TrackMeta, error) {
	path, err := s.resolve(candidate.NativeID)
	if err != nil {
		return source.TrackMeta{}, err
	}
	if err := ctx.Err(); err != nil {
		return source.TrackMeta{}, err
	}
	before, err := os.Stat(path)
	if err != nil || !sameCandidate(candidate, before) {
		return source.TrackMeta{}, ErrCandidateChanged
	}
	meta, err := s.read(path, candidate)
	if err != nil {
		return source.TrackMeta{}, err
	}
	if err := ctx.Err(); err != nil {
		return source.TrackMeta{}, err
	}
	after, err := os.Stat(path)
	if err != nil || !sameCandidate(candidate, after) {
		return source.TrackMeta{}, ErrCandidateChanged
	}
	return meta, nil
}

func sameCandidate(candidate source.TrackCandidate, st os.FileInfo) bool {
	return st != nil && st.Mode().IsRegular() && st.Size() == candidate.SizeBytes && st.ModTime().UnixNano() == candidate.MtimeNS
}

func (s *Source) read(path string, candidate source.TrackCandidate) (source.TrackMeta, error) {
	probe, err := Probe(path)
	if err != nil {
		return source.TrackMeta{}, err
	}

	meta := source.TrackMeta{
		NativeID:    candidate.NativeID,
		Title:       strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		DurationMs:  probe.DurationMs,
		Container:   probe.Container,
		Codec:       probe.Codec,
		BitrateKbps: probe.BitrateKbps,
		SizeBytes:   probe.SizeBytes,
		Version:     sourceRevision(candidate.SizeBytes, candidate.MtimeNS),
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
		meta.Album = normalizeAlbumTitle(strings.TrimSpace(t.Album()), candidate.NativeID)
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

func normalizeAlbumTitle(album, nativeID string) string {
	if !strings.HasPrefix(album, ": ") {
		return album
	}
	pathTitle, _, ok := source.FilesystemAlbumPathEvidence(nativeID)
	withoutPrefix := strings.TrimSpace(strings.TrimPrefix(album, ":"))
	if ok && strings.EqualFold(pathTitle, withoutPrefix) {
		return pathTitle
	}
	return album
}

func sourceRevision(sizeBytes, mtimeNS int64) int64 {
	var fingerprint [16]byte
	binary.LittleEndian.PutUint64(fingerprint[:8], uint64(sizeBytes))
	binary.LittleEndian.PutUint64(fingerprint[8:], uint64(mtimeNS))
	sum := sha256.Sum256(fingerprint[:])
	revision := int64(binary.LittleEndian.Uint64(sum[:8]) & uint64(^uint64(0)>>1))
	if revision == 0 {
		return 1
	}
	return revision
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

func (s *Source) Art(ctx context.Context, candidate source.TrackCandidate, expectedHash string) ([]byte, string, error) {
	path, err := s.resolve(candidate.NativeID)
	if err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	before, err := os.Stat(path)
	if err != nil || !sameCandidate(candidate, before) {
		return nil, "", ErrCandidateChanged
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	t, err := tag.ReadFrom(f)
	if err != nil {
		return nil, "", fmt.Errorf("read embedded art: %w", err)
	}
	pic := t.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return nil, "", errors.New("no embedded art")
	}
	sum := sha256.Sum256(pic.Data)
	if hex.EncodeToString(sum[:]) != expectedHash {
		return nil, "", errors.New("embedded art hash mismatch")
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	after, err := os.Stat(path)
	if err != nil || !sameCandidate(candidate, after) {
		return nil, "", ErrCandidateChanged
	}
	mime := pic.MIMEType
	if mime == "" {
		mime = "image/jpeg"
	}
	return pic.Data, mime, nil
}

var _ source.MusicSource = (*Source)(nil)
