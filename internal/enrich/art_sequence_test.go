package enrich

import (
	"context"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func TestArtworkAttachmentsUseFreshSequencesAcrossIdentity(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seq, _ := st.NextScanSeq(ctx)
	for i, album := range []string{"Before", "After One", "After Two"} {
		meta := source.TrackMeta{
			NativeID: string(rune('a' + i)), Title: album + " Track", Album: album,
			PrimaryArtist: source.ArtistReference{Name: "Local Name", MBID: "artist-mbid"},
			AlbumCredits:  []source.ArtistCredit{{Name: "Local Name", MBID: "artist-mbid"}},
			TrackCredits:  []source.ArtistCredit{{Name: "Local Name", MBID: "artist-mbid"}},
			Container:     "flac", Codec: "flac", Version: 1,
		}
		if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	albums, _, err := st.ListAlbums(ctx, "title", "", 10)
	if err != nil || len(albums) != 3 {
		t.Fatalf("albums=%+v err=%v", albums, err)
	}
	e := New(st, nil, t.TempDir(), Config{})
	if applied, err := e.attachAlbumArt(ctx, albums[0].AlbumID, "img-before"); err != nil || !applied {
		t.Fatalf("initial art applied=%v err=%v", applied, err)
	}
	beforeSummary, _ := st.GetLibrarySummary(ctx)
	artists, _, _ := st.ListArtists(ctx, "title", "", 10)
	if changed, err := st.PersistMusicBrainzArtistMetadataAtNextSequence(ctx, artists[0].ArtistID, "artist-mbid", "Canonical Name", nil); err != nil || !changed {
		t.Fatalf("identity changed=%v err=%v", changed, err)
	}
	identitySummary, _ := st.GetLibrarySummary(ctx)
	if identitySummary.Version <= beforeSummary.Version {
		t.Fatalf("identity version=%d initial art version=%d", identitySummary.Version, beforeSummary.Version)
	}

	versions := make([]int64, 0, 2)
	for i, album := range albums[1:] {
		if applied, err := e.attachAlbumArt(ctx, album.AlbumID, "img-after-"+string(rune('1'+i))); err != nil || !applied {
			t.Fatalf("post-identity art %d applied=%v err=%v", i, applied, err)
		}
		summary, err := st.GetLibrarySummary(ctx)
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, summary.Version)
	}
	if versions[0] <= identitySummary.Version || versions[1] <= versions[0] {
		t.Fatalf("versions initial=%d identity=%d post=%v", beforeSummary.Version, identitySummary.Version, versions)
	}
	changes, _, err := st.ChangesSince(ctx, identitySummary.Version, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, change := range changes {
		if change.Kind == "album" {
			seen[change.ChangeSeq] = true
		}
	}
	if !seen[versions[0]] || !seen[versions[1]] {
		t.Fatalf("post-identity artwork changes=%+v versions=%v", changes, versions)
	}
}
