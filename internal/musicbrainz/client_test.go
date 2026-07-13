package musicbrainz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type memoryCache struct {
	mu      sync.Mutex
	entries map[string]CacheEntry
	putErr  error
}

func (m *memoryCache) MusicBrainzCache(_ context.Context, k string) (CacheEntry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[k]
	return e, ok, nil
}
func (m *memoryCache) PutMusicBrainzCache(_ context.Context, k string, e CacheEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.putErr != nil {
		return m.putErr
	}
	m.entries[k] = e
	return nil
}

func TestPersistenceFailureIsReturnedForGlobalPressureAndFinalError(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			persistErr := errors.New("disk full")
			cache := &memoryCache{entries: map[string]CacheEntry{}, putErr: persistErr}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			defer srv.Close()
			c, err := NewClient(Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: cache, Interval: time.Nanosecond, MaxRetries: -1})
			if err != nil {
				t.Fatal(err)
			}
			var out any
			if err := c.GetJSON(context.Background(), "/x", &out); !errors.Is(err, persistErr) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (f *fakeClock) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	f.mu.Lock()
	f.sleeps = append(f.sleeps, d)
	f.now = f.now.Add(d)
	f.mu.Unlock()
	return nil
}

func newTestClient(t *testing.T, base string, h http.HandlerFunc) (*Client, *fakeClock, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	clock := &fakeClock{now: time.Unix(100, 0)}
	c, err := NewClient(Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Clock: clock, Cache: &memoryCache{entries: map[string]CacheEntry{}}, Interval: time.Millisecond, BodyLimit: 64, MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	return c, clock, srv.Close
}

func TestCacheHitBypassesRequestAndLimiter(t *testing.T) {
	requests := 0
	c, clock, closeFn := newTestClient(t, "", func(w http.ResponseWriter, r *http.Request) { requests++; _, _ = w.Write([]byte(`{"ok":true}`)) })
	defer closeFn()
	c.cache = &memoryCache{entries: map[string]CacheEntry{c.base + "/x": {Status: 200, Body: []byte(`{"ok":true}`), FreshUntil: clock.Now().Add(time.Hour)}}}
	var out map[string]bool
	if err := c.GetJSON(context.Background(), "/x", &out); err != nil {
		t.Fatal(err)
	}
	if requests != 0 || len(clock.sleeps) != 0 {
		t.Fatalf("requests=%d sleeps=%v", requests, clock.sleeps)
	}
}

func TestGlobalPacingRetryAfterAndUserAgent(t *testing.T) {
	requests := 0
	c, clock, closeFn := newTestClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("User-Agent") != "BlitterServer/test (mailto:test@example.com)" {
			t.Errorf("user-agent=%q", r.Header.Get("User-Agent"))
		}
		if requests == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(429)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	defer closeFn()
	var out map[string]bool
	if err := c.GetJSON(context.Background(), "/x", &out); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
	if got := clock.sleeps[len(clock.sleeps)-1]; got < 2*time.Second {
		t.Fatalf("retry sleep=%v", got)
	}
}

func TestBodyLimitAndCancellation(t *testing.T) {
	c, _, closeFn := newTestClient(t, "", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(make([]byte, 65)) })
	defer closeFn()
	var out any
	if err := c.GetJSON(context.Background(), "/large", &out); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.GetJSON(ctx, "/cancel", &out); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestConcurrentConsumersShareFIFOStartPacing(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	var gotClock *fakeClock
	c, clock, closeFn := newTestClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		starts = append(starts, gotClock.Now())
		n := len(starts)
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	gotClock = clock
	c.interval = time.Second
	defer closeFn()
	var wg sync.WaitGroup
	for _, p := range []string{"/a", "/b", "/c"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out any
			if err := c.GetJSON(context.Background(), p, &out); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(starts) != 4 {
		t.Fatalf("starts=%v", starts)
	}
	for i := 1; i < len(starts); i++ {
		if starts[i].Before(starts[i-1].Add(time.Millisecond)) {
			t.Fatalf("attempts started without reserved spacing: %v", starts)
		}
	}
	if starts[1].Before(starts[0].Add(2 * time.Second)) {
		t.Fatalf("retry deadline did not delay the next actual request: %v", starts)
	}
}

func TestPersistedRetryDeadlineSurvivesClientRestart(t *testing.T) {
	cache := &memoryCache{entries: map[string]CacheEntry{}}
	clock := &fakeClock{now: time.Unix(100, 0)}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	newClient := func() *Client {
		c, err := NewClient(Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: cache, Clock: clock, Interval: time.Millisecond, MaxRetries: -1})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	var out any
	var backoff *BackoffError
	if err := newClient().GetJSON(context.Background(), "/x", &out); !errors.As(err, &backoff) {
		t.Fatalf("first error=%v", err)
	}
	if err := newClient().GetJSON(context.Background(), "/x", &out); !errors.As(err, &backoff) || requests != 1 {
		t.Fatalf("cached error=%v requests=%d", err, requests)
	}
	clock.mu.Lock()
	clock.now = backoff.RetryAt.Add(time.Millisecond)
	clock.mu.Unlock()
	_ = newClient().GetJSON(context.Background(), "/x", &out)
	if requests != 2 {
		t.Fatalf("requests after deadline=%d", requests)
	}
}
