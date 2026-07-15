package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/source"
)

func TestFinishScanHidesCreditOnlyArtistsAndEmitsRemovals(t *testing.T) {
	names := []string{
		"Eminem feat. Rihanna",
		"BT - JES",
		"BT with April Bender",
		"BT with Au5 & Mangal Suvarnan",
		"BT with Christian Burns",
		"BT with Emma Hewitt",
		"BT with Iraina Mancini",
		"BT with Matt Fax",
		"BT with Matt Fax & Nation Of One",
		"BT with Wish I Was & Lola Rhodes",
		"FM-84, Clive Farrington",
		"FM-84, OLLIE WRIDE",
		"FM-84, Timecop1983, Josh Dally",
		"Keith Emerson",
		"Justin Timberlake",
	}
	for i, name := range names {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			ctx := context.Background()
			seq, err := s.NextScanSeq(ctx)
			if err != nil {
				t.Fatal(err)
			}
			owner := fmt.Sprintf("Album Owner %d", i)
			meta := source.TrackMeta{
				NativeID: fmt.Sprintf("track-%d.flac", i), Title: "Credited Track", Album: "Owned Album",
				PrimaryArtist: source.ArtistReference{Name: owner}, AlbumCredits: []source.ArtistCredit{{Name: owner}},
				TrackCredits: []source.ArtistCredit{{Name: name}}, Container: "flac", Codec: "flac", Version: 1,
			}
			if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
				t.Fatal(err)
			}
			var creditID string
			if err := s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE name=?`, name).Scan(&creditID); err != nil {
				t.Fatal(err)
			}
			if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
				t.Fatal(err)
			}

			artists, _, err := s.ListArtists(ctx, "title", "", 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, artist := range artists {
				if artist.ArtistID == creditID || artist.Name == name {
					t.Fatalf("credit-only artist listed: %+v", artist)
				}
			}
			if artist, found, err := s.GetArtist(ctx, creditID); err != nil || found {
				t.Fatalf("credit-only artist detail found=%v artist=%+v err=%v", found, artist, err)
			}
			search, err := s.SearchLibrary(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			if len(search.Artists) != 0 || len(search.Tracks) != 1 || len(search.Tracks[0].ArtistCredits) != 1 || search.Tracks[0].ArtistCredits[0].Name != name {
				t.Fatalf("search artists=%+v tracks=%+v", search.Artists, search.Tracks)
			}
			needs, err := s.ArtistsNeedingArt(ctx, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, need := range needs {
				if need.ArtistID == creditID || need.Name == name {
					t.Fatalf("credit-only artist needs art: %+v", need)
				}
			}
			changes, _, err := s.ChangesSince(ctx, seq-1, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			removed := false
			for _, change := range changes {
				removed = removed || change.Kind == "artist" && change.ID == creditID && change.Missing
			}
			if !removed {
				t.Fatalf("missing artist removal not emitted: %+v", changes)
			}
		})
	}
}

func TestApplyMusicBrainzArtistMetadataMergesDuplicateAlbumOwners(t *testing.T) {
	tests := []struct {
		name, local, canonical, mbid string
		aliases                      []string
	}{
		{name: "exclamation exact", local: "!!!", canonical: "!!!", mbid: "f26c72d3-e52c-467b-b651-679c73d8e1a7", aliases: []string{"Chk Chk Chk"}},
		{name: "exclamation compound alias", local: "!!! (Chk Chk Chk)", canonical: "!!!", mbid: "f26c72d3-e52c-467b-b651-679c73d8e1a7", aliases: []string{"Chk Chk Chk"}},
		{name: "Arcade Fire", local: "Arcade Fire", canonical: "Arcade Fire", mbid: "52074ba6-e495-4ef3-9bb4-0703888a9f68"},
		{name: "B.B. King", local: "B.B. King", canonical: "B.B. King", mbid: "dcb03ce3-67a5-4eb3-b2d1-2a12d93a38f3"},
		{name: "Blink 182", local: "Blink 182", canonical: "blink‐182", mbid: "0743b15a-3c32-48c8-ad58-cb325350befa", aliases: []string{"Blink 182", "Blink-182"}},
		{name: "blink-182", local: "blink-182", canonical: "blink‐182", mbid: "0743b15a-3c32-48c8-ad58-cb325350befa", aliases: []string{"Blink 182", "Blink-182"}},
		{name: "BT", local: "BT", canonical: "BT", mbid: "88e2147d-0332-46f6-85b2-b5f463ba957b"},
		{name: "Daft Punk", local: "Daft Punk", canonical: "Daft Punk", mbid: "056e4f3e-d505-4dad-8ec1-d04f521cbb56"},
		{name: "Dance With The Dead", local: "Dance With The Dead", canonical: "Dance With the Dead", mbid: "f7ab8acf-e859-468d-b335-d2dcc7671cb1"},
		{name: "Duran Duran", local: "Duran Duran", canonical: "Duran Duran", mbid: "1a1cd7f3-e5df-4eca-bae2-2757c9e656b5"},
		{name: "Fat Boy Slim", local: "Fat Boy Slim", canonical: "Fatboy Slim", mbid: "34c63966-445c-4613-afe1-4f0e1e53ae9a", aliases: []string{"Fat Boy Slim"}},
		{name: "FM-84", local: "FM-84", canonical: "FM-84", mbid: "1bc76772-45fe-4f14-92b5-c4f9ff567fcf"},
		{name: "Frank Zappa", local: "Frank Zappa", canonical: "Frank Zappa", mbid: "e20747e7-55a4-452e-8766-7b985585082d"},
		{name: "Kimbra", local: "Kimbra", canonical: "Kimbra", mbid: "fad2875e-ba81-4637-b859-4684622dcb1c"},
		{name: "Lane 8", local: "Lane 8", canonical: "Lane 8", mbid: "bd052b81-353a-4bce-8cd8-72ba9d4ce414"},
		{name: "PJ Harvey", local: "PJ Harvey", canonical: "PJ Harvey", mbid: "e795e03d-b5d5-4a5f-834d-162cfb308a2c"},
		{name: "Royksopp alias", local: "Royksopp", canonical: "Röyksopp", mbid: "1c70a3fc-fa3c-4be1-8b55-c3192db8a884", aliases: []string{"Royksopp"}},
		{name: "Röyksopp", local: "Röyksopp", canonical: "Röyksopp", mbid: "1c70a3fc-fa3c-4be1-8b55-c3192db8a884", aliases: []string{"Royksopp"}},
		{name: "The Avener", local: "The Avener", canonical: "The Avener", mbid: "529c40b4-c57f-49e2-ad5a-201eb2fb4a5b"},
		{name: "Tycho", local: "Tycho", canonical: "Tycho", mbid: "cbef45a9-7acb-4325-94c9-70081ac8d1b8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			ctx := context.Background()
			seq, _ := s.NextScanSeq(ctx)
			canonicalMeta := source.TrackMeta{
				NativeID: "canonical.flac", Title: "Canonical Track", Album: "Canonical Album",
				PrimaryArtist: source.ArtistReference{Name: tc.canonical, MBID: tc.mbid},
				AlbumCredits:  []source.ArtistCredit{{Name: tc.canonical, MBID: tc.mbid}},
				TrackCredits:  []source.ArtistCredit{{Name: tc.canonical, MBID: tc.mbid}}, Container: "flac", Codec: "flac", Version: 1,
			}
			localMeta := source.TrackMeta{
				NativeID: "local.flac", Title: "Local Track", Album: "Local Album",
				PrimaryArtist: source.ArtistReference{Name: " " + strings.ToUpper(tc.local) + " "},
				AlbumCredits:  []source.ArtistCredit{{Name: " " + strings.ToUpper(tc.local) + " "}},
				TrackCredits:  []source.ArtistCredit{{Name: tc.local}}, Container: "flac", Codec: "flac", Version: 1,
			}
			for _, meta := range []source.TrackMeta{canonicalMeta, localMeta} {
				if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
				t.Fatal(err)
			}
			var canonicalID, localID string
			if err := s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id=?`, tc.mbid).Scan(&canonicalID); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id IS NULL`).Scan(&localID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE artists SET art_id='img-local' WHERE artist_id=?`, localID); err != nil {
				t.Fatal(err)
			}

			applySeq, _ := s.NextScanSeq(ctx)
			changed, err := s.ApplyMusicBrainzArtistMetadata(ctx, canonicalID, tc.mbid, tc.canonical, tc.aliases, nil, applySeq)
			if err != nil || !changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			artists, _, err := s.ListArtists(ctx, "title", "", 100)
			if err != nil || len(artists) != 1 {
				t.Fatalf("artists=%+v err=%v", artists, err)
			}
			artist := artists[0]
			if artist.ArtistID != canonicalID || artist.Name != tc.canonical || artist.MusicBrainzID != tc.mbid || artist.AlbumCount != 2 || artist.ArtID != "img-local" {
				t.Fatalf("canonical artist=%+v", artist)
			}
			albums, err := s.ListArtistAlbums(ctx, canonicalID)
			if err != nil || len(albums) != 2 {
				t.Fatalf("albums=%+v err=%v", albums, err)
			}
			tracks, _, err := s.ListTracks(ctx, "title", "", 100)
			if err != nil || len(tracks) != 2 {
				t.Fatalf("tracks=%+v err=%v", tracks, err)
			}
			creditNames := map[string]bool{}
			for _, track := range tracks {
				if track.ArtistID != canonicalID || len(track.ArtistCredits) != 1 || track.ArtistCredits[0].ArtistID != canonicalID {
					t.Fatalf("track identity not merged: %+v", track)
				}
				if track.ArtistName != tc.canonical {
					t.Fatalf("track primary display name=%q want %q", track.ArtistName, tc.canonical)
				}
				creditNames[track.ArtistCredits[0].Name] = true
			}
			if !creditNames[tc.canonical] || !creditNames[tc.local] {
				t.Fatalf("track credit display names not preserved: %v", creditNames)
			}
			var missing int
			if err := s.db.QueryRowContext(ctx, `SELECT missing FROM artists WHERE artist_id=?`, localID).Scan(&missing); err != nil || missing != 1 {
				t.Fatalf("duplicate missing=%d err=%v", missing, err)
			}
			changes, _, err := s.ChangesSince(ctx, seq, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			removed, present := false, false
			for _, change := range changes {
				removed = removed || change.ID == localID && change.Kind == "artist" && change.Missing
				present = present || change.ID == canonicalID && change.Kind == "artist" && !change.Missing
			}
			if !removed || !present {
				t.Fatalf("changes removed=%v present=%v: %+v", removed, present, changes)
			}
		})
	}
}

