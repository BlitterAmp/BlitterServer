// Package providercache persists external provider responses independently of
// the resettable library database.
package providercache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Kind distinguishes successful responses from definitive provider misses.
type Kind string

const (
	// KindSuccess contains a usable response or image reference.
	KindSuccess Kind = "success"
	// KindMiss records a definitive absence that may be retried after its TTL.
	KindMiss Kind = "miss"
)

// Entry is one cached provider response. BlobHash points to an image in the
// shared art directory; Body is reserved for small metadata responses.
type Entry struct {
	URL          string    `json:"url,omitempty"`
	Status       int       `json:"status,omitempty"`
	MIME         string    `json:"mime,omitempty"`
	FetchedAt    time.Time `json:"fetched_at,omitempty"`
	FreshUntil   time.Time `json:"fresh_until,omitempty"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	RetryAt      time.Time `json:"retry_at,omitempty"`
	Error        string    `json:"error,omitempty"`
	Kind         Kind      `json:"kind"`
	BlobHash     string    `json:"blob_hash,omitempty"`
	Body         []byte    `json:"body,omitempty"`
}

// Fresh reports whether the entry's call-site-defined TTL has elapsed.
func (e Entry) Fresh(now time.Time) bool { return now.Before(e.FreshUntil) }

// Cache stores entries below root, separated by provider.
type Cache struct {
	root string
	mu   sync.RWMutex
}

// New creates a cache rooted at the supplied provider-cache directory.
func New(root string) *Cache { return &Cache{root: root} }

// CanonicalKey returns a credential-stripped canonical request URL. Query
// parameters are sorted by url.Values.Encode.
func CanonicalKey(method, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for key := range q {
		switch strings.ToLower(key) {
		case "api_key", "apikey", "token", "sk":
			q.Del(key)
		}
	}
	u.RawQuery = q.Encode()
	u.Fragment = ""
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return strings.ToUpper(method) + " " + u.String(), nil
}

func (c *Cache) path(provider, key string) string {
	sum := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(sum[:])
	return filepath.Join(c.root, safeProvider(provider), hash[:2], hash+".json")
}

func safeProvider(provider string) string {
	provider = strings.ToLower(provider)
	parts := strings.FieldsFunc(provider, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') })
	provider = strings.Join(parts, "-")
	if provider == "" {
		return "unknown"
	}
	return provider
}

// Get returns an entry when its file is readable and valid. Cache failures are
// misses so external provider availability never depends on the cache disk.
func (c *Cache) Get(provider, key string) (*Entry, bool) {
	c.mu.RLock()
	data, err := os.ReadFile(c.path(provider, key))
	c.mu.RUnlock()
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	var entry Entry
	decodeErr := json.Unmarshal(data, &entry)
	if err != nil || decodeErr != nil || entry.Kind == "" {
		slog.Debug("invalid provider cache entry", "provider", provider, "read_error", err, "decode_error", decodeErr)
		c.mu.Lock()
		removeErr := os.Remove(c.path(provider, key))
		c.mu.Unlock()
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			slog.Debug("remove corrupt provider cache entry", "provider", provider, "error", removeErr)
		}
		return nil, false
	}
	return &entry, true
}

// Put atomically writes one entry using a temporary file in the destination directory.
func (c *Cache) Put(provider, key string, entry Entry) error {
	path := c.path(provider, key)
	dir := filepath.Dir(path)
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
