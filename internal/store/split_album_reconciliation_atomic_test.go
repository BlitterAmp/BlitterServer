package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func twoWaySplitFixture(t *testing.T) splitAlbumFixture {
	t.Helper()
	return newSplitAlbumFixture(t, "Atomic Album", 2020, []string{"Canonical Owner", "Canonical Owner feat. Guest"}, [][]int{{1, 3}, {2, 4}})
}

func fragmentAlbumID(f splitAlbumFixture) string {
	for _, id := range f.albumIDs {
		if id != f.anchorID {
			return id
		}
	}
	return ""
}

func TestSplitAlbumReconciliationRefusesStaleFragmentSnapshot(t *testing.T) {
	f := twoWaySplitFixture(t)
	fragmentID := fragmentAlbumID(f)
	beforeMutation := musicBrainzAlbumForTest(t, f.store, fragmentID)
	seq, err := f.store.NextScanSeq(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE tracks SET title='Changed During Lookup',change_seq=? WHERE album_id=?`, seq, fragmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE albums SET change_seq=? WHERE album_id=?`, seq, fragmentID); err != nil {
		t.Fatal(err)
	}
	beforeApply := snapshotLibraryRows(t, f.store)
	applySeq, _ := f.store.NextScanSeq(f.ctx)
	changed, err := f.store.ApplyMusicBrainzMatch(f.ctx, f.anchor, f.release, applySeq, MusicBrainzCandidate{ReleaseID: f.release.ReleaseID, ReleaseGroupID: f.release.ReleaseGroupID, Evidence: map[string]any{"fragmentSnapshots": []MusicBrainzAlbum{beforeMutation}}}, nil, time.Now().Add(time.Hour))
	if err != nil || changed {
		t.Fatalf("stale fragment snapshot changed=%v err=%v", changed, err)
	}
	if after := snapshotLibraryRows(t, f.store); after != beforeApply {
		t.Fatalf("stale refusal mutated rows\nbefore=%s\nafter=%s", beforeApply, after)
	}
}

