package store

import (
	"context"
	"testing"
)

func TestSetAlbumArtAtNextSequenceIsAtomicAndNoOpDoesNotAdvance(t *testing.T) {
	ctx := context.Background()
	s := indexFixture(t)
	albums, _, err := s.ListAlbums(ctx, "title", "", 1)
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums=%+v err=%v", albums, err)
	}
	before, _ := s.GetLibrarySummary(ctx)
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_art_attachment BEFORE UPDATE OF art_id ON albums
		WHEN NEW.art_id='img-fail' BEGIN SELECT RAISE(ABORT,'art attachment failed'); END`); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.SetAlbumArtAtNextSequence(ctx, albums[0].AlbumID, "img-fail"); err == nil || applied {
		t.Fatalf("failed attachment applied=%v err=%v", applied, err)
	}
	afterFailure, _ := s.GetLibrarySummary(ctx)
	album, found, err := s.GetAlbum(ctx, albums[0].AlbumID)
	if err != nil || !found || album.ArtID != "" || afterFailure.Version != before.Version {
		t.Fatalf("failed attachment album=%+v version=%d want=%d err=%v", album, afterFailure.Version, before.Version, err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER fail_art_attachment`); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.SetAlbumArtAtNextSequence(ctx, albums[0].AlbumID, "img-ok"); err != nil || !applied {
		t.Fatalf("successful attachment applied=%v err=%v", applied, err)
	}
	afterSuccess, _ := s.GetLibrarySummary(ctx)
	if afterSuccess.Version <= before.Version {
		t.Fatalf("successful attachment version=%d want >%d", afterSuccess.Version, before.Version)
	}
	if applied, err := s.SetAlbumArtAtNextSequence(ctx, albums[0].AlbumID, "img-noop"); err != nil || applied {
		t.Fatalf("no-op attachment applied=%v err=%v", applied, err)
	}
	afterNoOp, _ := s.GetLibrarySummary(ctx)
	if afterNoOp.Version != afterSuccess.Version {
		t.Fatalf("no-op advanced version=%d want=%d", afterNoOp.Version, afterSuccess.Version)
	}
}