func TestApplyMusicBrainzArtistMetadataRefusesAmbiguousEvidence(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	for i, mbid := range []string{"canonical-one", "canonical-two"} {
		name := fmt.Sprintf("Canonical %d", i+1)
		meta := source.TrackMeta{NativeID: fmt.Sprintf("canonical-%d.flac", i), Title: name + " Track", Album: name + " Album", PrimaryArtist: source.ArtistReference{Name: name, MBID: mbid}, AlbumCredits: []source.ArtistCredit{{Name: name, MBID: mbid}}, TrackCredits: []source.ArtistCredit{{Name: name, MBID: mbid}}, Container: "flac", Codec: "flac", Version: 1}
		if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	var firstID, secondID string
	_ = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id='canonical-one'`).Scan(&firstID)
	_ = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id='canonical-two'`).Scan(&secondID)
	applySeq, _ := s.NextScanSeq(ctx)
	if _, err := s.ApplyMusicBrainzArtistMetadata(ctx, firstID, "canonical-one", "Canonical 1", []string{"Shared Evidence"}, nil, applySeq); err != nil {
		t.Fatal(err)
	}
	local := source.TrackMeta{NativeID: "local.flac", Title: "Local Track", Album: "Local Album", PrimaryArtist: source.ArtistReference{Name: " shared evidence "}, AlbumCredits: []source.ArtistCredit{{Name: " shared evidence "}}, TrackCredits: []source.ArtistCredit{{Name: "Shared Evidence"}}, Container: "flac", Codec: "flac", Version: 1}
	localSeq, _ := s.NextScanSeq(ctx)
	if err := s.UpsertTrack(ctx, "filesystem", local, "", localSeq); err != nil {
		t.Fatal(err)
	}
	var localID string
	if err := s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id IS NULL`).Scan(&localID); err != nil {
		t.Fatal(err)
	}
	applySeq, _ = s.NextScanSeq(ctx)
	if _, err := s.ApplyMusicBrainzArtistMetadata(ctx, secondID, "canonical-two", "Canonical 2", []string{"Shared Evidence"}, nil, applySeq); err != nil {
		t.Fatal(err)
	}
	var ownerID string
	if err := s.db.QueryRowContext(ctx, `SELECT artist_id FROM albums WHERE title='Local Album'`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if ownerID != localID {
		t.Fatalf("ambiguous local owner merged: got %s want %s", ownerID, localID)
	}
}

func TestCanonicalArtistNameOutranksAnotherArtistsAlias(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	for _, meta := range []source.TrackMeta{
		{NativeID: "dizzy.flac", Title: "Dizzy Track", Album: "Dizzy Album", PrimaryArtist: source.ArtistReference{Name: "Dizzy Gillespie", MBID: "dizzy-mbid"}, AlbumCredits: []source.ArtistCredit{{Name: "Dizzy Gillespie", MBID: "dizzy-mbid"}}, TrackCredits: []source.ArtistCredit{{Name: "Dizzy Gillespie", MBID: "dizzy-mbid"}}, Container: "flac", Codec: "flac", Version: 1},
		{NativeID: "charlie.flac", Title: "Charlie Track", Album: "Charlie Album", PrimaryArtist: source.ArtistReference{Name: "Charlie Parker", MBID: "charlie-mbid"}, AlbumCredits: []source.ArtistCredit{{Name: "Charlie Parker", MBID: "charlie-mbid"}}, TrackCredits: []source.ArtistCredit{{Name: "Charlie Parker", MBID: "charlie-mbid"}}, Container: "flac", Codec: "flac", Version: 1},
		{NativeID: "local.flac", Title: "Local Track", Album: "Local Album", PrimaryArtist: source.ArtistReference{Name: "Dizzy Gillespie"}, AlbumCredits: []source.ArtistCredit{{Name: "Dizzy Gillespie"}}, TrackCredits: []source.ArtistCredit{{Name: "Dizzy Gillespie"}}, Container: "flac", Codec: "flac", Version: 1},
	} {
		if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	var dizzyID, charlieID string
	_ = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id='dizzy-mbid'`).Scan(&dizzyID)
	_ = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id='charlie-mbid'`).Scan(&charlieID)
	metadataSeq, _ := s.NextScanSeq(ctx)
	if _, err := s.PersistMusicBrainzArtistMetadata(ctx, dizzyID, "dizzy-mbid", "Dizzy Gillespie", nil, nil, metadataSeq); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PersistMusicBrainzArtistMetadata(ctx, charlieID, "charlie-mbid", "Charlie Parker", []string{"Dizzy Gillespie"}, nil, metadataSeq); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.ConsolidateMusicBrainzArtistsAtNextSequence(ctx); err != nil || !changed {
		t.Fatalf("consolidate changed=%v err=%v", changed, err)
	}
	var ownerID string
	if err := s.db.QueryRowContext(ctx, `SELECT artist_id FROM albums WHERE title='Local Album'`).Scan(&ownerID); err != nil || ownerID != dizzyID {
		t.Fatalf("local owner=%s want canonical=%s err=%v", ownerID, dizzyID, err)
	}
}

func TestApplyMusicBrainzArtistMetadataRollsBackUnsafeAlbumCollision(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	canonical := source.TrackMeta{NativeID: "canonical.flac", Title: "One", Album: "Same Album", PrimaryArtist: source.ArtistReference{Name: "Canonical", MBID: "canonical-mbid"}, AlbumCredits: []source.ArtistCredit{{Name: "Canonical", MBID: "canonical-mbid"}}, TrackCredits: []source.ArtistCredit{{Name: "Canonical", MBID: "canonical-mbid"}}, Container: "flac", Codec: "flac", Version: 1}
	local := source.TrackMeta{NativeID: "local.flac", Title: "Two", Album: "Same Album", PrimaryArtist: source.ArtistReference{Name: "Local Alias"}, AlbumCredits: []source.ArtistCredit{{Name: "Local Alias"}}, TrackCredits: []source.ArtistCredit{{Name: "Local Alias"}}, Container: "flac", Codec: "flac", Version: 1}
	for _, meta := range []source.TrackMeta{canonical, local} {
		if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	var canonicalID, localID string
	_ = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id='canonical-mbid'`).Scan(&canonicalID)
	_ = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id IS NULL`).Scan(&localID)
	applySeq, _ := s.NextScanSeq(ctx)
	if changed, err := s.ApplyMusicBrainzArtistMetadata(ctx, canonicalID, "canonical-mbid", "Canonical", []string{"Local Alias"}, nil, applySeq); err == nil || changed {
		t.Fatalf("unsafe merge changed=%v err=%v", changed, err)
	}
	var owners int
	if err := s.db.QueryRowContext(ctx, `SELECT count(DISTINCT artist_id) FROM albums WHERE title='Same Album' AND artist_id IN (?,?)`, canonicalID, localID).Scan(&owners); err != nil || owners != 2 {
		t.Fatalf("collision rollback owners=%d err=%v", owners, err)
	}
}

func TestPersistMusicBrainzArtistMetadataDoesNotConsolidate(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	canonical := source.TrackMeta{NativeID: "canonical.flac", Title: "Canonical Track", Album: "Canonical Album", PrimaryArtist: source.ArtistReference{Name: "Canonical", MBID: "canonical-mbid"}, AlbumCredits: []source.ArtistCredit{{Name: "Canonical", MBID: "canonical-mbid"}}, TrackCredits: []source.ArtistCredit{{Name: "Canonical", MBID: "canonical-mbid"}}, Container: "flac", Codec: "flac", Version: 1}
	local := source.TrackMeta{NativeID: "local.flac", Title: "Local Track", Album: "Local Album", PrimaryArtist: source.ArtistReference{Name: "Local Alias"}, AlbumCredits: []source.ArtistCredit{{Name: "Local Alias"}}, TrackCredits: []source.ArtistCredit{{Name: "Local Alias"}}, Container: "flac", Codec: "flac", Version: 1}
	for _, meta := range []source.TrackMeta{canonical, local} {
		if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	var canonicalID, localID string
	_ = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id='canonical-mbid'`).Scan(&canonicalID)
	_ = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id IS NULL`).Scan(&localID)
	metadataSeq, _ := s.NextScanSeq(ctx)
	changed, err := s.PersistMusicBrainzArtistMetadata(ctx, canonicalID, "canonical-mbid", "Canonical", []string{"Local Alias"}, nil, metadataSeq)
	if err != nil || !changed {
		t.Fatalf("persist changed=%v err=%v", changed, err)
	}
	var ownerID string
	if err := s.db.QueryRowContext(ctx, `SELECT artist_id FROM albums WHERE title='Local Album'`).Scan(&ownerID); err != nil || ownerID != localID {
		t.Fatalf("metadata persistence consolidated owner=%q want %q err=%v", ownerID, localID, err)
	}
	pending, _, err := s.PendingMusicBrainzArtists(ctx, "", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("completed metadata remains pending=%+v err=%v", pending, err)
	}
}

func TestPersistMusicBrainzArtistMetadataStampsRenameDependents(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	meta := source.TrackMeta{NativeID: "track.flac", Title: "Track", Album: "Album", PrimaryArtist: source.ArtistReference{Name: "Old Name", MBID: "canonical-mbid"}, AlbumCredits: []source.ArtistCredit{{Name: "Old Name", MBID: "canonical-mbid"}}, TrackCredits: []source.ArtistCredit{{Name: "Old Name", MBID: "canonical-mbid"}}, Container: "flac", Codec: "flac", Version: 1}
	if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
		t.Fatal(err)
	}
	albums, _, _ := s.ListAlbums(ctx, "title", "", 10)
	tracks, _, _ := s.ListTracks(ctx, "title", "", 10)
	var artistID string
	_ = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id='canonical-mbid'`).Scan(&artistID)
	renameSeq, _ := s.NextScanSeq(ctx)
	changed, err := s.PersistMusicBrainzArtistMetadata(ctx, artistID, "canonical-mbid", "New Name", nil, nil, renameSeq)
	if err != nil || !changed {
		t.Fatalf("rename changed=%v err=%v", changed, err)
	}
	gotAlbums, _, _ := s.ListAlbums(ctx, "title", "", 10)
	gotTracks, _, _ := s.ListTracks(ctx, "title", "", 10)
	if gotAlbums[0].ArtistName != "New Name" || gotTracks[0].ArtistName != "New Name" {
		t.Fatalf("renamed album=%+v track=%+v", gotAlbums[0], gotTracks[0])
	}
	changes, _, err := s.ChangesSince(ctx, renameSeq-1, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{albums[0].AlbumID: false, tracks[0].TrackID: false}
	for _, change := range changes {
		if _, ok := want[change.ID]; ok {
			want[change.ID] = true
		}
	}
	for id, found := range want {
		if !found {
			t.Fatalf("rename dependent %s absent from changes: %+v", id, changes)
		}
	}
}

