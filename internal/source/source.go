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
	Name       string `json:"name"`
	JoinPhrase string `json:"joinPhrase"`
	MBID       string `json:"mbid"`
}

// TrackCandidate is the cheap filesystem identity used to decide whether a
// track needs parsing. Enumerating candidates must not open media files.
type TrackCandidate struct {
	NativeID  string `json:"nativeId"`
	SizeBytes int64  `json:"sizeBytes"`
	MtimeNS   int64  `json:"mtimeNs"`
}

// TrackMeta is one track's identity, tags, and media info as reported by a
// source during a scan.
type TrackMeta struct {
	NativeID         string          `json:"nativeId"` // stable within the source; filesystem uses the relative path
	Title            string          `json:"title"`
	PrimaryArtist    ArtistReference `json:"primaryArtist"` // grouping artist; adapters fall back to the first track credit
	TrackCredits     []ArtistCredit  `json:"trackCredits"`
	AlbumCredits     []ArtistCredit  `json:"albumCredits"`
	Album            string          `json:"album"`
	RecordingMBID    string          `json:"recordingMbid"`
	ReleaseMBID      string          `json:"releaseMbid"`
	ReleaseGroupMBID string          `json:"releaseGroupMbid"`
	Genre            string          `json:"genre"`
	Year             int             `json:"year"`
	Index            int             `json:"index"`
	Disc             int             `json:"disc"`
	DurationMs       int             `json:"durationMs"`
	Container        string          `json:"container"`
	Codec            string          `json:"codec"`
	BitrateKbps      int             `json:"bitrateKbps"`
	SizeBytes        int64           `json:"sizeBytes"`
	Version          int64           `json:"version"` // source revision, distinct from library scan sequence
	ArtHash          string          `json:"artHash"` // sha256 hex of embedded art, "" when none
}

// ArtistReference identifies the artist used to group an album.
type ArtistReference struct {
	Name string `json:"name"`
	MBID string `json:"mbid"`
}

// MusicSource is the port every library backend implements.
type MusicSource interface {
	// Kind names the adapter (filesystem, plex, …) for status surfaces.
	Kind() string
	// ParserVersion invalidates persisted raw metadata when parsing changes.
	ParserVersion() int
	// Enumerate streams cheap fingerprints without opening media files.
	Enumerate(ctx context.Context, emit func(TrackCandidate) error) error
	// Parse performs expensive probing and tag reads for one stable candidate.
	Parse(ctx context.Context, candidate TrackCandidate) (TrackMeta, error)
	// Open returns the raw audio bytes for streaming/transcoding.
	Open(ctx context.Context, nativeID string) (io.ReadSeekCloser, error)
	// Art returns fingerprint-stable embedded artwork matching expectedHash.
	Art(ctx context.Context, candidate TrackCandidate, expectedHash string) (data []byte, mime string, err error)
}
