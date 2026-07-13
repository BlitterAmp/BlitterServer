package store

import (
	"context"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"testing"
	"time"
)

func TestArtworkAttemptMarkingMatrix(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		artist    bool
		fallback  bool
		priorMiss int
		outcome   ArtAttemptOutcome
		wantMiss  int
		wantNext  time.Time
	}{
		{name: "album first miss", outcome: ArtAttemptMiss, wantMiss: 1, wantNext: now.Add(7 * 24 * time.Hour)},
		{name: "album repeat miss", priorMiss: 1, outcome: ArtAttemptMiss, wantMiss: 2, wantNext: now.Add(30 * 24 * time.Hour)},
		{name: "album transient preserves first tier", priorMiss: 1, outcome: ArtAttemptTransient, wantMiss: 1, wantNext: now.Add(24 * time.Hour)},
		{name: "artist first miss", artist: true, outcome: ArtAttemptMiss, wantMiss: 1, wantNext: now.Add(7 * 24 * time.Hour)},
		{name: "artist repeat miss", artist: true, priorMiss: 1, outcome: ArtAttemptMiss, wantMiss: 2, wantNext: now.Add(30 * 24 * time.Hour)},
		{name: "artist transient preserves tier", artist: true, priorMiss: 2, outcome: ArtAttemptTransient, wantMiss: 2, wantNext: now.Add(24 * time.Hour)},
		{name: "artist fallback miss uses upgrade window", artist: true, fallback: true, outcome: ArtAttemptMiss, wantMiss: 1, wantNext: now.Add(30 * 24 * time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := indexFixture(t)
			ctx := context.Background()
			table := "albums"
			idColumn := "album_id"
			needs, err := s.AlbumsNeedingArtAt(ctx, now, 1)
			if err != nil || len(needs) != 1 {
				t.Fatalf("album need: %+v, %v", needs, err)
			}
			id := needs[0].AlbumID
			if tt.artist {
				table, idColumn = "artists", "artist_id"
				// Fanart selection requires an MBID; identify the fixture artists.
				if _, err := s.db.ExecContext(ctx, `UPDATE artists SET musicbrainz_id='mbid-' || artist_id`); err != nil {
					t.Fatal(err)
				}
				artists, err := s.ArtistsNeedingArtAt(ctx, now, 1)
				if err != nil || len(artists) != 1 {
					t.Fatalf("artist need: %+v, %v", artists, err)
				}
				id = artists[0].ArtistID
				if tt.fallback {
					if _, err := s.db.ExecContext(ctx, `UPDATE artists SET art_id='img_album_fallback' WHERE artist_id=?`, id); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE `+table+` SET art_miss_count=? WHERE `+idColumn+`=?`, tt.priorMiss, id); err != nil {
				t.Fatal(err)
			}
			if tt.artist {
				err = s.MarkArtistArtAttempt(ctx, id, tt.outcome, now)
			} else {
				err = s.MarkAlbumArtAttempt(ctx, id, tt.outcome, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			var miss int
			var next int64
			if err := s.db.QueryRowContext(ctx, `SELECT art_miss_count, art_next_attempt_at FROM `+table+` WHERE `+idColumn+`=?`, id).Scan(&miss, &next); err != nil {
				t.Fatal(err)
			}
			if miss != tt.wantMiss || next != tt.wantNext.Unix() {
				t.Fatalf("state=(miss=%d next=%s), want (miss=%d next=%s)", miss, time.Unix(next, 0), tt.wantMiss, tt.wantNext)
			}
		})
	}
}

func TestArtworkDueWindowUsesExplicitTime(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	albums, err := s.AlbumsNeedingArtAt(ctx, now, 1)
	if err != nil || len(albums) != 1 {
		t.Fatalf("initial album: %+v, %v", albums, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE albums SET art_next_attempt_at=?`, now.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE albums SET art_next_attempt_at=? WHERE album_id=?`, now.Add(time.Second).Unix(), albums[0].AlbumID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.AlbumsNeedingArtAt(ctx, now, 10); err != nil || len(got) != 0 {
		t.Fatalf("not-yet-due albums=%+v err=%v", got, err)
	}
	if got, err := s.AlbumsNeedingArtAt(ctx, now.Add(time.Second), 10); err != nil || len(got) != 1 {
		t.Fatalf("boundary-due albums=%+v err=%v", got, err)
	}
}

func TestResetArtRetriesIncludesArtistAlbumFallback(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
	// Fanart selection requires MBIDs; identify the fixture artists.
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET musicbrainz_id='mbid-' || artist_id`); err != nil {
		t.Fatal(err)
	}
	artists, err := s.ArtistsNeedingArtAt(ctx, time.Now(), 1)
	if err != nil || len(artists) != 1 {
		t.Fatalf("artist need: %+v, %v", artists, err)
	}
	id := artists[0].ArtistID
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET art_id='img_album_fallback', art_miss_count=3, art_next_attempt_at=? WHERE artist_id=?`, time.Now().Add(30*24*time.Hour).Unix(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetArtRetries(ctx, true); err != nil {
		t.Fatal(err)
	}
	var misses int
	var next int64
	if err := s.db.QueryRowContext(ctx, `SELECT art_miss_count, art_next_attempt_at FROM artists WHERE artist_id=?`, id).Scan(&misses, &next); err != nil {
		t.Fatal(err)
	}
	if misses != 0 || next != 0 {
		t.Fatalf("forced retry left miss=%d next=%d", misses, next)
	}
}

// A definitive art miss before identity resolution must not block the
// post-resolution art fetch: gaining a release group resets eligibility.
func TestMatchApplyResetsAlbumArtRetryState(t *testing.T) {
	s, album := musicBrainzAlbumFixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.MarkAlbumArtAttempt(ctx, album.AlbumID, ArtAttemptMiss, now); err != nil {
		t.Fatal(err)
	}
	if need, err := s.AlbumsNeedingArtAt(ctx, now, 10); err != nil || len(need) != 0 {
		t.Fatalf("marked album still eligible: %v err=%v", need, err)
	}
	applyCanonicalRelease(t, s, album, "Local Artist", "mbid-local-artist", []source.ArtistCredit{{Name: "Local Artist", MBID: "mbid-local-artist"}})
	need, err := s.AlbumsNeedingArtAt(ctx, now, 10)
	if err != nil || len(need) != 1 || need[0].AlbumID != album.AlbumID {
		t.Fatalf("album must be art-eligible again after gaining identity: %v err=%v", need, err)
	}
	misses, next, err := s.ArtRetryState(ctx, false, album.AlbumID)
	if err != nil || misses != 0 || !next.Equal(time.Unix(0, 0)) {
		t.Fatalf("retry state not reset: misses=%d next=%v err=%v", misses, next, err)
	}
}

// Artists become fanart-eligible only once they carry an MBID; assignment
// through match application resets any pre-identity miss schedule.
func TestArtistMBIDAssignmentResetsArtRetryState(t *testing.T) {
	s, album := musicBrainzAlbumFixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.MarkArtistArtAttempt(ctx, album.PrimaryArtist.ArtistID, ArtAttemptMiss, now); err != nil {
		t.Fatal(err)
	}
	applyCanonicalRelease(t, s, album, "Local Artist", "mbid-local-artist", []source.ArtistCredit{{Name: "Local Artist", MBID: "mbid-local-artist"}})
	need, err := s.ArtistsNeedingArtAt(ctx, now, 10)
	if err != nil || len(need) != 1 || need[0].ArtistID != album.PrimaryArtist.ArtistID {
		t.Fatalf("artist must be art-eligible after gaining an MBID: %v err=%v", need, err)
	}
	if need[0].MusicBrainzID == "" {
		t.Fatal("selection must carry the MBID")
	}
}

// Fanart.tv is MBID-keyed: artists without one are not selectable and must
// not burn 404s or miss windows.
func TestArtistsNeedingArtSkipsWithoutMBID(t *testing.T) {
	s, album := musicBrainzAlbumFixture(t)
	ctx := context.Background()
	_ = album
	need, err := s.ArtistsNeedingArtAt(ctx, time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range need {
		if n.MusicBrainzID == "" {
			t.Fatalf("artist without MBID selected for fanart: %+v", n)
		}
	}
	if len(need) != 0 {
		t.Fatalf("fixture has no identified artists; selection must be empty, got %v", need)
	}
}
