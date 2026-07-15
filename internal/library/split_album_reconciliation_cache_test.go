package library

import (
	"context"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func TestSplitAlbumReconciliationSurvivesCachedAndChangedSourceScans(t *testing.T) {
	anchorCandidate := source.TrackCandidate{NativeID: "anchor.flac", SizeBytes: 100, MtimeNS: 1000}
	fragmentCandidate := source.TrackCandidate{NativeID: "fragment.flac", SizeBytes: 200, MtimeNS: 2000}
	s, manager, fake, _ := incrementalManager(t, anchorCandidate, fragmentCandidate)
	fake.metadata[anchorCandidate.NativeID] = splitCacheMeta(anchorCandidate, "Canonical Owner", 1)
	fake.metadata[fragmentCandidate.NativeID] = splitCacheMeta(fragmentCandidate, "Canonical Owner feat. Guest", 2)
	rescanAndWait(t, manager)
	if parse, _ := fake.counts(); parse != 2 {
		t.Fatalf("initial parse calls=%d want=2", parse)
	}

	ctx := context.Background()
	due, err := s.DueMusicBrainzAlbums(ctx, time.Now(), 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("split candidates=%+v err=%v", due, err)
	}
	var anchor store.MusicBrainzAlbum
	for _, album := range due {
		if album.PrimaryArtist.Name == "Canonical Owner" {
			anchor = album
		}
	}
	if anchor.AlbumID == "" {
		t.Fatal("canonical anchor not found")
	}
	release := store.CanonicalRelease{
		ReleaseID: "cache-release", ReleaseGroupID: "cache-group",
		AlbumCredits: []source.ArtistCredit{{Name: "Canonical Owner", MBID: "canonical-owner-mbid"}},
		Tracks: []store.CanonicalTrack{
			{Disc: 1, Index: 1, Title: "Track 1", DurationMs: 180001, RecordingID: "recording-1"},
			{Disc: 1, Index: 2, Title: "Track 2", DurationMs: 180002, RecordingID: "recording-2"},
		},
	}
	seq, _ := s.NextScanSeq(ctx)
	changed, err := s.ApplyMusicBrainzMatch(ctx, anchor, release, seq, store.MusicBrainzCandidate{ReleaseID: release.ReleaseID, ReleaseGroupID: release.ReleaseGroupID, Score: 100}, nil, time.Now().Add(7*24*time.Hour))
	if err != nil || !changed {
		t.Fatalf("reconcile changed=%v err=%v", changed, err)
	}
	albums, _, err := s.ListAlbums(ctx, "title", "", 10)
	if err != nil || len(albums) != 1 {
		t.Fatalf("after reconcile albums=%+v err=%v", albums, err)
	}
	survivorID := albums[0].AlbumID

	rescanAndWait(t, manager)
	if parse, _ := fake.counts(); parse != 2 {
		t.Fatalf("unchanged cache reparsed fragment: calls=%d want=2", parse)
	}
	albums, _, _ = s.ListAlbums(ctx, "title", "", 10)
	if len(albums) != 1 || albums[0].AlbumID != survivorID {
		t.Fatalf("unchanged cache recreated fragment: %+v", albums)
	}

	fake.mu.Lock()
	fake.candidates[1].MtimeNS++
	changedCandidate := fake.candidates[1]
	fake.metadata[changedCandidate.NativeID] = splitCacheMeta(changedCandidate, "Canonical Owner feat. Guest", 2)
	fake.mu.Unlock()
	rescanAndWait(t, manager)
	if parse, _ := fake.counts(); parse != 3 {
		t.Fatalf("changed fragment parse calls=%d want=3", parse)
	}
	albums, _, _ = s.ListAlbums(ctx, "title", "", 10)
	if len(albums) != 1 || albums[0].AlbumID != survivorID {
		t.Fatalf("changed non-authoritative file recreated fragment: %+v", albums)
	}
	tracks, err := s.ListAlbumTracks(ctx, survivorID)
	if err != nil || len(tracks) != 2 {
		t.Fatalf("changed file detached from survivor: tracks=%+v err=%v", tracks, err)
	}
}

func splitCacheMeta(candidate source.TrackCandidate, owner string, position int) source.TrackMeta {
	return source.TrackMeta{
		NativeID: candidate.NativeID, Title: "Track " + string(rune('0'+position)), Album: "Cached Split Album", Year: 2020,
		Disc: 1, Index: position, DurationMs: 180000 + position, PrimaryArtist: source.ArtistReference{Name: owner},
		AlbumCredits: []source.ArtistCredit{{Name: owner}}, TrackCredits: []source.ArtistCredit{{Name: owner}},
		Container: "flac", Codec: "flac", SizeBytes: candidate.SizeBytes, Version: candidate.MtimeNS,
	}
}
