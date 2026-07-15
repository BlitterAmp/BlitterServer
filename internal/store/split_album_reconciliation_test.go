package store

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/source"
)

type splitAlbumFixture struct {
	store       *Store
	ctx         context.Context
	anchor      MusicBrainzAlbum
	release     CanonicalRelease
	albumIDs    []string
	artistIDs   []string
	trackIDs    []string
	displays    map[string]string
	baseSeq     int64
	anchorID    string
	anchorArt   string
	fragmentArt string
}

func newSplitAlbumFixture(t *testing.T, title string, year int, owners []string, positionsByOwner [][]int) splitAlbumFixture {
	t.Helper()
	ctx := context.Background()
	s := open(t)
	seq, err := s.NextScanSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for ownerIndex, positions := range positionsByOwner {
		owner := owners[ownerIndex]
		for _, position := range positions {
			display := owner
			meta := source.TrackMeta{
				NativeID: fmt.Sprintf("%s/%02d.flac", owner, position), Title: fmt.Sprintf("Track %02d", position),
				Album: title, Year: year, Disc: 1, Index: position, DurationMs: 180000 + position,
				PrimaryArtist: source.ArtistReference{Name: owner}, AlbumCredits: []source.ArtistCredit{{Name: owner}},
				TrackCredits: []source.ArtistCredit{{Name: display}}, Container: "flac", Codec: "flac", Version: 1,
			}
			if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	albums, _, err := s.ListAlbums(ctx, "title", "", 100)
	if err != nil || len(albums) != len(owners) {
		t.Fatalf("seed albums=%d want=%d err=%v", len(albums), len(owners), err)
	}
	var anchorID string
	var albumIDs, artistIDs []string
	for _, album := range albums {
		albumIDs = append(albumIDs, album.AlbumID)
		artistIDs = append(artistIDs, album.ArtistID)
		if album.ArtistName == owners[0] {
			anchorID = album.AlbumID
			if _, err := s.db.ExecContext(ctx, `UPDATE artists SET musicbrainz_id=? WHERE artist_id=?`, "mbid-canonical-owner", album.ArtistID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if anchorID == "" {
		t.Fatal("anchor album not seeded")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE albums SET musicbrainz_release_group_id=? WHERE album_id=?`, "rg-"+title, anchorID); err != nil {
		t.Fatal(err)
	}
	selected := MusicBrainzCandidate{ReleaseID: "release-" + title, ReleaseGroupID: "rg-" + title, Title: title, ArtistCredit: owners[0], Score: 100, Evidence: map[string]any{"trackCount": "union", "positions": len(flattenPositions(positionsByOwner))}}
	if err := s.RecordMusicBrainzResult(ctx, anchorID, "ambiguous", selected, []MusicBrainzCandidate{selected}, time.Now().Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	anchor := musicBrainzAlbumForTest(t, s, anchorID)
	allPositions := flattenPositions(positionsByOwner)
	sort.Ints(allPositions)
	release := CanonicalRelease{ReleaseID: selected.ReleaseID, ReleaseGroupID: selected.ReleaseGroupID, AlbumCredits: []source.ArtistCredit{{Name: owners[0], MBID: "mbid-canonical-owner"}}}
	for _, position := range allPositions {
		release.Tracks = append(release.Tracks, CanonicalTrack{Disc: 1, Index: position, Title: fmt.Sprintf("Track %02d", position), DurationMs: 180000 + position, RecordingID: fmt.Sprintf("recording-%02d", position)})
	}
	tracks, _, err := s.ListTracks(ctx, "title", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	displays := make(map[string]string, len(tracks))
	var trackIDs []string
	for _, track := range tracks {
		trackIDs = append(trackIDs, track.TrackID)
		displays[track.TrackID] = track.ArtistName
	}
	return splitAlbumFixture{store: s, ctx: ctx, anchor: anchor, release: release, albumIDs: albumIDs, artistIDs: artistIDs, trackIDs: trackIDs, displays: displays, baseSeq: seq, anchorID: anchorID}
}

func musicBrainzAlbumForTest(t *testing.T, s *Store, albumID string) MusicBrainzAlbum {
	t.Helper()
	ctx := context.Background()
	a, found, err := s.GetAlbum(ctx, albumID)
	if err != nil || !found {
		t.Fatalf("album snapshot found=%v err=%v", found, err)
	}
	tracks, err := s.ListAlbumTracks(ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT change_seq FROM albums WHERE album_id=?`, albumID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return MusicBrainzAlbum{AlbumID: a.AlbumID, Title: a.Title, Year: a.Year, ReleaseID: a.MusicBrainzReleaseID, ReleaseGroupID: a.MusicBrainzReleaseGroupID, Version: version, TrackCount: len(tracks), PrimaryArtist: a.PrimaryArtist, ArtistCredits: a.ArtistCredits, Tracks: tracks}
}

func flattenPositions(groups [][]int) []int {
	var positions []int
	for _, group := range groups {
		positions = append(positions, group...)
	}
	return positions
}

func applySplitFixture(t *testing.T, f splitAlbumFixture) (bool, int64, error) {
	t.Helper()
	seq, err := f.store.NextScanSeq(f.ctx)
	if err != nil {
		return false, 0, err
	}
	changed, err := f.store.ApplyMusicBrainzMatch(f.ctx, f.anchor, f.release, seq, MusicBrainzCandidate{ReleaseID: f.release.ReleaseID, ReleaseGroupID: f.release.ReleaseGroupID, Score: 100}, nil, time.Now().Add(7*24*time.Hour))
	return changed, seq, err
}

func assertOneReconciledAlbum(t *testing.T, f splitAlbumFixture, trackCount int) {
	t.Helper()
	albums, _, err := f.store.ListAlbums(f.ctx, "title", "", 100)
	if err != nil || len(albums) != 1 {
		t.Fatalf("visible albums=%d want=1 err=%v: %+v", len(albums), err, albums)
	}
	if albums[0].AlbumID != f.anchorID || albums[0].MusicBrainzReleaseGroupID != f.release.ReleaseGroupID {
		t.Fatalf("survivor=%+v want anchor=%s group=%s", albums[0], f.anchorID, f.release.ReleaseGroupID)
	}
	tracks, err := f.store.ListAlbumTracks(f.ctx, f.anchorID)
	if err != nil || len(tracks) != trackCount {
		t.Fatalf("survivor tracks=%d want=%d err=%v", len(tracks), trackCount, err)
	}
	gotIDs := make(map[string]bool, len(tracks))
	for _, track := range tracks {
		gotIDs[track.TrackID] = true
		if track.ArtistName != f.displays[track.TrackID] {
			t.Errorf("track %s credit display=%q want original %q", track.TrackID, track.ArtistName, f.displays[track.TrackID])
		}
	}
	for _, id := range f.trackIDs {
		if !gotIDs[id] {
			t.Errorf("original track id %s was not retained", id)
		}
	}
}

func TestSplitAlbumReconciliationStructuralAuditShapes(t *testing.T) {
	cases := []struct {
		name        string
		year        int
		rows        int
		tracks      int
		fragmentTwo bool
		owners      []string
		positions   [][]int
	}{
		{
			name: "A Reckoning", year: 2023, rows: 4, tracks: 10,
			owners:    []string{"Kimbra", "Kimbra Ft. Erick The Architect", "Kimbra Ft. Ryan Lott", "Kimbra Ft. Tommy Raps + Pink Siifu"},
			positions: [][]int{{1, 2, 3, 4, 5, 9, 10}, {7}, {8}, {6}},
		},
		{name: "Blues On The Bayou", rows: 2, tracks: 15},
		{name: "Escape The Chaos", rows: 2, tracks: 12},
		{name: "Everything Is OK", rows: 2, tracks: 12, fragmentTwo: true},
		{
			name: "Heaven", year: 2020, rows: 4, tracks: 16,
			owners:    []string{"The Avener", "Terry Callier & The Avener", "The Avener & Tiwayo", "Bob Dylan"},
			positions: [][]int{{1, 2, 3, 4, 6, 8, 9, 11, 12, 13, 14, 15, 16}, {5}, {10}, {7}},
		},
		{
			name: "Infinite Health", year: 2024, rows: 2, tracks: 9,
			owners:    []string{"Tycho", "Tycho, Cautious Clay"},
			positions: [][]int{{1, 2, 3, 4, 6, 7, 8, 9}, {5}},
		},
		{name: "Shape & Form", rows: 2, tracks: 14},
		{name: "Death of Slim Shady", rows: 2, tracks: 19},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owners, positions := tc.owners, tc.positions
			if len(owners) == 0 {
				owners = make([]string, tc.rows)
				positions = make([][]int, tc.rows)
				owners[0] = "Canonical Owner"
				for i := 1; i < tc.rows; i++ {
					owners[i] = fmt.Sprintf("Canonical Owner feat. Guest %d", i)
				}
				if tc.fragmentTwo {
					positions[0] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
					positions[1] = []int{11, 12}
				} else {
					for position := 1; position <= tc.tracks; position++ {
						owner := (position - 1) % tc.rows
						positions[owner] = append(positions[owner], position)
					}
				}
			}
			year := tc.year
			if year == 0 {
				year = 2001
			}
			f := newSplitAlbumFixture(t, tc.name, year, owners, positions)
			changed, _, err := applySplitFixture(t, f)
			if err != nil || !changed {
				t.Fatalf("apply changed=%v err=%v", changed, err)
			}
			assertOneReconciledAlbum(t, f, tc.tracks)
		})
	}
}

func TestSplitAlbumReconciliationRefusesUnsafeUnions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *splitAlbumFixture)
	}{
		{"overlapping positions", func(t *testing.T, f *splitAlbumFixture) {
			_, err := f.store.db.ExecContext(f.ctx, `UPDATE tracks SET idx=1 WHERE album_id<>? AND idx=2`, f.anchorID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"missing position", func(t *testing.T, f *splitAlbumFixture) {
			_, err := f.store.db.ExecContext(f.ctx, `UPDATE tracks SET missing=1 WHERE album_id<>? AND idx=2`, f.anchorID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"gapped positions", func(t *testing.T, f *splitAlbumFixture) {
			_, err := f.store.db.ExecContext(f.ctx, `UPDATE tracks SET idx=5 WHERE album_id<>? AND idx=4`, f.anchorID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"index zero", func(t *testing.T, f *splitAlbumFixture) {
			_, err := f.store.db.ExecContext(f.ctx, `UPDATE tracks SET idx=0 WHERE album_id<>?`, f.anchorID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"materially different complete recording candidate", func(t *testing.T, f *splitAlbumFixture) {
			first := MusicBrainzCandidate{ReleaseID: f.release.ReleaseID, ReleaseGroupID: f.release.ReleaseGroupID, Score: 100, Evidence: map[string]any{"trackCount": "complete", "recordings": []string{"a", "b", "c", "d"}}}
			second := MusicBrainzCandidate{ReleaseID: "other-edition", ReleaseGroupID: f.release.ReleaseGroupID, Score: 100, Evidence: map[string]any{"trackCount": "complete", "recordings": []string{"w", "x", "y", "z"}}}
			if err := f.store.RecordMusicBrainzResult(f.ctx, f.anchorID, "ambiguous", first, []MusicBrainzCandidate{first, second}, time.Now().Add(24*time.Hour), ""); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newSplitAlbumFixture(t, "Unsafe Union", 2010, []string{"Owner", "Owner feat. Guest"}, [][]int{{1, 3}, {2, 4}})
			tc.mutate(t, &f)
			before := snapshotLibraryRows(t, f.store)
			changed, _, err := applySplitFixture(t, f)
			if err != nil || changed {
				t.Fatalf("unsafe union changed=%v err=%v", changed, err)
			}
			if after := snapshotLibraryRows(t, f.store); after != before {
				t.Fatalf("unsafe union mutated library\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

func TestSplitAlbumReconciliationAcceptsStrongReleaseFitWithTagDrift(t *testing.T) {
	f := newSplitAlbumFixture(t, "Tag Drift", 2020, []string{"Owner", "Collaborator"}, [][]int{{1, 3, 5}, {2, 4}})
	f.release.Tracks[0].Title = "Track 01 (radio edit)"
	f.release.Tracks[1], f.release.Tracks[2] = f.release.Tracks[2], f.release.Tracks[1]
	f.release.Tracks[1].Index, f.release.Tracks[2].Index = 2, 3
	f.release.Tracks[4].Title = "Edition-specific title"
	f.release.Tracks[4].DurationMs += 10000
	changed, _, err := applySplitFixture(t, f)
	if err != nil || !changed {
		t.Fatalf("strong release fit changed=%v err=%v", changed, err)
	}
	assertOneReconciledAlbum(t, f, 5)
}

func TestSplitAlbumReconciliationKeepsDifferentYearsAndReleaseGroupsSeparate(t *testing.T) {
	for _, title := range []string{"Discovery", "Power"} {
		t.Run(title, func(t *testing.T) {
			f := newSplitAlbumFixture(t, title, 2000, []string{"Owner", "Owner feat. Guest"}, [][]int{{1}, {2}})
			fragment := f.albumIDs[0]
			if fragment == f.anchorID {
				fragment = f.albumIDs[1]
			}
			if title == "Discovery" {
				if _, err := f.store.db.ExecContext(f.ctx, `UPDATE albums SET year=2001 WHERE album_id=?`, fragment); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.store.db.ExecContext(f.ctx, `UPDATE albums SET musicbrainz_release_group_id='different-group' WHERE album_id=?`, fragment); err != nil {
					t.Fatal(err)
				}
			}
			changed, _, err := applySplitFixture(t, f)
			if err != nil {
				t.Fatal(err)
			}
			albums, _, err := f.store.ListAlbums(f.ctx, "title", "", 10)
			if err != nil || len(albums) != 2 || !changed {
				t.Fatalf("different edition was merged: albums=%+v changed=%v err=%v", albums, changed, err)
			}
		})
	}
}

func TestSplitAlbumReconciliationRejectsLongEditionForFeddeUnion(t *testing.T) {
	f := newSplitAlbumFixture(t, "Something Real", 2016, []string{"Fedde Le Grand", "Fedde Le Grand feat. Jonathan Mendelsohn"}, [][]int{{1, 3, 5, 7, 9, 11, 13}, {2, 4, 6, 8, 10, 12, 14}})
	for position := 15; position <= 31; position++ {
		f.release.Tracks = append(f.release.Tracks, CanonicalTrack{Disc: 1, Index: position, Title: fmt.Sprintf("Bonus %02d", position), RecordingID: fmt.Sprintf("bonus-%02d", position)})
	}
	before := snapshotLibraryRows(t, f.store)
	changed, _, err := applySplitFixture(t, f)
	if err != nil || changed {
		t.Fatalf("31-track edition matched 14-track union: changed=%v err=%v", changed, err)
	}
	if after := snapshotLibraryRows(t, f.store); after != before {
		t.Fatalf("31-track refusal mutated rows\nbefore=%s\nafter=%s", before, after)
	}
}

func snapshotLibraryRows(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.Query(`SELECT 'artist',artist_id,name,missing,change_seq FROM artists UNION ALL SELECT 'album',album_id,title,missing,change_seq FROM albums UNION ALL SELECT 'track',track_id,album_id,missing,change_seq FROM tracks ORDER BY 1,2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out string
	for rows.Next() {
		var kind, id, value string
		var missing int
		var seq int64
		if err := rows.Scan(&kind, &id, &value, &missing, &seq); err != nil {
			t.Fatal(err)
		}
		out += fmt.Sprintf("%s|%s|%s|%d|%d\n", kind, id, value, missing, seq)
	}
	return out
}
