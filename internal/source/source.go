// Package source defines the MusicSource port: what any library backend
// (filesystem, Plex, …) must provide. Domain code depends on this interface,
// never on an adapter.
package source

import (
	"context"
	"io"
)

// TrackMeta is one track's identity, tags, and media info as reported by a
// source during a scan.
type TrackMeta struct {
	NativeID    string // stable within the source; filesystem uses the relative path
	Title       string
	Artist      string // track display artist
	AlbumArtist string // grouping artist; adapters fall back to Artist
	Album       string
	Genre       string
	Year        int
	Index       int
	Disc        int
	DurationMs  int
	Container   string
	Codec       string
	BitrateKbps int
	SizeBytes   int64
	Version     int64  // change marker (filesystem: mtime unix); equal ⇒ unchanged
	ArtHash     string // sha256 hex of embedded art, "" when none
}

// MusicSource is the port every library backend implements.
type MusicSource interface {
	// Kind names the adapter (filesystem, plex, …) for status surfaces.
	Kind() string
	// Scan streams every track currently in the source. emit returning an
	// error aborts the scan.
	Scan(ctx context.Context, emit func(TrackMeta) error) error
	// Open returns the raw audio bytes for streaming/transcoding.
	Open(ctx context.Context, nativeID string) (io.ReadSeekCloser, error)
	// Art returns embedded artwork bytes and MIME type ("" hash in TrackMeta
	// means there is none to fetch).
	Art(ctx context.Context, nativeID string) (data []byte, mime string, err error)
}
