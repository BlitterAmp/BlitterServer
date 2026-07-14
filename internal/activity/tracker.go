// Package activity tracks the current in-memory library pipeline stage.
package activity

import (
	"sync"
	"time"
)

// Stage identifies one externally visible library pipeline stage.
type Stage string

const (
	StageFilesystemScan            Stage = "filesystem_scan"
	StageMusicBrainzResolution     Stage = "musicbrainz_resolution"
	StageMusicBrainzArtistMetadata Stage = "musicbrainz_artist_metadata"
	StageAlbumArtwork              Stage = "album_artwork"
	StageArtistArtwork             Stage = "artist_artwork"
)

// State is the current stage outcome. Idle is represented by a nil snapshot.
type State string

const (
	StateRunning State = "running"
	StateFailed  State = "failed"
)

// Reason is a deliberately small, path-free failure classification.
type Reason string

const ReasonOperationFailed Reason = "operation_failed"

// Counts contains the aggregate counters shared with structured stage logs.
type Counts struct {
	Total      int
	Discovered int
	Reused     int
	Probed     int
	Indexed    int
	Processed  int
	Attempted  int
	Changed    int
	Removed    int
	Succeeded  int
	Skipped    int
	Missed     int
	Transient  int
	Failed     int
	Terminal   int
	Remaining  int
}

// Snapshot is an immutable-by-convention copy of current activity.
type Snapshot struct {
	Stage     Stage
	State     State
	StartedAt time.Time
	UpdatedAt time.Time
	Counts    Counts
	Reason    Reason
}

// Token identifies a tracker generation. Only the newest token may mutate it.
type Token struct{ generation uint64 }

// Tracker stores one process-local activity snapshot.
type Tracker struct {
	mu         sync.RWMutex
	now        func() time.Time
	generation uint64
	snapshot   *Snapshot
}

// New constructs a tracker using the system clock.
func New() *Tracker { return NewWithClock(time.Now) }

// NewWithClock constructs a tracker with an injected clock for tests.
func NewWithClock(now func() time.Time) *Tracker { return &Tracker{now: now} }

// Start replaces current activity and returns its generation token.
func (t *Tracker) Start(stage Stage, counts Counts) Token {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.generation++
	now := t.now().UTC()
	t.snapshot = &Snapshot{Stage: stage, State: StateRunning, StartedAt: now, UpdatedAt: now, Counts: counts}
	return Token{generation: t.generation}
}

// Update replaces counters when token still owns current activity.
func (t *Tracker) Update(token Token, counts Counts) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.current(token) {
		return
	}
	t.snapshot.Counts = counts
	t.snapshot.UpdatedAt = t.now().UTC()
}

// Fail retains a path-free failed snapshot until superseded or finished.
func (t *Tracker) Fail(token Token, counts Counts) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.current(token) {
		return
	}
	t.snapshot.State = StateFailed
	t.snapshot.Counts = counts
	t.snapshot.Reason = ReasonOperationFailed
	t.snapshot.UpdatedAt = t.now().UTC()
}

// Finish clears activity only when token still owns current activity.
func (t *Tracker) Finish(token Token) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current(token) {
		t.snapshot = nil
	}
}

// RetainFailure restores an earlier failed stage if token still owns current
// activity. Pipelines can continue useful later stages without losing the
// failure that must remain visible when the overall run ends.
func (t *Tracker) RetainFailure(token Token, failed Snapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.current(token) {
		return
	}
	failed.State = StateFailed
	failed.Reason = ReasonOperationFailed
	failed.UpdatedAt = t.now().UTC()
	t.snapshot = &failed
}

// Snapshot returns a copy safe for concurrent callers to inspect.
func (t *Tracker) Snapshot() *Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.snapshot == nil {
		return nil
	}
	copy := *t.snapshot
	return &copy
}

func (t *Tracker) current(token Token) bool {
	return t.snapshot != nil && token.generation != 0 && token.generation == t.generation
}
