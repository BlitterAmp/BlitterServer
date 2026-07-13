package musicbrainz

import (
	"context"
	"net/http"

	"github.com/BlitterAmp/BlitterServer/internal/providercache"
)

// FilesystemCache adapts the shared provider cache to the MusicBrainz client cache port.
type FilesystemCache struct{ cache *providercache.Cache }

// NewFilesystemCache creates a MusicBrainz cache adapter.
func NewFilesystemCache(cache *providercache.Cache) *FilesystemCache {
	return &FilesystemCache{cache: cache}
}

func (c *FilesystemCache) MusicBrainzCache(_ context.Context, rawKey string) (CacheEntry, bool, error) {
	key, err := providercache.CanonicalKey(http.MethodGet, rawKey)
	if err != nil {
		return CacheEntry{}, false, err
	}
	entry, ok := c.cache.Get("musicbrainz", key)
	if !ok {
		return CacheEntry{}, false, nil
	}
	return CacheEntry{Status: entry.Status, Body: entry.Body, FetchedAt: entry.FetchedAt, FreshUntil: entry.FreshUntil, RetryAt: entry.RetryAt, ETag: entry.ETag, LastModified: entry.LastModified, Error: entry.Error}, true, nil
}

func (c *FilesystemCache) PutMusicBrainzCache(_ context.Context, rawKey string, entry CacheEntry) error {
	key, err := providercache.CanonicalKey(http.MethodGet, rawKey)
	if err != nil {
		return err
	}
	return c.cache.Put("musicbrainz", key, providercache.Entry{URL: key, Status: entry.Status, Body: entry.Body, FetchedAt: entry.FetchedAt, FreshUntil: entry.FreshUntil, RetryAt: entry.RetryAt, ETag: entry.ETag, LastModified: entry.LastModified, Error: entry.Error, Kind: providercache.KindSuccess})
}
