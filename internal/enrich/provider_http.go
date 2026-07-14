package enrich

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/providercache"
)

func (e *Enricher) getJSON(ctx context.Context, u, userAgent string, out any) bool {
	return e.getJSONOutcome(ctx, u, userAgent, out) == lookupSuccess
}

func (e *Enricher) getJSONOutcome(ctx context.Context, u, userAgent string, out any) lookupOutcome {
	provider := e.provider(u)
	key, err := providercache.CanonicalKey(http.MethodGet, u)
	if err != nil {
		return lookupTransient
	}
	if e.cache != nil {
		if cached, ok := e.cache.Get(provider, key); ok && cached.Fresh(time.Now()) {
			if cached.Kind == providercache.KindMiss || cached.Status != http.StatusOK {
				return lookupMiss
			}
			if provider == "lastfm" {
				if outcome, semanticError := lastfmSemanticOutcome(cached.Body); semanticError {
					if outcome == lookupMiss {
						e.markMiss(ctx, provider, u)
						return outcome
					}
				} else if json.Unmarshal(cached.Body, out) == nil {
					return lookupSuccess
				} else {
					return lookupTransient
				}
				// Transient semantic errors from older cache entries are ignored
				// so this request can retry the provider.
			} else {
				if json.Unmarshal(cached.Body, out) != nil {
					return lookupTransient
				}
				return lookupSuccess
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return lookupTransient
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := e.do(provider, req)
	if err != nil {
		return lookupTransient
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			e.putCache(ctx, provider, key, providercache.Entry{URL: key, Status: resp.StatusCode, FetchedAt: time.Now(), FreshUntil: time.Now().Add(7 * 24 * time.Hour), Kind: providercache.KindMiss})
		}
		if resp.StatusCode == http.StatusNotFound {
			return lookupMiss
		}
		return lookupTransient
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return lookupTransient
	}
	if provider == "lastfm" {
		if outcome, semanticError := lastfmSemanticOutcome(body); semanticError {
			if outcome == lookupMiss {
				now := time.Now()
				e.putCache(ctx, provider, key, providercache.Entry{URL: key, Status: resp.StatusCode, FetchedAt: now, FreshUntil: now.Add(7 * 24 * time.Hour), Kind: providercache.KindMiss})
			}
			return outcome
		}
	}
	if json.Unmarshal(body, out) != nil {
		return lookupTransient
	}
	e.putCache(ctx, provider, key, providercache.Entry{URL: key, Status: resp.StatusCode, MIME: resp.Header.Get("Content-Type"), FetchedAt: time.Now(), FreshUntil: time.Now().Add(30 * 24 * time.Hour), Kind: providercache.KindSuccess, Body: body})
	return lookupSuccess
}

func lastfmSemanticOutcome(body []byte) (lookupOutcome, bool) {
	var envelope struct {
		Error *int `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Error == nil {
		return lookupSuccess, false
	}
	if *envelope.Error == 6 {
		return lookupMiss, true
	}
	return lookupTransient, true
}

// fetch downloads binary art; returns the bytes + content type.
func (e *Enricher) fetch(ctx context.Context, provider, u, userAgent string) ([]byte, string) {
	data, mime, _ := e.fetchOutcome(ctx, provider, u, userAgent)
	return data, mime
}

func (e *Enricher) fetchOutcome(ctx context.Context, provider, u, userAgent string) ([]byte, string, lookupOutcome) {
	key, err := providercache.CanonicalKey(http.MethodGet, u)
	if err != nil {
		return nil, "", lookupTransient
	}
	if e.cache != nil {
		if cached, ok := e.cache.Get(provider, key); ok && cached.Fresh(time.Now()) {
			if cached.Kind == providercache.KindMiss {
				return nil, "", lookupMiss
			}
			if cached.BlobHash != "" {
				data, err := os.ReadFile(filepath.Join(e.artDir, cached.BlobHash))
				if err == nil {
					return data, cached.MIME, lookupSuccess
				}
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", lookupTransient
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := e.do(provider, req)
	if err != nil {
		return nil, "", lookupTransient
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			e.putCache(ctx, provider, key, providercache.Entry{URL: key, Status: resp.StatusCode, FetchedAt: time.Now(), FreshUntil: time.Now().Add(7 * 24 * time.Hour), Kind: providercache.KindMiss})
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, "", lookupMiss
		}
		return nil, "", lookupTransient
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil || len(data) == 0 {
		return nil, "", lookupTransient
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" || !strings.HasPrefix(mime, "image/") {
		mime = "image/jpeg"
	}
	sum := sha256.Sum256(data)
	e.putCache(ctx, provider, key, providercache.Entry{URL: key, Status: resp.StatusCode, MIME: mime, FetchedAt: time.Now(), FreshUntil: time.Now().Add(30 * 24 * time.Hour), Kind: providercache.KindSuccess, BlobHash: hex.EncodeToString(sum[:])})
	return data, mime, lookupSuccess
}

func (e *Enricher) provider(u string) string {
	switch {
	case strings.HasPrefix(u, e.CAABase):
		return "caa"
	case strings.HasPrefix(u, e.FanartBase):
		return "fanart"
	case strings.HasPrefix(u, e.LastfmBase):
		return "lastfm"
	case strings.HasPrefix(u, e.DiscogsBase):
		return "discogs"
	default:
		return "images"
	}
}

func (e *Enricher) putCache(ctx context.Context, provider, key string, entry providercache.Entry) {
	if e.cache != nil {
		if err := e.cache.Put(provider, key, entry); err != nil {
			logging.From(ctx).Debug("provider cache write failed", "provider", provider, "err", err)
		}
	}
}

func (e *Enricher) markMiss(ctx context.Context, provider, rawURL string) {
	key, err := providercache.CanonicalKey(http.MethodGet, rawURL)
	if err == nil {
		now := time.Now()
		e.putCache(ctx, provider, key, providercache.Entry{URL: key, Status: http.StatusOK, FetchedAt: now, FreshUntil: now.Add(7 * 24 * time.Hour), Kind: providercache.KindMiss})
	}
}

func (e *Enricher) do(provider string, req *http.Request) (*http.Response, error) {
	if pacer := e.providerPacers[provider]; pacer != nil {
		if err := pacer.Wait(req.Context()); err != nil {
			return nil, err
		}
	}
	resp, err := e.http.Do(req)
	if err != nil || (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable) {
		return resp, err
	}
	delay := retryAfter(resp.Header.Get("Retry-After"), time.Now())
	resp.Body.Close()
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		if pacer := e.providerPacers[provider]; pacer != nil {
			if err := pacer.Wait(req.Context()); err != nil {
				return nil, err
			}
		}
		return e.http.Do(req)
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}
