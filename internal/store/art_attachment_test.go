package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestAttachTrackArtReplacesSourceArtAndEmitsChange(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	artDir := filepath.Join(t.TempDir(), "art")
	oldID, err := s.UpsertArt(ctx, sha256Hex([]byte("old art")), "image/jpeg", []byte("old art"), artDir)
	if err != nil {
		t.Fatal(err)
	}
	newID, err := s.UpsertArt(ctx, sha256Hex([]byte("new art")), "image/jpeg", []byte("new art"), artDir)
	if err != nil {
		t.Fatal(err)
	}
	seq, _ := s.NextScanSeq(ctx)
	if err := s.UpsertTrack(ctx, "filesystem", meta("song.flac", "Song", "Artist", "Album", "", 2026, 1), oldID, seq); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	tracks, _, err := s.ListTracks(ctx, "title", "", 10)
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks=%+v err=%v", tracks, err)
	}

	replacementSeq, _ := s.NextScanSeq(ctx)
	changed, err := s.AttachTrackArt(ctx, "filesystem", "song.flac", newID, replacementSeq)
	if err != nil || !changed {
		t.Fatalf("replace art changed=%v err=%v", changed, err)
	}
	track, found, err := s.GetTrack(ctx, tracks[0].TrackID)
	if err != nil || !found || track.ArtID != newID {
		t.Fatalf("replaced track=%+v found=%v err=%v", track, found, err)
	}
	changes, _, err := s.ChangesSince(ctx, seq, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Kind != "track" || changes[0].ID != track.TrackID || changes[0].ChangeSeq != replacementSeq {
		t.Fatalf("replacement changes=%+v", changes)
	}

	noOpSeq, _ := s.NextScanSeq(ctx)
	changed, err = s.AttachTrackArt(ctx, "filesystem", "song.flac", newID, noOpSeq)
	if err != nil || changed {
		t.Fatalf("same art changed=%v err=%v", changed, err)
	}
	changes, _, err = s.ChangesSince(ctx, replacementSeq, "", 20)
	if err != nil || len(changes) != 0 {
		t.Fatalf("same art changes=%+v err=%v", changes, err)
	}
}

func TestFindArtByHashRejectsAndUpsertRepairsDamagedBlob(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(string) error
	}{
		{name: "missing", damage: os.Remove},
		{name: "corrupt", damage: func(path string) error { return os.WriteFile(path, []byte("bad data"), 0o644) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := open(t)
			artDir := filepath.Join(t.TempDir(), "art")
			data := []byte("good art")
			hash := sha256Hex(data)
			artID, err := s.UpsertArt(ctx, hash, "image/jpeg", data, artDir)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(artDir, hash)
			if err := tc.damage(path); err != nil {
				t.Fatal(err)
			}
			if foundID, found, err := s.FindArtByHash(ctx, hash); err != nil || found || foundID != "" {
				t.Fatalf("damaged lookup id=%q found=%v err=%v", foundID, found, err)
			}
			repairedID, err := s.UpsertArt(ctx, hash, "image/jpeg", data, artDir)
			if err != nil || repairedID != artID {
				t.Fatalf("repair id=%q want=%q err=%v", repairedID, artID, err)
			}
			if foundID, found, err := s.FindArtByHash(ctx, hash); err != nil || !found || foundID != artID {
				t.Fatalf("repaired lookup id=%q found=%v err=%v", foundID, found, err)
			}
		})
	}
}

func TestFindArtByHashRejectsNonRegularBlob(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	artDir := filepath.Join(t.TempDir(), "art")
	data := []byte("good art")
	hash := sha256Hex(data)
	if _, err := s.UpsertArt(ctx, hash, "image/jpeg", data, artDir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(artDir, hash)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if artID, found, err := s.FindArtByHash(ctx, hash); err != nil || found || artID != "" {
		t.Fatalf("non-regular lookup id=%q found=%v err=%v", artID, found, err)
	}
}

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
