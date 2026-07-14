package enrich

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/providercache"
)

const (
	testDiscogsToken = "discogs-secret-marker"
	testDiscogsUA    = "BlitterServer/test (https://github.com/BlitterAmp/BlitterServer)"
)

func newDiscogsTestEnricher(t *testing.T, baseURL string) (*Enricher, string) {
	t.Helper()
	st, _ := seedAlbum(t)
	dataDir := t.TempDir()
	e := New(st, nil, filepath.Join(dataDir, "art"), Config{
		DiscogsToken:     func(context.Context) string { return testDiscogsToken },
		DiscogsUserAgent: testDiscogsUA,
		ProviderCache:    providercache.New(filepath.Join(dataDir, "provider-cache")),
	})
	e.DiscogsBase = baseURL
	e.providerPacers["discogs"] = newProviderPacer(0)
	return e, dataDir
}

func assertDiscogsRequestHeaders(t *testing.T, r *http.Request, metadata bool) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Discogs token="+testDiscogsToken {
		t.Fatalf("Authorization=%q", got)
	}
	if got := r.Header.Get("User-Agent"); got != testDiscogsUA {
		t.Fatalf("User-Agent=%q", got)
	}
	if metadata {
		if got := r.Header.Get("Accept"); got != "application/vnd.discogs.v2.discogs+json" {
			t.Fatalf("Accept=%q", got)
		}
	}
	if strings.Contains(r.URL.RawQuery, testDiscogsToken) {
		t.Fatal("personal token leaked into query")
	}
}

