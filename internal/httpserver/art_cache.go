package httpserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/logging"
)

const defaultArtCacheBudget = int64(1) << 30

type artCache struct {
	root   string
	budget int64
	mu     sync.Mutex
}

func newArtCache(root string, budget int64) *artCache {
	c := &artCache{root: root, budget: budget}
	if err := c.enforceBudget(context.Background()); err != nil {
		logging.From(context.Background()).Warn("art cache startup sweep", "err", err)
	}
	return c
}

func (c *artCache) touch(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return touchArtCacheFile(path)
}

func touchArtCacheFile(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	now := time.Now()
	return os.Chtimes(path, now, now) == nil
}

func (c *artCache) getOrCreate(ctx context.Context, path string, create func(string) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if touchArtCacheFile(path) {
		return nil
	}
	if err := create(path); err != nil {
		return err
	}
	return c.enforceBudgetLocked(ctx)
}

func (c *artCache) enforceBudget(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enforceBudgetLocked(ctx)
}

func (c *artCache) enforceBudgetLocked(ctx context.Context) error {
	entries, err := os.ReadDir(c.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type cacheFile struct {
		path    string
		size    int64
		modTime time.Time
	}
	var files []cacheFile
	var used int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		used += info.Size()
		files = append(files, cacheFile{
			path: filepath.Join(c.root, entry.Name()), size: info.Size(), modTime: info.ModTime(),
		})
	}
	if used <= c.budget {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	for _, file := range files {
		if used <= c.budget {
			break
		}
		if err := os.Remove(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		used -= file.size
		logging.From(ctx).Debug("art cache entry evicted", "path", file.path, "bytes", file.size)
	}
	return nil
}

func snapArtDimensions(w, h int) (int, int) {
	if w >= 1280 || h >= 1280 {
		return 0, 0
	}
	return snapArtDimension(w), snapArtDimension(h)
}

func snapArtDimension(value int) int {
	switch {
	case value == 0:
		return 0
	case value <= 96:
		return 96
	case value <= 320:
		return 320
	case value <= 640:
		return 640
	default:
		return 1280
	}
}
