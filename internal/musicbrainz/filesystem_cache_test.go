package musicbrainz

import (
	"context"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/providercache"
)

func TestFilesystemCachePersistsAcrossInstances(t *testing.T) {
	root := t.TempDir()
	key := "https://musicbrainz.test/ws/2/release/x?fmt=json"
	want := CacheEntry{Status: 200, Body: []byte(`{"id":"x"}`), FetchedAt: time.Unix(1, 0), FreshUntil: time.Unix(2, 0), ETag: "tag"}
	if err := NewFilesystemCache(providercache.New(root)).PutMusicBrainzCache(context.Background(), key, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := NewFilesystemCache(providercache.New(root)).MusicBrainzCache(context.Background(), key)
	if err != nil || !ok || got.Status != want.Status || string(got.Body) != string(want.Body) || got.ETag != want.ETag {
		t.Fatalf("got=%+v ok=%v err=%v", got, ok, err)
	}
}
