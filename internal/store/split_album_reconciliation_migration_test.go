package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

func TestMigration0016WakesOnlyAmbiguousSplitAlbumAnchors(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "migration.db")+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	goose.SetLogger(goose.NopLogger())
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, "migrations", 15); err != nil {
		t.Fatal(err)
	}
	for _, artistID := range []string{"art_anchor", "art_fragment"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO artists(artist_id,name,missing,created_at,change_seq) VALUES(?,?,0,?,0)`, artistID, artistID, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	for _, album := range []struct {
		id, artistID, title string
		year                int
	}{
		{"alb_split_anchor", "art_anchor", "Split Release", 2020},
		{"alb_split_fragment", "art_fragment", "Split Release", 2020},
		{"alb_solo", "art_anchor", "Solo Release", 2020},
		{"alb_matched_anchor", "art_anchor", "Matched Release", 2021},
		{"alb_matched_fragment", "art_fragment", "Matched Release", 2021},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO albums(album_id,artist_id,title,year,missing,created_at,change_seq) VALUES(?,?,?,?,0,?,0)`, album.id, album.artistID, album.title, album.year, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	for _, match := range []struct {
		albumID, state string
		next           int64
	}{
		{"alb_split_anchor", "ambiguous", 901},
		{"alb_split_fragment", "unmatched", 902},
		{"alb_solo", "ambiguous", 903},
		{"alb_matched_anchor", "matched", 904},
		{"alb_matched_fragment", "unmatched", 905},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO album_musicbrainz_matches(album_id,state,next_attempt_at) VALUES(?,?,?)`, match.albumID, match.state, match.next); err != nil {
			t.Fatal(err)
		}
	}
	if err := goose.UpToContext(ctx, db, "migrations", 16); err != nil {
		t.Fatal(err)
	}
	for albumID, want := range map[string]int64{
		"alb_split_anchor":     0,
		"alb_split_fragment":   902,
		"alb_solo":             903,
		"alb_matched_anchor":   904,
		"alb_matched_fragment": 905,
	} {
		var got int64
		if err := db.QueryRowContext(ctx, `SELECT next_attempt_at FROM album_musicbrainz_matches WHERE album_id=?`, albumID).Scan(&got); err != nil || got != want {
			t.Fatalf("album %s next_attempt_at=%d want=%d err=%v", albumID, got, want, err)
		}
	}
	if err := goose.UpToContext(ctx, db, "migrations", 17); err != nil {
		t.Fatal(err)
	}
	for _, albumID := range []string{"alb_split_fragment", "alb_matched_fragment"} {
		var got int64
		if err := db.QueryRowContext(ctx, `SELECT next_attempt_at FROM album_musicbrainz_matches WHERE album_id=?`, albumID).Scan(&got); err != nil || got != 0 {
			t.Fatalf("unmatched album %s next_attempt_at=%d want=0 err=%v", albumID, got, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE album_musicbrainz_matches SET next_attempt_at=999 WHERE state IN ('unmatched','ambiguous')`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, "migrations", 19); err != nil {
		t.Fatal(err)
	}
	for _, albumID := range []string{"alb_split_anchor", "alb_split_fragment", "alb_solo", "alb_matched_fragment"} {
		var got int64
		if err := db.QueryRowContext(ctx, `SELECT next_attempt_at FROM album_musicbrainz_matches WHERE album_id=?`, albumID).Scan(&got); err != nil || got != 0 {
			t.Fatalf("path retry album %s next_attempt_at=%d want=0 err=%v", albumID, got, err)
		}
	}
	var matchedNext int64
	if err := db.QueryRowContext(ctx, `SELECT next_attempt_at FROM album_musicbrainz_matches WHERE album_id='alb_matched_anchor'`).Scan(&matchedNext); err != nil || matchedNext != 904 {
		t.Fatalf("matched album next_attempt_at=%d want=904 err=%v", matchedNext, err)
	}
	if err := goose.UpToContext(ctx, db, "migrations", 20); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE albums SET missing=1 WHERE album_id='alb_split_fragment'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE albums SET artist_id='art_anchor',title='Split Release' WHERE album_id='alb_split_fragment'`); err != nil {
		t.Fatalf("retired fragment blocked visible identity after migration 20: %v", err)
	}
}
