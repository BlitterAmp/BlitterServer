// Package musicbrainz provides the single policy-enforcing adapter used for
// every request to the public MusicBrainz web service.
package musicbrainz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrBodyTooLarge = errors.New("musicbrainz response exceeds limit")

// BackoffError reports a persisted provider retry deadline.
type BackoffError struct {
	RetryAt    time.Time
	StatusCode int
}

func (e *BackoffError) Error() string {
	return "musicbrainz backoff until " + e.RetryAt.Format(time.RFC3339)
}

func (e *BackoffError) Unwrap() error {
	if e.StatusCode == 0 {
		return nil
	}
	return &HTTPStatusError{StatusCode: e.StatusCode}
}

// HTTPStatusError reports a non-success MusicBrainz response status.
type HTTPStatusError struct{ StatusCode int }

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("musicbrainz status %d", e.StatusCode)
}

type CacheEntry struct {
	Status                         int
	Body                           []byte
	FetchedAt, FreshUntil, RetryAt time.Time
	ETag, LastModified, Error      string
}

type Cache interface {
	MusicBrainzCache(context.Context, string) (CacheEntry, bool, error)
	PutMusicBrainzCache(context.Context, string, CacheEntry) error
}

type Clock interface {
	Now() time.Time
	Sleep(context.Context, time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type Options struct {
	BaseURL, UserAgent                string
	HTTP                              *http.Client
	Cache                             Cache
	Clock                             Clock
	Interval, FreshFor, ErrorFreshFor time.Duration
	BodyLimit                         int64
	MaxRetries                        int
}

type Client struct {
	base, userAgent                   string
	http                              *http.Client
	cache                             Cache
	clock                             Clock
	interval, freshFor, errorFreshFor time.Duration
	bodyLimit                         int64
	maxRetries                        int
	mu                                sync.Mutex
	gate                              chan struct{}
	nextStart, retryDeadline          time.Time
}

func NewClient(o Options) (*Client, error) {
	if !strings.Contains(o.UserAgent, "/") || (!strings.Contains(o.UserAgent, "http") && !strings.Contains(o.UserAgent, "mailto:")) {
		return nil, errors.New("musicbrainz user agent must be versioned and contactable")
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.Interval == 0 {
		o.Interval = 1100 * time.Millisecond
	} else if o.Interval < 0 {
		o.Interval = 0
	}
	if o.FreshFor == 0 {
		o.FreshFor = 30 * 24 * time.Hour
	}
	if o.ErrorFreshFor == 0 {
		o.ErrorFreshFor = 24 * time.Hour
	}
	if o.BodyLimit == 0 {
		o.BodyLimit = 4 << 20
	}
	if o.MaxRetries == 0 {
		o.MaxRetries = 2
	}
	c := &Client{base: strings.TrimRight(o.BaseURL, "/"), userAgent: o.UserAgent, http: o.HTTP, cache: o.Cache, clock: o.Clock, interval: o.Interval, freshFor: o.FreshFor, errorFreshFor: o.ErrorFreshFor, bodyLimit: o.BodyLimit, maxRetries: o.MaxRetries, gate: make(chan struct{}, 1)}
	c.gate <- struct{}{}
	return c, nil
}

func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	key := c.base + path
	now := c.clock.Now()
	var cached CacheEntry
	if c.cache != nil {
		global, ok, err := c.cache.MusicBrainzCache(ctx, c.base+"/.global-retry")
		if err != nil {
			return err
		}
		if ok && now.Before(global.RetryAt) {
			c.extendRetry(global.RetryAt)
			return &BackoffError{RetryAt: global.RetryAt, StatusCode: global.Status}
		}
		entry, ok, err := c.cache.MusicBrainzCache(ctx, key)
		if err != nil {
			return err
		}
		if ok {
			cached = entry
			if now.Before(entry.RetryAt) {
				c.extendRetry(entry.RetryAt)
				return &BackoffError{RetryAt: entry.RetryAt, StatusCode: entry.Status}
			}
			if now.Before(entry.FreshUntil) {
				if entry.Status != http.StatusOK {
					return &HTTPStatusError{StatusCode: entry.Status}
				}
				return json.Unmarshal(entry.Body, out)
			}
		}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, key, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")
		if cached.ETag != "" {
			req.Header.Set("If-None-Match", cached.ETag)
		}
		if cached.LastModified != "" {
			req.Header.Set("If-Modified-Since", cached.LastModified)
		}
		resp, err := c.doAttempt(ctx, req, attempt)
		if err != nil {
			return err
		}
		body, readErr := readLimited(resp.Body, c.bodyLimit)
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		now = c.clock.Now()
		if resp.StatusCode == http.StatusNotModified && len(cached.Body) > 0 {
			cached.FetchedAt, cached.FreshUntil = now, now.Add(c.freshFor)
			if err := c.put(ctx, key, cached); err != nil {
				return fmt.Errorf("persist musicbrainz revalidation: %w", err)
			}
			return json.Unmarshal(cached.Body, out)
		}
		entry := CacheEntry{Status: resp.StatusCode, Body: body, FetchedAt: now, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}
		if resp.StatusCode == http.StatusOK {
			entry.FreshUntil = now.Add(c.freshFor)
			if err := c.put(ctx, key, entry); err != nil {
				return err
			}
			return json.Unmarshal(body, out)
		}
		transient := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable
		if transient {
			delay := retryAfter(resp.Header.Get("Retry-After"), now)
			if delay <= 0 {
				delay = time.Duration(1<<attempt) * c.interval
			}
			c.extendRetry(now.Add(delay))
			if err := c.put(ctx, c.base+"/.global-retry", CacheEntry{Status: resp.StatusCode, RetryAt: c.globalRetry()}); err != nil {
				return fmt.Errorf("persist musicbrainz global backoff: %w", err)
			}
			if attempt < c.maxRetries {
				continue
			}
		}
		if !transient {
			entry.FreshUntil = now.Add(c.errorFreshFor)
		}
		entry.Error = http.StatusText(resp.StatusCode)
		entry.RetryAt = c.globalRetry()
		if err := c.put(ctx, key, entry); err != nil {
			return fmt.Errorf("persist musicbrainz response: %w", err)
		}
		if transient {
			return &BackoffError{RetryAt: entry.RetryAt, StatusCode: entry.Status}
		}
		return &HTTPStatusError{StatusCode: resp.StatusCode}
	}
}

func (c *Client) put(ctx context.Context, key string, entry CacheEntry) error {
	if c.cache == nil {
		return nil
	}
	return c.cache.PutMusicBrainzCache(ctx, key, entry)
}
func (c *Client) doAttempt(ctx context.Context, req *http.Request, attempt int) (*http.Response, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.gate:
	}
	defer func() { c.gate <- struct{}{} }()
	for {
		c.mu.Lock()
		at := c.nextStart
		if c.retryDeadline.After(at) {
			at = c.retryDeadline
		}
		c.mu.Unlock()
		now := c.clock.Now()
		if !at.After(now) {
			c.mu.Lock()
			c.nextStart = now.Add(c.interval)
			c.mu.Unlock()
			resp, err := c.http.Do(req)
			if err == nil && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) {
				now = c.clock.Now()
				delay := retryAfter(resp.Header.Get("Retry-After"), now)
				if delay <= 0 {
					delay = time.Duration(1<<attempt) * c.interval
				}
				// Install provider pressure before releasing the process-wide gate,
				// otherwise a queued consumer can start in the response-handling gap.
				c.extendRetry(now.Add(delay))
			}
			return resp, err
		}
		if err := c.clock.Sleep(ctx, at.Sub(now)); err != nil {
			return nil, err
		}
	}
}
func (c *Client) extendRetry(at time.Time) {
	c.mu.Lock()
	if at.After(c.retryDeadline) {
		c.retryDeadline = at
	}
	c.mu.Unlock()
}
func (c *Client) globalRetry() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.retryDeadline }
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, ErrBodyTooLarge
	}
	return b, nil
}
func retryAfter(v string, now time.Time) time.Duration {
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}
