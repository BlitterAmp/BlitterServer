package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// dataFixture: a library plus two profiles.
func dataFixture(t *testing.T) (*Store, ProfileRecord, ProfileRecord, []TrackRow) {
	t.Helper()
	s := indexFixture(t) // 4 tracks, 2 artists (library_test.go)
	ctx := context.Background()
	p1, err := s.CreateProfileRecord(ctx, "Nathan", "", "")
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := s.CreateProfileRecord(ctx, "Kid", "", "")
	tracks, _, err := s.ListTracks(ctx, "title", "", 100)
	if err != nil || len(tracks) != 4 {
		t.Fatalf("fixture tracks: %v %d", err, len(tracks))
	}
	return s, p1, p2, tracks
}

// ── playlists ──────────────────────────────────────────────────

func TestPlaylistLifecycle(t *testing.T) {
	s, p1, p2, tracks := dataFixture(t)
	ctx := context.Background()

	pl, err := s.CreatePlaylist(ctx, p1.ProfileID, "Road Trip", "private", []string{tracks[0].TrackID, tracks[1].TrackID})
	if err != nil || pl.Title != "Road Trip" || pl.TrackCount != 2 || pl.OwnerProfileID != p1.ProfileID {
		t.Fatalf("create: %v %+v", err, pl)
	}

	// Owner sees it; the other profile does not (private).
	mine, _ := s.ListPlaylists(ctx, p1.ProfileID)
	if len(mine) != 1 {
		t.Fatalf("owner list: %+v", mine)
	}
	theirs, _ := s.ListPlaylists(ctx, p2.ProfileID)
	if len(theirs) != 0 {
		t.Fatalf("private playlist leaked: %+v", theirs)
	}

	// Shared becomes visible to the household.
	if _, err := s.UpdatePlaylist(ctx, pl.PlaylistID, str("Trip"), str("shared")); err != nil {
		t.Fatal(err)
	}
	theirs, _ = s.ListPlaylists(ctx, p2.ProfileID)
	if len(theirs) != 1 || theirs[0].Title != "Trip" || theirs[0].OwnerName != "Nathan" {
		t.Fatalf("shared visibility: %+v", theirs)
	}

	items, next, err := s.ListPlaylistItems(ctx, pl.PlaylistID, "", 1)
	if err != nil || len(items) != 1 || next == "" {
		t.Fatalf("items page1: %v %d %q", err, len(items), next)
	}
	items2, next2, _ := s.ListPlaylistItems(ctx, pl.PlaylistID, next, 10)
	if len(items2) != 1 || next2 != "" || items2[0].ItemID == items[0].ItemID {
		t.Fatalf("items page2: %+v %q", items2, next2)
	}

	if err := s.AppendPlaylistTracks(ctx, pl.PlaylistID, []string{tracks[2].TrackID}); err != nil {
		t.Fatal(err)
	}
	all, _, _ := s.ListPlaylistItems(ctx, pl.PlaylistID, "", 100)
	if len(all) != 3 || all[2].Track.TrackID != tracks[2].TrackID {
		t.Fatalf("append order: %+v", all)
	}
	if err := s.RemovePlaylistItem(ctx, pl.PlaylistID, all[0].ItemID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePlaylistItem(ctx, pl.PlaylistID, all[0].ItemID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double remove: want ErrNotFound, got %v", err)
	}

	got, found, _ := s.GetPlaylist(ctx, pl.PlaylistID)
	if !found || got.TrackCount != 2 || got.DurationMs != 4000 {
		t.Fatalf("get after mutations: %+v", got)
	}

	if err := s.DeletePlaylist(ctx, pl.PlaylistID); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.GetPlaylist(ctx, pl.PlaylistID); found {
		t.Fatal("deleted playlist must vanish")
	}
}

// ── loves ──────────────────────────────────────────────────────

func TestLoveTriState(t *testing.T) {
	s, p1, p2, tracks := dataFixture(t)
	ctx := context.Background()

	rec, err := s.SetLove(ctx, p1.ProfileID, tracks[0].TrackID, "loved")
	if err != nil || rec.State != "loved" || rec.Kind != "track" || rec.Name != tracks[0].Title || !rec.Owned {
		t.Fatalf("love: %v %+v", err, rec)
	}
	// Artist love by prefix.
	arec, err := s.SetLove(ctx, p1.ProfileID, tracks[0].ArtistID, "loved")
	if err != nil || arec.Kind != "artist" {
		t.Fatalf("artist love: %v %+v", err, arec)
	}
	// not_for_me replaces loved (idempotent upsert).
	rec, _ = s.SetLove(ctx, p1.ProfileID, tracks[0].TrackID, "not_for_me")
	if rec.State != "not_for_me" {
		t.Fatalf("flip: %+v", rec)
	}
	// neutral deletes.
	rec, err = s.SetLove(ctx, p1.ProfileID, tracks[0].TrackID, "neutral")
	if err != nil || rec.State != "neutral" {
		t.Fatalf("neutral: %v %+v", err, rec)
	}
	list, _, _ := s.ListLoves(ctx, p1.ProfileID, "", "", "", 100)
	if len(list) != 1 || list[0].Ref != tracks[0].ArtistID {
		t.Fatalf("neutral must not persist: %+v", list)
	}
	// Unknown ref 404s; other profile unaffected.
	if _, err := s.SetLove(ctx, p1.ProfileID, "trk_nope", "loved"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown ref: %v", err)
	}
	if l2, _, _ := s.ListLoves(ctx, p2.ProfileID, "", "", "", 100); len(l2) != 0 {
		t.Fatalf("cross-profile leak: %+v", l2)
	}

	// Filters.
	s.SetLove(ctx, p1.ProfileID, tracks[1].TrackID, "not_for_me")
	onlyLoved, _, _ := s.ListLoves(ctx, p1.ProfileID, "", "loved", "", 100)
	if len(onlyLoved) != 1 || onlyLoved[0].State != "loved" {
		t.Fatalf("state filter: %+v", onlyLoved)
	}
	onlyTracks, _, _ := s.ListLoves(ctx, p1.ProfileID, "track", "", "", 100)
	if len(onlyTracks) != 1 || onlyTracks[0].Kind != "track" {
		t.Fatalf("kind filter: %+v", onlyTracks)
	}

	// Batch decoration.
	states, err := s.GetLoveStates(ctx, p1.ProfileID, []string{tracks[0].TrackID, tracks[1].TrackID, tracks[0].ArtistID})
	if err != nil || states[tracks[1].TrackID] != "not_for_me" || states[tracks[0].ArtistID] != "loved" || states[tracks[0].TrackID] != "" {
		t.Fatalf("states: %v %+v", err, states)
	}
}

// ── ratings ────────────────────────────────────────────────────

func TestRatings(t *testing.T) {
	s, p1, _, tracks := dataFixture(t)
	ctx := context.Background()

	if err := s.SetRating(ctx, p1.ProfileID, "track", tracks[0].TrackID, 8); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRatings(ctx, p1.ProfileID, []string{tracks[0].TrackID, tracks[1].TrackID})
	if err != nil || got[tracks[0].TrackID] != 8 || got[tracks[1].TrackID] != 0 {
		t.Fatalf("ratings: %v %+v", err, got)
	}
	// Clear via rating 0... explicit clear API:
	if err := s.ClearRating(ctx, p1.ProfileID, tracks[0].TrackID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetRatings(ctx, p1.ProfileID, []string{tracks[0].TrackID})
	if got[tracks[0].TrackID] != 0 {
		t.Fatalf("clear: %+v", got)
	}
	if err := s.SetRating(ctx, p1.ProfileID, "track", "trk_nope", 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown item: %v", err)
	}
}

// ── playback + presence + taste ────────────────────────────────

func TestPlaybackIngestionAndPresence(t *testing.T) {
	s, p1, p2, tracks := dataFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()

	n, err := s.IngestPlaybackEvents(ctx, p1.ProfileID, "dev_x", []PlaybackEventRecord{
		{EventID: "e1", Type: "started", TrackID: tracks[0].TrackID, At: now.Add(-2 * time.Minute)},
		{EventID: "e2", Type: "ended", TrackID: tracks[0].TrackID, At: now.Add(-1 * time.Minute)},
		{EventID: "e3", Type: "started", TrackID: tracks[1].TrackID, At: now},
		{EventID: "bogus", Type: "started", TrackID: "trk_nope", At: now},
	})
	if err != nil || n != 3 {
		t.Fatalf("ingest: %v accepted=%d (unknown tracks skipped)", err, n)
	}
	// Duplicate flush is a no-op.
	n, err = s.IngestPlaybackEvents(ctx, p1.ProfileID, "dev_x", []PlaybackEventRecord{
		{EventID: "e3", Type: "started", TrackID: tracks[1].TrackID, At: now},
	})
	if err != nil || n != 0 {
		t.Fatalf("dedupe: %v %d", err, n)
	}

	entries, err := s.ListPresence(ctx)
	if err != nil || len(entries) != 1 || entries[0].ProfileID != p1.ProfileID || entries[0].Track.TrackID != tracks[1].TrackID {
		t.Fatalf("presence: %v %+v", err, entries)
	}

	// shareListening=false hides the profile.
	s.SetShareListening(ctx, p1.ProfileID, false)
	if entries, _ := s.ListPresence(ctx); len(entries) != 0 {
		t.Fatalf("presence must honor shareListening: %+v", entries)
	}
	s.SetShareListening(ctx, p1.ProfileID, true)

	// Stopping playback clears presence.
	s.IngestPlaybackEvents(ctx, p1.ProfileID, "dev_x", []PlaybackEventRecord{
		{EventID: "e4", Type: "paused", TrackID: tracks[1].TrackID, At: now.Add(time.Second)},
	})
	if entries, _ := s.ListPresence(ctx); len(entries) != 0 {
		t.Fatalf("paused must clear presence: %+v", entries)
	}

	// Taste snapshot: one completed play, no skips; profile-scoped.
	snap, err := s.TasteSnapshot(ctx, p1.ProfileID)
	if err != nil || len(snap.Tracks) != 1 || snap.Tracks[0].Plays != 1 {
		t.Fatalf("taste: %v %+v", err, snap)
	}
	if len(snap.Artists) == 0 || snap.Artists[0].Affinity <= 0 {
		t.Fatalf("artist affinity: %+v", snap.Artists)
	}
	if snap2, _ := s.TasteSnapshot(ctx, p2.ProfileID); len(snap2.Tracks) != 0 {
		t.Fatalf("cross-profile taste: %+v", snap2)
	}
}

// ── recommendations ────────────────────────────────────────────

func TestRecommendationsInbox(t *testing.T) {
	s, p1, p2, tracks := dataFixture(t)
	ctx := context.Background()

	rec, err := s.CreateRecommendation(ctx, p1.ProfileID, p2.ProfileID, tracks[0].AlbumID, "give it a spin")
	if err != nil || rec.Kind != "album" || rec.FromProfileName != "Nathan" || rec.Seen {
		t.Fatalf("create: %v %+v", err, rec)
	}
	if _, err := s.CreateRecommendation(ctx, p1.ProfileID, "prf_nope", tracks[0].TrackID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown target: %v", err)
	}
	if _, err := s.CreateRecommendation(ctx, p1.ProfileID, p2.ProfileID, "alb_nope", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown ref: %v", err)
	}

	inbox, _, err := s.ListRecommendations(ctx, p2.ProfileID, false, "", 100)
	if err != nil || len(inbox) != 1 || inbox[0].Name != tracks[0].AlbumTitle {
		t.Fatalf("inbox: %v %+v", err, inbox)
	}
	if senderInbox, _, _ := s.ListRecommendations(ctx, p1.ProfileID, false, "", 100); len(senderInbox) != 0 {
		t.Fatalf("sender must not see it in their inbox: %+v", senderInbox)
	}

	if err := s.MarkRecommendationSeen(ctx, p2.ProfileID, rec.RecommendationID); err != nil {
		t.Fatal(err)
	}
	unseen, _, _ := s.ListRecommendations(ctx, p2.ProfileID, true, "", 100)
	if len(unseen) != 0 {
		t.Fatalf("seen filter: %+v", unseen)
	}
	// Wrong profile can't mark someone else's.
	if err := s.MarkRecommendationSeen(ctx, p1.ProfileID, rec.RecommendationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-profile seen: %v", err)
	}
}

// ── event log ──────────────────────────────────────────────────

func TestEventLogAppendAndRead(t *testing.T) {
	s, p1, p2, _ := dataFixture(t)
	ctx := context.Background()

	seq1, err := s.AppendEvent(ctx, "library.changed", "", `{"libraryId":"lib_local"}`)
	if err != nil || seq1 == 0 {
		t.Fatalf("append: %v %d", err, seq1)
	}
	seq2, _ := s.AppendEvent(ctx, "love.updated", p1.ProfileID, `{}`)
	if seq2 <= seq1 {
		t.Fatalf("monotonic: %d then %d", seq1, seq2)
	}

	// p2 sees instance events but not p1's profile events.
	evs, err := s.EventsSince(ctx, p2.ProfileID, 0, 100)
	if err != nil || len(evs) != 1 || evs[0].Type != "library.changed" {
		t.Fatalf("p2 events: %v %+v", err, evs)
	}
	evs, _ = s.EventsSince(ctx, p1.ProfileID, 0, 100)
	if len(evs) != 2 {
		t.Fatalf("p1 events: %+v", evs)
	}
	evs, _ = s.EventsSince(ctx, p1.ProfileID, seq1, 100)
	if len(evs) != 1 || evs[0].Seq != seq2 {
		t.Fatalf("resume: %+v", evs)
	}
}
