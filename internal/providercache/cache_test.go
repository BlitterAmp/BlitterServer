package providercache

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPutGetRoundTripAndExpiry(t *testing.T) {
	root := t.TempDir()
	c := New(root)
	key, err := CanonicalKey("GET", "https://example.test/x?b=2&api_key=secret&a=1")
	if err != nil {
		t.Fatal(err)
	}
	want := Entry{Status: 200, MIME: "application/json", FetchedAt: time.Unix(10, 0), FreshUntil: time.Unix(20, 0), Kind: KindSuccess, Body: []byte(`{"ok":true}`)}
	if err := c.Put("fanart", key, want); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get("fanart", key)
	if !ok || got.Status != want.Status || string(got.Body) != string(want.Body) || got.Fresh(time.Unix(21, 0)) {
		t.Fatalf("got=%+v ok=%v", got, ok)
	}
	if !got.Fresh(time.Unix(19, 0)) {
		t.Fatal("entry should be fresh before deadline")
	}
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(body), "secret") || strings.Contains(string(body), "api_key") {
			t.Errorf("credential leaked into %s", path)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("file mode=%o", info.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCorruptEntryIsMissAndRemoved(t *testing.T) {
	c := New(t.TempDir())
	key, _ := CanonicalKey("GET", "https://example.test/x")
	path := c.path("musicbrainz", key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("musicbrainz", key); ok {
		t.Fatal("corrupt entry was a hit")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("corrupt entry remains: %v", err)
	}
}

func TestConcurrentPutGet(t *testing.T) {
	c := New(t.TempDir())
	key, _ := CanonicalKey("GET", "https://example.test/x")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := c.Put("caa", key, Entry{Kind: KindSuccess, Body: []byte("body")}); err != nil {
					t.Error(err)
				}
				c.Get("caa", key)
			}
		}()
	}
	wg.Wait()
}
