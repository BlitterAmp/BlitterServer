package store

import (
	"context"
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
