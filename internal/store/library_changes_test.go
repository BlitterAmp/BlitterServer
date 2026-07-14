package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/source"
)

// rescan runs one scan pass over the given tracks (a fresh seq), the way the
// library manager does.
func rescan(t *testing.T, s *Store, metas []source.TrackMeta) {
	t.Helper()
	ctx := context.Background()
	seq, err := s.NextScanSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range metas {
		if err := s.UpsertTrack(ctx, "filesystem", m, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
}

func counts(chs []LibraryChange) (present, missing int) {
	for _, c := range chs {
		if c.Missing {
			missing++
		} else {
			present++
		}
	}
	return
}

func hasChange(chs []LibraryChange, kind string, missing bool) bool {
	for _, c := range chs {
		if c.Kind == kind && c.Missing == missing {
			return true
		}
	}
	return false
}

func TestChangeTrackingAndDelta(t *testing.T) {
	ctx := context.Background()
	s := indexFixture(t) // scan 1: 2 artists, 3 albums, 4 tracks — all change_seq=1

	sum, err := s.GetLibrarySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Version != 1 {
		t.Fatalf("version=%d want 1", sum.Version)
	}

	// since=0 bootstraps everything: 2 artists + 3 albums + 4 tracks, none missing.
	all, next, err := s.ChangesSince(ctx, 0, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Fatalf("unexpected cursor %q", next)
	}
	if present, missing := counts(all); present != 9 || missing != 0 {
		t.Fatalf("since=0: present=%d missing=%d want 9/0", present, missing)
	}

	// A caught-up client (since=version) sees nothing.
	if none, _, _ := s.ChangesSince(ctx, sum.Version, "", 100); len(none) != 0 {
		t.Fatalf("since=version returned %d changes, want 0", len(none))
	}

	base := []source.TrackMeta{
		meta("a1/al1/t1.flac", "Alpha One", "The Alphas", "First Album", "Rock", 1994, 1),
		meta("a1/al1/t2.flac", "Alpha Two", "The Alphas", "First Album", "Rock", 1994, 2),
		meta("a1/al2/t1.flac", "Alpha Three", "The Alphas", "Second Album", "Electronic", 2001, 1),
		meta("a2/al1/t1.flac", "Beta One", "Betamax", "Beta Album", "Jazz", 1987, 1),
	}

	// Re-scan identical → nothing changes; unchanged rows keep their change_seq.
	rescan(t, s, base) // scan 2
	if delta, _, _ := s.ChangesSince(ctx, 1, "", 100); len(delta) != 0 {
		t.Fatalf("identical rescan produced %d changes, want 0", len(delta))
	}

	// Change one track's title → only that track bumps.
	changed := append([]source.TrackMeta(nil), base...)
	changed[0] = meta("a1/al1/t1.flac", "Alpha One (Remix)", "The Alphas", "First Album", "Rock", 1994, 1)
	rescan(t, s, changed) // scan 3
	d3, _, _ := s.ChangesSince(ctx, 2, "", 100)
	if present, missing := counts(d3); present != 1 || missing != 0 {
		t.Fatalf("one-track change: present=%d missing=%d want 1/0", present, missing)
	}
	if !hasChange(d3, "track", false) {
		t.Fatalf("expected a track change, got %+v", d3)
	}

	// Remove Beta One → its track, album, and artist all go missing.
	rescan(t, s, changed[:3]) // scan 4 (omit a2/al1/t1)
	d4, _, _ := s.ChangesSince(ctx, 3, "", 100)
	if present, missing := counts(d4); present != 0 || missing != 3 {
		t.Fatalf("removal: present=%d missing=%d want 0/3", present, missing)
	}
	if !hasChange(d4, "track", true) || !hasChange(d4, "album", true) || !hasChange(d4, "artist", true) {
		t.Fatalf("expected track+album+artist removals, got %+v", d4)
	}
}

func TestChangesSincePaginates(t *testing.T) {
	ctx := context.Background()
	s := indexFixture(t) // 9 changed entities at seq=1

	var seen []LibraryChange
	cur := ""
	for {
		page, next, err := s.ChangesSince(ctx, 0, cur, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > 2 {
			t.Fatalf("page size %d exceeds limit 2", len(page))
		}
		seen = append(seen, page...)
		if next == "" {
			break
		}
		cur = next
	}
	if len(seen) != 9 {
		t.Fatalf("paged total=%d want 9", len(seen))
	}
}

func TestChangesSnapshotPinsRowsAndVersionToOneReadTransaction(t *testing.T) {
	ctx := context.Background()
	s := indexFixture(t)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	version, err := libraryChangesVersionTx(ctx, tx)
	if err != nil || version != 1 {
		t.Fatalf("snapshot version=%d err=%v", version, err)
	}

	writer, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	newVersion, err := nextScanSeqTx(ctx, writer)
	if err == nil {
		_, err = writer.ExecContext(ctx, `UPDATE tracks SET title='Committed Later',change_seq=? WHERE track_id=(SELECT min(track_id) FROM tracks)`, newVersion)
	}
	if err == nil {
		err = writer.Commit()
	} else {
		_ = writer.Rollback()
	}
	if err != nil {
		t.Fatal(err)
	}

	changes, _, err := changesSinceQuery(ctx, tx, 1, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 || len(changes) != 0 {
		t.Fatalf("snapshot exposed version=%d changes=%+v", version, changes)
	}
	publicChanges, _, publicVersion, err := s.ChangesSnapshot(ctx, 1, "", 100)
	if err != nil || publicVersion != newVersion || len(publicChanges) != 1 || publicChanges[0].ChangeSeq != newVersion {
		t.Fatalf("next snapshot version=%d changes=%+v err=%v", publicVersion, publicChanges, err)
	}
}
