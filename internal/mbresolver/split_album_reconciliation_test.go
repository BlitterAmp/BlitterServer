package mbresolver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/musicbrainz"
	"github.com/BlitterAmp/BlitterServer/internal/providercache"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func TestSplitAlbumReconciliationResolverReproducesSomethingRealFeddeLeGrand(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owners := []struct {
		name      string
		positions []int
	}{
		{"Fedde Le Grand", []int{9, 13, 14}},
		{"Fedde Le Grand And Merk & Kremont", []int{1}},
		{"Fedde Le Grand And Holl & Rush", []int{2}},
		{"Fedde Le Grand And Cobra Effect", []int{3}},
		{"Fedde Le Grand feat. MoZella", []int{4}},
		{"Fedde Le Grand feat. Jonathan Mendelsohn", []int{5, 7}},
		{"Fedde Le Grand feat. Erene", []int{6}},
		{"Fedde Le Grand feat. Bright Lights", []int{8}},
		{"Fedde Le Grand feat. Jared Lee", []int{10}},
		{"Fedde Le Grand feat. Denny White", []int{11}},
		{"Fedde Le Grand feat. Natalie La Rose", []int{12}},
	}
	titles := []string{"Give Me Some", "Feel Good", "I Can Feel", "Beauty From The Ashes", "Lost", "Immortal", "Miracle", "Feel Alive", "Keep On Believing", "Shadows", "Cinematic", "Down On Me", "The Noise (Yeah Baby)", "Tales of Tomorrow"}
	seq, _ := st.NextScanSeq(ctx)
	displays := map[string]string{}
	for _, owner := range owners {
		for _, position := range owner.positions {
			meta := source.TrackMeta{NativeID: fmt.Sprintf("something-real/%02d.flac", position), Title: titles[position-1], Album: "Something Real", Year: 2016, Disc: 1, Index: position, DurationMs: 200000 + position, PrimaryArtist: source.ArtistReference{Name: owner.name}, AlbumCredits: []source.ArtistCredit{{Name: owner.name}}, TrackCredits: []source.ArtistCredit{{Name: owner.name}}, Container: "flac", Codec: "flac", Version: 1}
			if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	beforeTracks, _, _ := st.ListTracks(ctx, "title", "", 100)
	for _, track := range beforeTracks {
		displays[track.TrackID] = track.ArtistName
	}
	due, err := st.DueMusicBrainzAlbums(ctx, time.Now(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var anchor store.MusicBrainzAlbum
	for _, album := range due {
		if album.PrimaryArtist.Name == "Fedde Le Grand" {
			anchor = album
		}
	}
	if anchor.AlbumID == "" {
		t.Fatal("Fedde anchor not found")
	}
	evidence := store.MusicBrainzCandidate{ReleaseID: "something-real-release", ReleaseGroupID: "something-real-group", Title: "Something Real", ArtistCredit: "Fedde Le Grand", Score: 95, Evidence: map[string]any{"candidate": "anchor"}}
	evidenceSeq, _ := st.NextScanSeq(ctx)
	changed, err := st.ApplyMusicBrainzConsensus(ctx, anchor, store.CanonicalRelease{ReleaseGroupID: "something-real-group", AlbumCredits: []source.ArtistCredit{{Name: "Fedde Le Grand", MBID: "fedde-mbid"}}}, evidenceSeq, evidence, []store.MusicBrainzCandidate{evidence}, time.Unix(0, 0))
	if err != nil || !changed {
		t.Fatalf("seed anchor evidence changed=%v err=%v", changed, err)
	}

	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		if r.URL.Path == "/release" {
			if !strings.Contains(r.URL.Query().Get("query"), `artist:"Fedde Le Grand"`) {
				_, _ = w.Write([]byte(`{"releases":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"releases":[{"id":"something-real-release","title":"Something Real","date":"2016","release-group":{"id":"something-real-group"},"artist-credit":[{"name":"Fedde Le Grand","artist":{"id":"fedde-mbid","name":"Fedde Le Grand"}}],"media":[{"position":1,"track-count":14}]}]}`))
			return
		}
		if r.URL.Path == "/release/something-real-release" {
			var tracks []string
			for i, title := range titles {
				tracks = append(tracks, fmt.Sprintf(`{"position":%d,"recording":{"id":"recording-%02d","title":%q,"length":%d}}`, i+1, i+1, title, 200001+i))
			}
			_, _ = fmt.Fprintf(w, `{"id":"something-real-release","title":"Something Real","date":"2016","release-group":{"id":"something-real-group"},"artist-credit":[{"name":"Fedde Le Grand","artist":{"id":"fedde-mbid","name":"Fedde Le Grand"}}],"media":[{"position":1,"tracks":[%s]}]}`, strings.Join(tracks, ","))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	client, err := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	changed, err = New(st, client).Run(ctx)
	if err != nil || !changed {
		t.Fatalf("resolver changed=%v err=%v requests=%v", changed, err, requests)
	}

	albums, _, err := st.ListAlbums(ctx, "title", "", 100)
	if err != nil || len(albums) != 1 {
		t.Fatalf("visible Something Real albums=%d want=1 err=%v: %+v", len(albums), err, albums)
	}
	if albums[0].ArtistName != "Fedde Le Grand" || albums[0].PrimaryArtist.MusicBrainzID != "fedde-mbid" {
		t.Fatalf("canonical survivor owner=%+v", albums[0])
	}
	afterTracks, err := st.ListAlbumTracks(ctx, albums[0].AlbumID)
	if err != nil || len(afterTracks) != 14 {
		t.Fatalf("survivor tracks=%d want=14 err=%v", len(afterTracks), err)
	}
	beforeIDs, afterIDs := make([]string, 0, 14), make([]string, 0, 14)
	for _, track := range beforeTracks {
		beforeIDs = append(beforeIDs, track.TrackID)
	}
	for _, track := range afterTracks {
		afterIDs = append(afterIDs, track.TrackID)
		if track.ArtistName != displays[track.TrackID] {
			t.Errorf("track %s display=%q want=%q", track.TrackID, track.ArtistName, displays[track.TrackID])
		}
	}
	sort.Strings(beforeIDs)
	sort.Strings(afterIDs)
	if fmt.Sprint(afterIDs) != fmt.Sprint(beforeIDs) {
		t.Fatalf("track IDs changed: before=%v after=%v", beforeIDs, afterIDs)
	}
	changes, _, version, err := st.ChangesSnapshot(ctx, evidenceSeq, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	missingAlbums, missingArtists, updatedTracks, removedTracks := 0, 0, 0, 0
	for _, change := range changes {
		if change.ChangeSeq != version {
			t.Errorf("change %s/%s seq=%d want transaction seq=%d", change.Kind, change.ID, change.ChangeSeq, version)
		}
		if change.Kind == "album" && change.Missing {
			missingAlbums++
		}
		if change.Kind == "artist" && change.Missing {
			missingArtists++
		}
		if change.Kind == "track" && !change.Missing {
			updatedTracks++
		}
		if change.Kind == "track" && change.Missing {
			removedTracks++
		}
	}
	if missingAlbums != 10 || missingArtists != 10 || updatedTracks != 14 || removedTracks != 0 {
		t.Fatalf("reconciliation deltas: missing albums=%d artists=%d updated tracks=%d removed tracks=%d changes=%+v", missingAlbums, missingArtists, updatedTracks, removedTracks, changes)
	}
}