func TestSplitAlbumReconciliationLateFailureRollsBackEveryDependentState(t *testing.T) {
	f := twoWaySplitFixture(t)
	fragmentID := fragmentAlbumID(f)
	profile, err := f.store.CreateProfile(f.ctx, "Listener")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SetLove(f.ctx, profile, fragmentID, "loved"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetRating(f.ctx, profile, "album", fragmentID, 8); err != nil {
		t.Fatal(err)
	}
	artID, err := f.store.UpsertArt(f.ctx, "rollback-art", "image/jpeg", []byte("rollback-art"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE albums SET art_id=? WHERE album_id=?`, artID, fragmentID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RecordMusicBrainzResult(f.ctx, fragmentID, "unmatched", MusicBrainzCandidate{}, nil, time.Now().Add(time.Hour), "before"); err != nil {
		t.Fatal(err)
	}
	before := fullReconciliationState(t, f.store)
	if _, err := f.store.db.ExecContext(f.ctx, `CREATE TRIGGER fail_split_album_late BEFORE UPDATE OF missing ON artists WHEN NEW.missing=1 BEGIN SELECT RAISE(ABORT,'synthetic late reconciliation failure'); END`); err != nil {
		t.Fatal(err)
	}
	changed, _, err := applySplitFixture(t, f)
	if err == nil || changed {
		t.Fatalf("late trigger must fail reconciliation: changed=%v err=%v", changed, err)
	}
	if after := fullReconciliationState(t, f.store); after != before {
		t.Fatalf("late failure did not roll back all state\nbefore=%s\nafter=%s", before, after)
	}
}

func TestSplitAlbumReconciliationSecondApplyIsIdempotent(t *testing.T) {
	f := twoWaySplitFixture(t)
	changed, firstSeq, err := applySplitFixture(t, f)
	if err != nil || !changed {
		t.Fatalf("first changed=%v err=%v", changed, err)
	}
	assertOneReconciledAlbum(t, f, 4)
	before := fullReconciliationState(t, f.store)
	versionBefore, _ := f.store.GetLibrarySummary(f.ctx)
	secondSeq := firstSeq + 1
	changed, err = f.store.ApplyMusicBrainzMatch(f.ctx, musicBrainzAlbumForTest(t, f.store, f.anchorID), f.release, secondSeq, MusicBrainzCandidate{ReleaseID: f.release.ReleaseID, ReleaseGroupID: f.release.ReleaseGroupID, Score: 100}, nil, time.Now().Add(time.Hour))
	if err != nil || changed {
		t.Fatalf("second changed=%v err=%v", changed, err)
	}
	if after := fullReconciliationState(t, f.store); after != before {
		t.Fatalf("idempotent apply churned rows\nbefore=%s\nafter=%s", before, after)
	}
	changes, _, err := f.store.ChangesSince(f.ctx, firstSeq, "", 100)
	if err != nil || len(changes) != 0 {
		t.Fatalf("second apply emitted changes=%+v err=%v", changes, err)
	}
	versionAfter, _ := f.store.GetLibrarySummary(f.ctx)
	if versionAfter.UpdatedAt != versionBefore.UpdatedAt {
		t.Fatalf("idempotent apply churned library freshness: before=%+v after=%+v", versionBefore, versionAfter)
	}
}

func TestSplitAlbumReconciliationMovesProfileAlbumReferences(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		anchorLove, fragmentLove     string
		anchorRating, fragmentRating int
		wantRefusal                  bool
	}{
		{name: "move fragment refs", fragmentLove: "loved", fragmentRating: 8},
		{name: "collapse identical refs", anchorLove: "loved", fragmentLove: "loved", anchorRating: 8, fragmentRating: 8},
		{name: "refuse contradictory love", anchorLove: "loved", fragmentLove: "not_for_me", wantRefusal: true},
		{name: "refuse contradictory rating", anchorRating: 8, fragmentRating: 3, wantRefusal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := twoWaySplitFixture(t)
			fragmentID := fragmentAlbumID(f)
			from, err := f.store.CreateProfile(f.ctx, "From")
			if err != nil {
				t.Fatal(err)
			}
			to, err := f.store.CreateProfile(f.ctx, "To")
			if err != nil {
				t.Fatal(err)
			}
			if tc.anchorLove != "" {
				if _, err := f.store.SetLove(f.ctx, from, f.anchorID, tc.anchorLove); err != nil {
					t.Fatal(err)
				}
			}
			if tc.fragmentLove != "" {
				if _, err := f.store.SetLove(f.ctx, from, fragmentID, tc.fragmentLove); err != nil {
					t.Fatal(err)
				}
			}
			if tc.anchorRating != 0 {
				if err := f.store.SetRating(f.ctx, from, "album", f.anchorID, tc.anchorRating); err != nil {
					t.Fatal(err)
				}
			}
			if tc.fragmentRating != 0 {
				if err := f.store.SetRating(f.ctx, from, "album", fragmentID, tc.fragmentRating); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.store.CreateRecommendation(f.ctx, from, to, fragmentID, "listen"); err != nil {
				t.Fatal(err)
			}
			before := fullReconciliationState(t, f.store)
			changed, _, err := applySplitFixture(t, f)
			if tc.wantRefusal {
				if err != nil || changed {
					t.Fatalf("contradiction changed=%v err=%v", changed, err)
				}
				if after := fullReconciliationState(t, f.store); after != before {
					t.Fatalf("contradiction mutated state\nbefore=%s\nafter=%s", before, after)
				}
				return
			}
			if err != nil || !changed {
				t.Fatalf("apply changed=%v err=%v", changed, err)
			}
			for table := range map[string]bool{"loves": true, "ratings": true, "recommendations": true} {
				column := "ref"
				if table == "ratings" {
					column = "item_id"
				}
				var survivor, fragment int
				if err := f.store.db.QueryRowContext(f.ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s=?`, table, column), f.anchorID).Scan(&survivor); err != nil {
					t.Fatal(err)
				}
				if err := f.store.db.QueryRowContext(f.ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s=?`, table, column), fragmentID).Scan(&fragment); err != nil {
					t.Fatal(err)
				}
				if survivor != 1 || fragment != 0 {
					t.Errorf("%s survivor refs=%d fragment refs=%d", table, survivor, fragment)
				}
			}
		})
	}
}

func TestSplitAlbumReconciliationArtworkPrecedenceAndBlobSurvival(t *testing.T) {
	for _, tc := range []struct {
		name             string
		anchorArt        bool
		fragmentArts     int
		wantFragmentFill bool
		wantRefusal      bool
	}{
		{name: "anchor art wins", anchorArt: true, fragmentArts: 1},
		{name: "single fragment fills empty anchor", fragmentArts: 1, wantFragmentFill: true},
		{name: "different fragment art is ambiguous", fragmentArts: 2, wantRefusal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owners := []string{"Owner", "Owner feat. One", "Owner feat. Two"}
			f := newSplitAlbumFixture(t, "Artwork Album", 2021, owners, [][]int{{1}, {2}, {3}})
			anchorArt, _ := f.store.UpsertArt(f.ctx, "anchor-hash", "image/jpeg", []byte("anchor"), t.TempDir())
			fragmentArtOne, _ := f.store.UpsertArt(f.ctx, "fragment-hash-one", "image/jpeg", []byte("fragment-one"), t.TempDir())
			fragmentArtTwo, _ := f.store.UpsertArt(f.ctx, "fragment-hash-two", "image/jpeg", []byte("fragment-two"), t.TempDir())
			if tc.anchorArt {
				_, _ = f.store.db.ExecContext(f.ctx, `UPDATE albums SET art_id=? WHERE album_id=?`, anchorArt, f.anchorID)
			}
			fragments := make([]string, 0, 2)
			for _, id := range f.albumIDs {
				if id != f.anchorID {
					fragments = append(fragments, id)
				}
			}
			if tc.fragmentArts > 0 {
				_, _ = f.store.db.ExecContext(f.ctx, `UPDATE albums SET art_id=? WHERE album_id=?`, fragmentArtOne, fragments[0])
			}
			if tc.fragmentArts > 1 {
				_, _ = f.store.db.ExecContext(f.ctx, `UPDATE albums SET art_id=? WHERE album_id=?`, fragmentArtTwo, fragments[1])
			}
			changed, _, err := applySplitFixture(t, f)
			if tc.wantRefusal {
				if err != nil || changed {
					t.Fatalf("ambiguous art changed=%v err=%v", changed, err)
				}
			} else if err != nil || !changed {
				t.Fatalf("apply changed=%v err=%v", changed, err)
			}
			if !tc.wantRefusal {
				assertOneReconciledAlbum(t, f, 3)
			}
			var gotArt string
			if err := f.store.db.QueryRowContext(f.ctx, `SELECT COALESCE(art_id,'') FROM albums WHERE album_id=?`, f.anchorID).Scan(&gotArt); err != nil {
				t.Fatal(err)
			}
			want := anchorArt
			if tc.wantFragmentFill {
				want = fragmentArtOne
			}
			if !tc.wantRefusal && gotArt != want {
				t.Errorf("survivor art=%q want=%q", gotArt, want)
			}
			var blobs int
			if err := f.store.db.QueryRowContext(f.ctx, `SELECT count(*) FROM art`).Scan(&blobs); err != nil {
				t.Fatal(err)
			}
			if blobs != 3 {
				t.Errorf("art blobs=%d want=3; reconciliation must not delete blobs", blobs)
			}
		})
	}
}

func TestSplitAlbumReconciliationChangeFeedPaginatesOneAtomicSequence(t *testing.T) {
	f := newSplitAlbumFixture(t, "Paged Album", 2022, []string{"Owner", "Owner feat. One", "Owner feat. Two"}, [][]int{{1}, {2}, {3}})
	changed, seq, err := applySplitFixture(t, f)
	if err != nil || !changed {
		t.Fatalf("apply changed=%v err=%v", changed, err)
	}
	var changes []LibraryChange
	cursor := ""
	for {
		page, next, version, err := f.store.ChangesSnapshot(f.ctx, f.baseSeq, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if version != seq {
			t.Fatalf("snapshot version=%d want reconciliation seq=%d", version, seq)
		}
		changes = append(changes, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	want := map[string]bool{"album:" + f.anchorID: true}
	for _, id := range f.albumIDs {
		if id != f.anchorID {
			want["album:"+id] = true
		}
	}
	for _, id := range f.trackIDs {
		want["track:"+id] = true
	}
	for _, id := range f.artistIDs {
		if id != f.anchor.PrimaryArtist.ArtistID {
			want["artist:"+id] = true
		}
	}
	for _, change := range changes {
		if change.ChangeSeq != seq {
			t.Errorf("change %+v not at atomic seq %d", change, seq)
		}
		delete(want, change.Kind+":"+change.ID)
	}
	if len(want) != 0 {
		t.Fatalf("paged feed missing reconciliation changes: %v; got=%+v", want, changes)
	}
}

func fullReconciliationState(t *testing.T, s *Store) string {
	t.Helper()
	queries := []string{
		`SELECT 'artist',artist_id,name,missing,change_seq FROM artists`,
		`SELECT 'album',album_id,COALESCE(art_id,''),missing,change_seq FROM albums`,
		`SELECT 'track',track_id,album_id,missing,change_seq FROM tracks`,
		`SELECT 'love',love_id,ref,0,0 FROM loves`,
		`SELECT 'rating',profile_id,CAST(item_id AS TEXT),rating10,0 FROM ratings`,
		`SELECT 'recommendation',recommendation_id,ref,seen,0 FROM recommendations`,
		`SELECT 'match',album_id,state,attempt_count,next_attempt_at FROM album_musicbrainz_matches`,
		`SELECT 'art',art_id,hash,0,0 FROM art`,
	}
	var out string
	for _, query := range queries {
		rows, err := s.db.QueryContext(context.Background(), query+` ORDER BY 1,2`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var a, b, c string
			var d, e int64
			if err := rows.Scan(&a, &b, &c, &d, &e); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out += fmt.Sprintf("%s|%s|%s|%d|%d\n", a, b, c, d, e)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}