func TestDiscogsAlbumSuccessUsesExactMasterAndCachesWithoutToken(t *testing.T) {
	var requests []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		switch r.URL.Path {
		case "/database/search":
			assertDiscogsRequestHeaders(t, r, true)
			q := r.URL.Query()
			if q.Get("type") != "master" || q.Get("artist") != "The Band" || q.Get("release_title") != "Great Album" || q.Get("page") != "1" || q.Get("per_page") != "5" {
				t.Fatalf("search query=%v", q)
			}
			_, _ = w.Write([]byte(`{"results":[{"id":41,"type":"release","title":"The Band - Great Album"},{"id":42,"type":"master","title":" the band - great album "}]}`))
		case "/masters/42":
			assertDiscogsRequestHeaders(t, r, true)
			_, _ = w.Write([]byte(`{"title":" GREAT ALBUM ","artists":[{"name":" the band "}],"images":[{"type":"secondary","uri":"` + server.URL + `/wrong"},{"type":"primary","uri":"` + server.URL + `/image?signature=a%2Fb%2Bc&expires=123"}]}`))
		case "/image":
			assertDiscogsRequestHeaders(t, r, false)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	e, dataDir := newDiscogsTestEnricher(t, server.URL)
	data, mime, outcome := e.discogsAlbumArtOutcome(context.Background(), "The Band", "Great Album")
	if outcome != lookupSuccess || string(data) != string(pngBytes) || mime != "image/png" {
		t.Fatalf("data=%q mime=%q outcome=%v", data, mime, outcome)
	}
	if got := requests[len(requests)-1]; got != "/image?signature=a%2Fb%2Bc&expires=123" {
		t.Fatalf("signed image URL changed: %q", got)
	}
	if got := e.providerPacers["discogs"].WaitCount(); got != 3 {
		t.Fatalf("shared pacer waits=%d", got)
	}
	if _, err := e.store(context.Background(), data, mime); err != nil {
		t.Fatal(err)
	}
	if data, _, outcome := e.discogsAlbumArtOutcome(context.Background(), "The Band", "Great Album"); data == nil || outcome != lookupSuccess || len(requests) != 3 {
		t.Fatalf("cached lookup data=%d outcome=%v requests=%d", len(data), outcome, len(requests))
	}
	if got := e.providerPacers["discogs"].WaitCount(); got != 3 {
		t.Fatalf("cache hits entered pacer: waits=%d", got)
	}
	if err := filepath.Walk(filepath.Join(dataDir, "provider-cache"), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(path, testDiscogsToken) || strings.Contains(string(body), testDiscogsToken) {
				t.Errorf("Discogs token leaked into provider cache %s", path)
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDiscogsArtistSuccessUsesExactResultAndPrimaryImage(t *testing.T) {
	var requests []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		switch r.URL.Path {
		case "/database/search":
			assertDiscogsRequestHeaders(t, r, true)
			q := r.URL.Query()
			if q.Get("type") != "artist" || q.Get("artist") != "Björk" || q.Get("page") != "1" || q.Get("per_page") != "5" {
				t.Fatalf("search query=%v", q)
			}
			_, _ = w.Write([]byte(`{"results":[{"id":7,"type":"artist","title":" björk "}]}`))
		case "/artists/7":
			assertDiscogsRequestHeaders(t, r, true)
			_, _ = w.Write([]byte(`{"name":"BJÖRK","images":[{"type":"secondary","uri":"` + server.URL + `/wrong"},{"type":"primary","uri":"` + server.URL + `/artist-image?sig=unchanged%2Fvalue"}]}`))
		case "/artist-image":
			assertDiscogsRequestHeaders(t, r, false)
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("artist-image"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	e, _ := newDiscogsTestEnricher(t, server.URL)
	data, mime, outcome := e.discogsArtistArtOutcome(context.Background(), "Björk")
	if outcome != lookupSuccess || string(data) != "artist-image" || mime != "image/jpeg" {
		t.Fatalf("data=%q mime=%q outcome=%v", data, mime, outcome)
	}
	if got := requests[len(requests)-1]; got != "/artist-image?sig=unchanged%2Fvalue" {
		t.Fatalf("signed image URL changed: %q", got)
	}
	if _, err := e.store(context.Background(), data, mime); err != nil {
		t.Fatal(err)
	}
	if data, _, outcome := e.discogsArtistArtOutcome(context.Background(), "Björk"); data == nil || outcome != lookupSuccess || len(requests) != 3 {
		t.Fatalf("cached artist lookup data=%d outcome=%v requests=%d", len(data), outcome, len(requests))
	}
}

func TestDiscogsAlbumRejectsNonExactOrUnusableResults(t *testing.T) {
	tests := []struct {
		name   string
		search string
		detail string
	}{
		{name: "ambiguous exact masters", search: `{"results":[{"id":1,"type":"master","title":"Artist - Album"},{"id":2,"type":"master","title":" artist - album "}]}`},
		{name: "fuzzy search title", search: `{"results":[{"id":1,"type":"master","title":"Artist - Album (Deluxe)"}]}`},
		{name: "detail album mismatch", search: `{"results":[{"id":1,"type":"master","title":"Artist - Album"}]}`, detail: `{"title":"Other","artists":[{"name":"Artist"}]}`},
		{name: "detail artist mismatch", search: `{"results":[{"id":1,"type":"master","title":"Artist - Album"}]}`, detail: `{"title":"Album","artists":[{"name":"Other"}]}`},
		{name: "no primary image", search: `{"results":[{"id":1,"type":"master","title":"Artist - Album"}]}`, detail: `{"title":"Album","artists":[{"name":"Artist"}],"images":[{"type":"secondary","uri":"https://i.discogs.com/wrong"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/database/search" {
					_, _ = w.Write([]byte(tt.search))
					return
				}
				_, _ = w.Write([]byte(tt.detail))
			}))
			defer srv.Close()
			e, _ := newDiscogsTestEnricher(t, srv.URL)
			if data, _, outcome := e.discogsAlbumArtOutcome(context.Background(), "Artist", "Album"); data != nil || outcome != lookupMiss {
				t.Fatalf("data=%q outcome=%v", data, outcome)
			}
		})
	}
}

func TestDiscogsArtistRejectsNonExactOrUnusableResults(t *testing.T) {
	tests := []struct {
		name   string
		search string
		detail string
	}{
		{name: "ambiguous", search: `{"results":[{"id":1,"type":"artist","title":"Artist"},{"id":2,"type":"artist","title":" artist "}]}`},
		{name: "fuzzy", search: `{"results":[{"id":1,"type":"artist","title":"Artist Band"}]}`},
		{name: "detail mismatch", search: `{"results":[{"id":1,"type":"artist","title":"Artist"}]}`, detail: `{"name":"Other"}`},
		{name: "no primary image", search: `{"results":[{"id":1,"type":"artist","title":"Artist"}]}`, detail: `{"name":"Artist","images":[{"type":"secondary","uri":"https://i.discogs.com/wrong"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/database/search" {
					_, _ = w.Write([]byte(tt.search))
					return
				}
				_, _ = w.Write([]byte(tt.detail))
			}))
			defer srv.Close()
			e, _ := newDiscogsTestEnricher(t, srv.URL)
			if data, _, outcome := e.discogsArtistArtOutcome(context.Background(), "Artist"); data != nil || outcome != lookupMiss {
				t.Fatalf("data=%q outcome=%v", data, outcome)
			}
		})
	}
}

func TestDiscogsCachesSemanticMisses(t *testing.T) {
	tests := []struct {
		name       string
		searchBody string
		detailBody string
		lookup     func(*Enricher) lookupOutcome
		wantPath   string
		wantCalls  int
	}{
		{
			name:       "search has no unique exact result",
			searchBody: `{"results":[{"id":1,"type":"artist","title":"Artist"},{"id":2,"type":"artist","title":" artist "}]}`,
			lookup: func(e *Enricher) lookupOutcome {
				_, _, outcome := e.discogsArtistArtOutcome(context.Background(), "Artist")
				return outcome
			},
			wantPath:  "/database/search",
			wantCalls: 1,
		},
		{
			name:       "detail identity mismatch",
			searchBody: `{"results":[{"id":1,"type":"artist","title":"Artist"}]}`,
			detailBody: `{"name":"Other","images":[{"type":"primary","uri":"https://i.discogs.com/image"}]}`,
			lookup: func(e *Enricher) lookupOutcome {
				_, _, outcome := e.discogsArtistArtOutcome(context.Background(), "Artist")
				return outcome
			},
			wantPath:  "/artists/1",
			wantCalls: 2,
		},
		{
			name:       "detail has no primary image",
			searchBody: `{"results":[{"id":1,"type":"master","title":"Artist - Album"}]}`,
			detailBody: `{"title":"Album","artists":[{"name":"Artist"}],"images":[{"type":"secondary","uri":"https://i.discogs.com/image"}]}`,
			lookup: func(e *Enricher) lookupOutcome {
				_, _, outcome := e.discogsAlbumArtOutcome(context.Background(), "Artist", "Album")
				return outcome
			},
			wantPath:  "/masters/1",
			wantCalls: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			var missURL string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path == tt.wantPath {
					missURL = "http://" + r.Host + r.URL.RequestURI()
				}
				if r.URL.Path == "/database/search" {
					_, _ = w.Write([]byte(tt.searchBody))
					return
				}
				_, _ = w.Write([]byte(tt.detailBody))
			}))
			defer srv.Close()

			e, _ := newDiscogsTestEnricher(t, srv.URL)
			before := time.Now()
			if outcome := tt.lookup(e); outcome != lookupMiss {
				t.Fatalf("outcome=%v", outcome)
			}
			if requests != tt.wantCalls || missURL == "" {
				t.Fatalf("requests=%d missURL=%q", requests, missURL)
			}
			key, err := providercache.CanonicalKey(http.MethodGet, missURL)
			if err != nil {
				t.Fatal(err)
			}
			entry, ok := e.cache.Get("discogs", key)
			if !ok || entry.Kind != providercache.KindMiss || entry.Status != http.StatusOK {
				t.Fatalf("semantic miss cache entry=%+v ok=%v", entry, ok)
			}
			if entry.FreshUntil.Before(before.Add(7*24*time.Hour-time.Minute)) || entry.FreshUntil.After(time.Now().Add(7*24*time.Hour+time.Minute)) {
				t.Fatalf("semantic miss FreshUntil=%s", entry.FreshUntil)
			}
			if outcome := tt.lookup(e); outcome != lookupMiss || requests != tt.wantCalls {
				t.Fatalf("repeat outcome=%v requests=%d", outcome, requests)
			}
		})
	}
}

