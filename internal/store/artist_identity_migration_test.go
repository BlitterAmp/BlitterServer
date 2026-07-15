package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

func TestPendingMusicBrainzArtistsIncludesMissingRowsAndUsesBoundedPages(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for i := range 3 {
		id := fmt.Sprintf("art_pending_%d", i)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO artists(artist_id,name,musicbrainz_id,missing,created_at,change_seq) VALUES(?,?,?,?,?,?)`, id, fmt.Sprintf("Pending %d", i), fmt.Sprintf("mbid-%d", i), 1, time.Now().Unix(), 0); err != nil {
			t.Fatal(err)
		}
	}
	first, next, err := s.PendingMusicBrainzArtists(ctx, "", 2)
	if err != nil || len(first) != 2 || next == "" {
		t.Fatalf("first=%+v next=%q err=%v", first, next, err)
	}
	second, next, err := s.PendingMusicBrainzArtists(ctx, next, 2)
	if err != nil || len(second) != 1 || next != "" {
		t.Fatalf("second=%+v next=%q err=%v", second, next, err)
	}
	for _, artist := range append(first, second...) {
		if artist.MusicBrainzID == "" {
			t.Fatalf("pending artist lacks MBID: %+v", artist)
		}
	}
}

func TestMigration0014ReemitsAlreadyMissingArtistRemovals(t *testing.T) {
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
	if err := goose.UpToContext(ctx, db, "migrations", 13); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('library_scan_seq','7')`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id      string
		missing int
	}{{id: "art_already_missing", missing: 1}, {id: "art_zero_owned", missing: 0}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO artists(artist_id,name,missing,created_at,change_seq) VALUES(?,?,?,?,2)`, row.id, row.id, row.missing, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if err := goose.UpToContext(ctx, db, "migrations", 14); err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err := db.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM settings WHERE key='library_scan_seq'`).Scan(&seq); err != nil || seq != 8 {
		t.Fatalf("migration seq=%d err=%v", seq, err)
	}
	for _, id := range []string{"art_already_missing", "art_zero_owned"} {
		var missing int
		var changeSeq int64
		if err := db.QueryRowContext(ctx, `SELECT missing,change_seq FROM artists WHERE artist_id=?`, id).Scan(&missing, &changeSeq); err != nil || missing != 1 || changeSeq != seq {
			t.Fatalf("artist %s missing=%d change_seq=%d err=%v", id, missing, changeSeq, err)
		}
	}
	var fetchedAt int64
	if err := db.QueryRowContext(ctx, `SELECT musicbrainz_aliases_fetched_at FROM artists WHERE artist_id='art_already_missing'`).Scan(&fetchedAt); err != nil || fetchedAt != 0 {
		t.Fatalf("aliases fetched marker=%d err=%v", fetchedAt, err)
	}
	if err := goose.DownToContext(ctx, db, "migrations", 13); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `SELECT musicbrainz_aliases_fetched_at FROM artists LIMIT 1`); err == nil || !strings.Contains(err.Error(), "no such column") {
		t.Fatalf("migration down retained fetched marker: %v", err)
	}
	if err := goose.UpToContext(ctx, db, "migrations", 14); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT musicbrainz_aliases_fetched_at FROM artists WHERE artist_id='art_already_missing'`).Scan(&fetchedAt); err != nil || fetchedAt != 0 {
		t.Fatalf("migration re-up aliases fetched marker=%d err=%v", fetchedAt, err)
	}
}

func TestMigration0021QueuesMusicBrainzGenresAndReemitsArtists(t *testing.T) {
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
	if err := goose.UpToContext(ctx, db, "migrations", 20); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('library_scan_seq','7')`); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		id      string
		mbid    any
		aliases int
	}{{"complete", "mbid-complete", 123}, {"terminal", "mbid-terminal", -1}, {"local", nil, 0}}
	for _, row := range rows {
		if _, err := db.ExecContext(ctx, `INSERT INTO artists(artist_id,name,musicbrainz_id,created_at,change_seq,musicbrainz_aliases_fetched_at) VALUES(?,?,?,?,2,?)`, row.id, row.id, row.mbid, time.Now().Unix(), row.aliases); err != nil {
			t.Fatal(err)
		}
	}
	if err := goose.UpToContext(ctx, db, "migrations", 21); err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err := db.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM settings WHERE key='library_scan_seq'`).Scan(&seq); err != nil || seq != 8 {
		t.Fatalf("migration seq=%d err=%v", seq, err)
	}
	for _, row := range rows {
		var changeSeq, genreMarker int64
		if err := db.QueryRowContext(ctx, `SELECT change_seq,musicbrainz_genres_fetched_at FROM artists WHERE artist_id=?`, row.id).Scan(&changeSeq, &genreMarker); err != nil {
			t.Fatal(err)
		}
		wantMarker := int64(0)
		if row.aliases == -1 {
			wantMarker = -1
		}
		if changeSeq != seq || genreMarker != wantMarker {
			t.Fatalf("artist %s change_seq=%d marker=%d", row.id, changeSeq, genreMarker)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO artist_genres(artist_id,position,name) VALUES('complete',0,'rock')`); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownToContext(ctx, db, "migrations", 20); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `SELECT musicbrainz_genres_fetched_at FROM artists LIMIT 1`); err == nil || !strings.Contains(err.Error(), "no such column") {
		t.Fatalf("migration down retained genre marker: %v", err)
	}
	if _, err := db.ExecContext(ctx, `SELECT * FROM artist_genres`); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("migration down retained genres table: %v", err)
	}
}
