package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

func TestMigration0018ClearsSharedUntriedArtistArt(t *testing.T) {
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
	if err := goose.UpToContext(ctx, db, "migrations", 17); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('library_scan_seq','7') ON CONFLICT(key) DO UPDATE SET value='7'`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id    string
		tried int
		artID string
	}{
		{"art_fallback_one", 0, "img_shared"},
		{"art_fallback_two", 0, "img_shared"},
		{"art_provider", 1, "img_shared"},
		{"art_unique", 0, "img_unique"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO artists(artist_id,name,art_id,art_tried,missing,created_at,change_seq) VALUES(?,?,?,?,0,?,2)`, row.id, row.id, row.artID, row.tried, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if err := goose.UpToContext(ctx, db, "migrations", 18); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"art_fallback_one", "art_fallback_two"} {
		var art sql.NullString
		var seq int64
		if err := db.QueryRowContext(ctx, `SELECT art_id,change_seq FROM artists WHERE artist_id=?`, id).Scan(&art, &seq); err != nil || art.Valid || seq != 8 {
			t.Fatalf("fallback %s art=%v seq=%d err=%v", id, art, seq, err)
		}
	}
	for _, id := range []string{"art_provider", "art_unique"} {
		var art string
		if err := db.QueryRowContext(ctx, `SELECT art_id FROM artists WHERE artist_id=?`, id).Scan(&art); err != nil || art == "" {
			t.Fatalf("preserved %s art=%q err=%v", id, art, err)
		}
	}
}
