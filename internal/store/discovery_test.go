package store

import (
	"context"
	"testing"
	"time"
)

// discoveryFixture: 4 tracks, plays/ratings/loves for profile 1.
func discoveryFixture(t *testing.T) (*Store, string, []TrackRow) {
	t.Helper()
	s, p1, _, tracks := dataFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// tracks[0]: played recently + rated 9 + loved.
	s.IngestPlaybackEvents(ctx, p1.ProfileID, "d", []PlaybackEventRecord{
		{EventID: "d1", Type: "ended", TrackID: tracks[0].TrackID, At: now.Add(-1 * time.Hour)},
		{EventID: "d2", Type: "ended", TrackID: tracks[0].TrackID, At: now.Add(-2 * time.Hour)},
	})
	s.SetRating(ctx, p1.ProfileID, "track", tracks[0].TrackID, 9)
	s.SetLove(ctx, p1.ProfileID, tracks[0].TrackID, "loved")

	// tracks[1]: played long ago (rediscover material).
	s.IngestPlaybackEvents(ctx, p1.ProfileID, "d", []PlaybackEventRecord{
		{EventID: "d3", Type: "ended", TrackID: tracks[1].TrackID, At: now.Add(-90 * 24 * time.Hour)},
	})

	// tracks[3]: not_for_me — must never surface in generated content.
	s.SetLove(ctx, p1.ProfileID, tracks[3].TrackID, "not_for_me")

	return s, p1.ProfileID, tracks
}

func trackIDs(rows []TrackRow) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		out[r.TrackID] = true
	}
	return out
}

func TestRecentlyPlayedAndAdded(t *testing.T) {
	s, p1, tracks := discoveryFixture(t)
	ctx := context.Background()

	recent, err := s.RecentlyPlayedTracks(ctx, p1, 10)
	if err != nil || len(recent) != 2 || recent[0].TrackID != tracks[0].TrackID {
		t.Fatalf("recently played: %v %+v", err, recent)
	}
	albums, err := s.RecentlyAddedAlbums(ctx, 10)
	if err != nil || len(albums) != 3 {
		t.Fatalf("recently added: %v %d", err, len(albums))
	}
}

func TestMixes(t *testing.T) {
	s, p1, tracks := discoveryFixture(t)
	ctx := context.Background()

	mixes, err := s.AvailableMixes(ctx, p1)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, m := range mixes {
		kinds[m.Kind] = m.MixID
	}
	for _, want := range []string{"forYou", "topRated", "heavyRotation", "rediscover", "genre"} {
		if _, ok := kinds[want]; !ok {
			t.Fatalf("missing mix kind %s: %+v", want, mixes)
		}
	}

	top, err := s.MixTracks(ctx, p1, "topRated")
	if err != nil || len(top) != 1 || top[0].TrackID != tracks[0].TrackID {
		t.Fatalf("topRated: %v %+v", err, top)
	}
	redis, err := s.MixTracks(ctx, p1, "rediscover")
	if err != nil || len(redis) != 1 || redis[0].TrackID != tracks[1].TrackID {
		t.Fatalf("rediscover: %v %+v", err, redis)
	}
	heavy, err := s.MixTracks(ctx, p1, "heavyRotation")
	if err != nil || len(heavy) != 1 || heavy[0].TrackID != tracks[0].TrackID {
		t.Fatalf("heavyRotation: %v %+v", err, heavy)
	}
	forYou, err := s.MixTracks(ctx, p1, "forYou")
	if err != nil || len(forYou) == 0 {
		t.Fatalf("forYou: %v %+v", err, forYou)
	}
	if ids := trackIDs(forYou); ids[tracks[3].TrackID] {
		t.Fatal("not_for_me leaked into forYou")
	}
	genreMix, err := s.MixTracks(ctx, p1, "genre:Rock")
	if err != nil || len(genreMix) == 0 {
		t.Fatalf("genre mix: %v %+v", err, genreMix)
	}
	if _, err := s.MixTracks(ctx, p1, "genre:Nope"); err == nil {
		t.Fatal("unknown genre mix must error")
	}
	if _, err := s.MixTracks(ctx, p1, "bogus"); err == nil {
		t.Fatal("unknown mix must error")
	}
}

func TestRadioNext(t *testing.T) {
	s, p1, tracks := discoveryFixture(t)
	ctx := context.Background()

	// Seeded by tracks[0]'s artist; excludes the seed itself and not_for_me.
	rows, err := s.RadioNext(ctx, p1, []string{tracks[0].ArtistID}, []string{tracks[0].TrackID}, 10)
	if err != nil || len(rows) == 0 {
		t.Fatalf("radio: %v %+v", err, rows)
	}
	ids := trackIDs(rows)
	if ids[tracks[0].TrackID] {
		t.Fatal("excluded seed leaked into radio")
	}
	if ids[tracks[3].TrackID] {
		t.Fatal("not_for_me leaked into radio")
	}
	// Count cap respected.
	one, _ := s.RadioNext(ctx, p1, nil, nil, 1)
	if len(one) != 1 {
		t.Fatalf("count cap: %+v", one)
	}
}
