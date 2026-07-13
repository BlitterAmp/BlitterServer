// Package source defines the MusicSource port: what any library backend
// (filesystem, Plex, …) must provide. Domain code depends on this interface,
// never on an adapter.
package source

import (
	"context"
	"io"
)

// ArtistCredit preserves one credited artist and the text joining it to the next credit.
type ArtistCredit struct {
	Name       string
	JoinPhrase string
	MBID       string
}

// TrackMeta is one track's identity, tags, and media info as reported by a
// source during a scan.
type TrackMeta struct {
	NativeID         string // stable within the source; filesystem uses the relative path
	Title            string
	PrimaryArtist    ArtistReference // grouping artist; adapters fall back to the first track credit
	TrackCredits     []ArtistCredit
	AlbumCredits     []ArtistCredit
	Album            string
	RecordingMBID    string
	ReleaseMBID      string
	ReleaseGroupMBID string
	Genre            string
	Year             int
	Index            int
	Disc             int
	DurationMs       int
	Container        string
	Codec            string
	BitrateKbps      int
	SizeBytes        int64
	Version          int64  // change marker (filesystem: mtime unix); equal ⇒ unchanged
	ArtHash          string // sha256 hex of embedded art, "" when none
}

// ArtistReference identifies the artist used to group an album.
type ArtistReference struct {
	Name string
	MBID string
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