func TestConsolidateMusicBrainzArtistsStampsCreditParents(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	canonical := source.TrackMeta{
		NativeID: "canonical.flac", Title: "Canonical Track", Album: "Canonical Album",
		PrimaryArtist: source.ArtistReference{Name: "Canonical", MBID: "canonical-mbid"},
		AlbumCredits:  []source.ArtistCredit{{Name: "Canonical", MBID: "canonical-mbid"}, {Name: "Local Alias", JoinPhrase: " & "}},
		TrackCredits:  []source.ArtistCredit{{Name: "Canonical", MBID: "canonical-mbid"}, {Name: "Local Alias", JoinPhrase: " feat. "}},
		Container:     "flac", Codec: "flac", Version: 1,
	}
	local := source.TrackMeta{NativeID: "local.flac", Title: "Local Track", Album: "Local Album", PrimaryArtist: source.ArtistReference{Name: "Local Alias"}, AlbumCredits: []source.ArtistCredit{{Name: "Local Alias"}}, TrackCredits: []source.ArtistCredit{{Name: "Local Alias"}}, Container: "flac", Codec: "flac", Version: 1}
	for _, meta := range []source.TrackMeta{canonical, local} {
		if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	albums, _, _ := s.ListAlbums(ctx, "title", "", 10)
	tracks, _, _ := s.ListTracks(ctx, "title", "", 10)
	var canonicalID string
	_ = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id='canonical-mbid'`).Scan(&canonicalID)
	metadataSeq, _ := s.NextScanSeq(ctx)
	if _, err := s.PersistMusicBrainzArtistMetadata(ctx, canonicalID, "canonical-mbid", "Canonical", []string{"Local Alias"}, nil, metadataSeq); err != nil {
		t.Fatal(err)
	}
	consolidationSeq, _ := s.NextScanSeq(ctx)
	changed, err := s.ConsolidateMusicBrainzArtists(ctx, consolidationSeq)
	if err != nil || !changed {
		t.Fatalf("consolidate changed=%v err=%v", changed, err)
	}
	var canonicalAlbumID, canonicalTrackID string
	for _, album := range albums {
		if album.Title == "Canonical Album" {
			canonicalAlbumID = album.AlbumID
		}
	}
	for _, track := range tracks {
		if track.Title == "Canonical Track" {
			canonicalTrackID = track.TrackID
		}
	}
	changes, _, err := s.ChangesSince(ctx, consolidationSeq-1, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{canonicalAlbumID: false, canonicalTrackID: false}
	for _, change := range changes {
		if _, ok := want[change.ID]; ok {
			want[change.ID] = true
		}
	}
	for id, found := range want {
		if !found {
			t.Fatalf("credit-only parent %s absent from changes: %+v", id, changes)
		}
	}
	gotTracks, _, _ := s.ListTracks(ctx, "title", "", 10)
	for _, track := range gotTracks {
		if track.Title == "Canonical Track" {
			if len(track.ArtistCredits) != 2 || track.ArtistCredits[1].Name != "Local Alias" || track.ArtistCredits[1].JoinPhrase != " feat. " {
				t.Fatalf("track credit display changed: %+v", track.ArtistCredits)
			}
		}
	}
}

func TestConsolidateMusicBrainzArtistsSkipsCollidingGroupAndMergesSafeGroup(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	metas := []source.TrackMeta{
		{NativeID: "bad-canonical.flac", Title: "One", Album: "Collision", PrimaryArtist: source.ArtistReference{Name: "Bad Canonical", MBID: "bad-mbid"}, AlbumCredits: []source.ArtistCredit{{Name: "Bad Canonical", MBID: "bad-mbid"}}, TrackCredits: []source.ArtistCredit{{Name: "Bad Canonical", MBID: "bad-mbid"}}, Container: "flac", Codec: "flac", Version: 1},
		{NativeID: "bad-local.flac", Title: "Two", Album: "Collision", PrimaryArtist: source.ArtistReference{Name: "Bad Alias"}, AlbumCredits: []source.ArtistCredit{{Name: "Bad Alias"}}, TrackCredits: []source.ArtistCredit{{Name: "Bad Alias"}}, Container: "flac", Codec: "flac", Version: 1},
		{NativeID: "safe-canonical.flac", Title: "Three", Album: "Safe Canonical Album", PrimaryArtist: source.ArtistReference{Name: "Safe Canonical", MBID: "safe-mbid"}, AlbumCredits: []source.ArtistCredit{{Name: "Safe Canonical", MBID: "safe-mbid"}}, TrackCredits: []source.ArtistCredit{{Name: "Safe Canonical", MBID: "safe-mbid"}}, Container: "flac", Codec: "flac", Version: 1},
		{NativeID: "safe-local.flac", Title: "Four", Album: "Safe Local Album", PrimaryArtist: source.ArtistReference{Name: "Safe Alias"}, AlbumCredits: []source.ArtistCredit{{Name: "Safe Alias"}}, TrackCredits: []source.ArtistCredit{{Name: "Safe Alias"}}, Container: "flac", Codec: "flac", Version: 1},
	}
	for _, meta := range metas {
		if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	for _, identity := range []struct {
		mbid, name, alias string
	}{{"bad-mbid", "Bad Canonical", "Bad Alias"}, {"safe-mbid", "Safe Canonical", "Safe Alias"}} {
		var artistID string
		_ = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id=?`, identity.mbid).Scan(&artistID)
		metadataSeq, _ := s.NextScanSeq(ctx)
		if _, err := s.PersistMusicBrainzArtistMetadata(ctx, artistID, identity.mbid, identity.name, []string{identity.alias}, nil, metadataSeq); err != nil {
			t.Fatal(err)
		}
	}
	consolidationSeq, _ := s.NextScanSeq(ctx)
	changed, err := s.ConsolidateMusicBrainzArtists(ctx, consolidationSeq)
	if err != nil || !changed {
		t.Fatalf("global consolidation changed=%v err=%v", changed, err)
	}
	var badOwners, safeOwners int
	_ = s.db.QueryRowContext(ctx, `SELECT count(DISTINCT artist_id) FROM albums WHERE title='Collision'`).Scan(&badOwners)
	_ = s.db.QueryRowContext(ctx, `SELECT count(DISTINCT artist_id) FROM albums WHERE title IN ('Safe Canonical Album','Safe Local Album')`).Scan(&safeOwners)
	if badOwners != 2 || safeOwners != 1 {
		t.Fatalf("collision owners=%d safe owners=%d", badOwners, safeOwners)
	}
	pending, _, err := s.PendingMusicBrainzArtists(ctx, "", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("collision metadata retried pending=%+v err=%v", pending, err)
	}
}

