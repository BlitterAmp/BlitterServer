package store

import (
	"context"
	"errors"
	"testing"
)

func TestArtifactDedupeAndLifecycle(t *testing.T) {
	s, _, _, tracks := dataFixture(t)
	ctx := context.Background()
	tr := tracks[0]

	a, created, err := s.UpsertArtifact(ctx, tr.TrackID, "aac", 256)
	if err != nil || !created || a.Status != "queued" || a.TrackID != tr.TrackID {
		t.Fatalf("create: %v %v %+v", err, created, a)
	}
	// Idempotent re-request returns the same artifact.
	b, created, err := s.UpsertArtifact(ctx, tr.TrackID, "aac", 256)
	if err != nil || created || b.ArtifactID != a.ArtifactID {
		t.Fatalf("dedupe: %v %v %q vs %q", err, created, b.ArtifactID, a.ArtifactID)
	}
	// Different bitrate is a different artifact.
	c, _, _ := s.UpsertArtifact(ctx, tr.TrackID, "aac", 128)
	if c.ArtifactID == a.ArtifactID {
		t.Fatal("bitrate must partition artifacts")
	}
	// Unknown track 404s.
	if _, _, err := s.UpsertArtifact(ctx, "trk_nope", "aac", 256); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown track: %v", err)
	}

	// Status transitions persist bytes/path/error.
	if err := s.MarkArtifactProcessing(ctx, a.ArtifactID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkArtifactReady(ctx, a.ArtifactID, 4321, "/cache/a.m4a"); err != nil {
		t.Fatal(err)
	}
	got, found, _ := s.GetArtifact(ctx, a.ArtifactID)
	if !found || got.Status != "ready" || got.Bytes != 4321 || got.Path != "/cache/a.m4a" {
		t.Fatalf("ready: %+v", got)
	}
	if err := s.MarkArtifactFailed(ctx, c.ArtifactID, "boom"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetArtifact(ctx, c.ArtifactID)
	if got.Status != "failed" || got.Error != "boom" {
		t.Fatalf("failed: %+v", got)
	}

	// Queue drains oldest-first.
	d, _, _ := s.UpsertArtifact(ctx, tracks[1].TrackID, "aac", 256)
	queued, err := s.NextQueuedArtifact(ctx)
	if err != nil || queued == nil || queued.ArtifactID != d.ArtifactID {
		t.Fatalf("queue: %v %+v", err, queued)
	}
	s.MarkArtifactProcessing(ctx, d.ArtifactID)
	if q, _ := s.NextQueuedArtifact(ctx); q != nil {
		t.Fatalf("empty queue must be nil: %+v", q)
	}
}

func TestArtifactOriginalReadyImmediately(t *testing.T) {
	s, _, _, tracks := dataFixture(t)
	ctx := context.Background()
	a, created, err := s.UpsertArtifact(ctx, tracks[0].TrackID, "original", 0)
	if err != nil || !created || a.Status != "ready" {
		t.Fatalf("original: %v %+v", err, a)
	}
	if a.Bytes != tracks[0].SizeBytes || a.AlbumID != tracks[0].AlbumID {
		t.Fatalf("original bytes/album: %+v vs track %+v", a, tracks[0])
	}
}

func TestArtifactReleaseAndEviction(t *testing.T) {
	s, _, _, tracks := dataFixture(t)
	ctx := context.Background()

	a, _, _ := s.UpsertArtifact(ctx, tracks[0].TrackID, "aac", 256)
	b, _, _ := s.UpsertArtifact(ctx, tracks[1].TrackID, "aac", 256)
	s.MarkArtifactReady(ctx, a.ArtifactID, 6000, "/cache/a.m4a")
	s.MarkArtifactReady(ctx, b.ArtifactID, 6000, "/cache/b.m4a")

	if err := s.ReleaseArtifact(ctx, a.ArtifactID); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.ArtifactCacheUsage(ctx); used != 12000 {
		t.Fatalf("usage: %d", used)
	}
	// Eviction candidates: released first.
	victims, err := s.ArtifactEvictionCandidates(ctx, 12000-6000) // need to free 6000
	if err != nil || len(victims) != 1 || victims[0].ArtifactID != a.ArtifactID {
		t.Fatalf("victims: %v %+v", err, victims)
	}
	if err := s.DeleteArtifact(ctx, a.ArtifactID); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.GetArtifact(ctx, a.ArtifactID); found {
		t.Fatal("evicted artifact must be gone")
	}
}
