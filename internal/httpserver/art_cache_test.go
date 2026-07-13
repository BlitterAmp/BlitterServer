package httpserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArtCacheEnforceBudgetEvictsColdest(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.jpg")
	newPath := filepath.Join(dir, "new.jpg")
	if err := os.WriteFile(oldPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("newer"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}

	c := newArtCache(dir, 5)
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("cold file not evicted: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new file evicted: %v", err)
	}
	if err := c.enforceBudget(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestArtCacheEnforcesBudgetAfterWrite(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.jpg")
	if err := os.WriteFile(oldPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}
	c := newArtCache(dir, 5)
	newPath := filepath.Join(dir, "new.jpg")
	if err := c.getOrCreate(context.Background(), newPath, func(path string) error {
		return os.WriteFile(path, []byte("newer"), 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("cold file not evicted after write: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new file evicted after write: %v", err)
	}
}

func TestArtCacheHitUpdatesRecency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hit.jpg")
	if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	c := newArtCache(dir, 1024)
	if hit := c.touch(path); !hit {
		t.Fatal("existing cache entry reported as miss")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().After(old) {
		t.Fatalf("cache hit did not update recency: %v", info.ModTime())
	}
}

func TestSnapArtDimensions(t *testing.T) {
	tests := []struct {
		name         string
		w, h         int
		wantW, wantH int
	}{
		{name: "original", wantW: 0, wantH: 0},
		{name: "small", w: 32, h: 90, wantW: 96, wantH: 96},
		{name: "middle", w: 97, h: 321, wantW: 320, wantH: 640},
		{name: "one dimension", w: 500, wantW: 640, wantH: 0},
		{name: "large is original", w: 1280, h: 100, wantW: 0, wantH: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotW, gotH := snapArtDimensions(tc.w, tc.h)
			if gotW != tc.wantW || gotH != tc.wantH {
				t.Fatalf("got %dx%d, want %dx%d", gotW, gotH, tc.wantW, tc.wantH)
			}
		})
	}
}
