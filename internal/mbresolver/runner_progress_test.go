package mbresolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/musicbrainz"
	"github.com/BlitterAmp/BlitterServer/internal/providercache"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func TestResolverCancellationDoesNotCountCancelledAlbum(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seq, _ := st.NextScanSeq(context.Background())
	credit := source.ArtistCredit{Name: "Artist"}
	meta := source.TrackMeta{NativeID: "one.flac", Title: "One", Album: "Album", ReleaseMBID: "release", PrimaryArtist: source.ArtistReference{Name: "Artist"}, TrackCredits: []source.ArtistCredit{credit}, AlbumCredits: []source.ArtistCredit{credit}, Container: "flac", Codec: "flac"}
	if err := st.UpsertTrack(context.Background(), "filesystem", meta, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScan(context.Background(), "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { cancel(); <-ctx.Done() }))
	defer server.Close()
	client, err := musicbrainz.NewClient(musicbrainz.Options{BaseURL: server.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	callbacks := 0
	var observed ResolutionProgress
	_, err = New(st, client).RunWithProgress(ctx, func(progress ResolutionProgress) { callbacks++; observed = progress })
	if err == nil {
		t.Fatal("cancelled resolver returned nil error")
	}
	if callbacks != 0 || observed.Processed != 0 || observed.Failed != 0 {
		t.Fatalf("cancelled album counted: callbacks=%d progress=%+v", callbacks, observed)
	}
}