func TestDiscogsClassifiesProviderFailures(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		outcome lookupOutcome
	}{
		{name: "not found", status: http.StatusNotFound, outcome: lookupMiss},
		{name: "unauthorized", status: http.StatusUnauthorized, outcome: lookupTransient},
		{name: "forbidden", status: http.StatusForbidden, outcome: lookupTransient},
		{name: "rate limited", status: http.StatusTooManyRequests, outcome: lookupTransient},
		{name: "server error", status: http.StatusInternalServerError, outcome: lookupTransient},
		{name: "unavailable", status: http.StatusServiceUnavailable, outcome: lookupTransient},
		{name: "malformed", status: http.StatusOK, body: `{`, outcome: lookupTransient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			e, _ := newDiscogsTestEnricher(t, srv.URL)
			ctx := context.Background()
			if tt.status == http.StatusTooManyRequests || tt.status == http.StatusServiceUnavailable {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			if data, _, outcome := e.discogsAlbumArtOutcome(ctx, "Artist", "Album"); data != nil || outcome != tt.outcome {
				t.Fatalf("data=%q outcome=%v want=%v", data, outcome, tt.outcome)
			}
		})
	}
}

func TestDiscogsDoesNotForwardTokenToUnrelatedImageHost(t *testing.T) {
	imageRequests := 0
	unrelated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imageRequests++
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("credential forwarded to unrelated host: %q", got)
		}
	}))
	defer unrelated.Close()

	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/database/search":
			_, _ = w.Write([]byte(`{"results":[{"id":1,"type":"master","title":"Artist - Album"}]}`))
		case "/masters/1":
			_, _ = fmt.Fprintf(w, `{"title":"Album","artists":[{"name":"Artist"}],"images":[{"type":"primary","uri":%q}]}`, unrelated.URL+"/image")
		}
	}))
	defer metadata.Close()

	e, _ := newDiscogsTestEnricher(t, metadata.URL)
	if data, _, outcome := e.discogsAlbumArtOutcome(context.Background(), "Artist", "Album"); data != nil || outcome != lookupTransient {
		t.Fatalf("data=%q outcome=%v", data, outcome)
	}
	if imageRequests != 0 {
		t.Fatalf("unrelated image host received %d requests", imageRequests)
	}
}