func TestConsolidateMusicBrainzArtistsPublishesOneCompleteSequence(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	for i := 1; i <= 2; i++ {
		canonical := source.TrackMeta{NativeID: fmt.Sprintf("canonical-%d.flac", i), Title: fmt.Sprintf("Canonical Track %d", i), Album: fmt.Sprintf("Canonical Album %d", i), PrimaryArtist: source.ArtistReference{Name: fmt.Sprintf("Canonical %d", i), MBID: fmt.Sprintf("mbid-%d", i)}, AlbumCredits: []source.ArtistCredit{{Name: fmt.Sprintf("Canonical %d", i), MBID: fmt.Sprintf("mbid-%d", i)}}, TrackCredits: []source.ArtistCredit{{Name: fmt.Sprintf("Canonical %d", i), MBID: fmt.Sprintf("mbid-%d", i)}}, Container: "flac", Codec: "flac", Version: 1}
		local := source.TrackMeta{NativeID: fmt.Sprintf("local-%d.flac", i), Title: fmt.Sprintf("Local Track %d", i), Album: fmt.Sprintf("Local Album %d", i), PrimaryArtist: source.ArtistReference{Name: fmt.Sprintf("Alias %d", i)}, AlbumCredits: []source.ArtistCredit{{Name: fmt.Sprintf("Alias %d", i)}}, TrackCredits: []source.ArtistCredit{{Name: fmt.Sprintf("Alias %d", i)}}, Container: "flac", Codec: "flac", Version: 1}
		for _, meta := range []source.TrackMeta{canonical, local} {
			if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
				t.Fatal(err)
			}
		}
		var artistID string
		_ = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id=?`, fmt.Sprintf("mbid-%d", i)).Scan(&artistID)
		metadataSeq, _ := s.NextScanSeq(ctx)
		if _, err := s.PersistMusicBrainzArtistMetadata(ctx, artistID, fmt.Sprintf("mbid-%d", i), fmt.Sprintf("Canonical %d", i), []string{fmt.Sprintf("Alias %d", i)}, nil, metadataSeq); err != nil {
			t.Fatal(err)
		}
	}
	consolidationSeq, _ := s.NextScanSeq(ctx)
	if changed, err := s.ConsolidateMusicBrainzArtists(ctx, consolidationSeq); err != nil || !changed {
		t.Fatalf("consolidate changed=%v err=%v", changed, err)
	}
	changes, cursor, err := s.ChangesSince(ctx, consolidationSeq-1, "", 1)
	seen := append([]LibraryChange(nil), changes...)
	for err == nil && cursor != "" {
		changes, cursor, err = s.ChangesSince(ctx, consolidationSeq-1, cursor, 1)
		seen = append(seen, changes...)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) < 8 {
		t.Fatalf("paged consolidation changes=%+v", seen)
	}
	for _, change := range seen {
		if change.ChangeSeq != consolidationSeq {
			t.Fatalf("change %s sequence=%d want %d", change.ID, change.ChangeSeq, consolidationSeq)
		}
	}
	if changed, err := s.ConsolidateMusicBrainzArtists(ctx, consolidationSeq); err != nil || changed {
		t.Fatalf("later same-sequence changes changed=%v err=%v", changed, err)
	}
}
