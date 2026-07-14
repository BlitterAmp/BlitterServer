package enrich

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/providercache"
)

const (
	defaultDiscogsBase = "https://api.discogs.com"
	discogsJSONAccept  = "application/vnd.discogs.v2.discogs+json"
)

var discogsProcessPacer = newProviderPacer(time.Second)

type discogsSearchResponse struct {
	Results []struct {
		ID    int64  `json:"id"`
		Type  string `json:"type"`
		Title string `json:"title"`
	} `json:"results"`
}

type discogsImage struct {
	Type string `json:"type"`
	URI  string `json:"uri"`
}

func (e *Enricher) discogsAlbumArtOutcome(ctx context.Context, artist, album string) ([]byte, string, lookupOutcome) {
	token := e.discogsToken(ctx)
	if token == "" {
		return nil, "", lookupMiss
	}
	u, err := url.Parse(e.DiscogsBase + "/database/search")
	if err != nil {
		return nil, "", lookupTransient
	}
	q := u.Query()
	q.Set("type", "master")
	q.Set("artist", artist)
	q.Set("release_title", album)
	q.Set("page", "1")
	q.Set("per_page", "5")
	u.RawQuery = q.Encode()
	var search discogsSearchResponse
	if outcome := e.discogsGetJSON(ctx, token, u.String(), &search); outcome != lookupSuccess {
		return nil, "", outcome
	}
	want := strings.TrimSpace(artist) + " - " + strings.TrimSpace(album)
	var id int64
	for _, result := range search.Results {
		if result.Type == "master" && exactDiscogsText(result.Title, want) {
			if id != 0 {
				e.markMiss(ctx, "discogs", u.String())
				return nil, "", lookupMiss
			}
			id = result.ID
		}
	}
	if id == 0 {
		e.markMiss(ctx, "discogs", u.String())
		return nil, "", lookupMiss
	}
	detailURL := fmt.Sprintf("%s/masters/%d", e.DiscogsBase, id)
	var detail struct {
		Title   string `json:"title"`
		Artists []struct {
			Name string `json:"name"`
		} `json:"artists"`
		Images []discogsImage `json:"images"`
	}
	if outcome := e.discogsGetJSON(ctx, token, detailURL, &detail); outcome != lookupSuccess {
		return nil, "", outcome
	}
	if !exactDiscogsText(detail.Title, album) || len(detail.Artists) == 0 || !exactDiscogsText(detail.Artists[0].Name, artist) {
		e.markMiss(ctx, "discogs", detailURL)
		return nil, "", lookupMiss
	}
	image := primaryDiscogsImage(detail.Images)
	if image == "" {
		e.markMiss(ctx, "discogs", detailURL)
		return nil, "", lookupMiss
	}
	return e.discogsFetchImage(ctx, token, image)
}

func (e *Enricher) discogsArtistArtOutcome(ctx context.Context, name string) ([]byte, string, lookupOutcome) {
	token := e.discogsToken(ctx)
	if token == "" {
		return nil, "", lookupMiss
	}
	u, err := url.Parse(e.DiscogsBase + "/database/search")
	if err != nil {
		return nil, "", lookupTransient
	}
	q := u.Query()
	q.Set("type", "artist")
	q.Set("artist", name)
	q.Set("page", "1")
	q.Set("per_page", "5")
	u.RawQuery = q.Encode()
	var search discogsSearchResponse
	if outcome := e.discogsGetJSON(ctx, token, u.String(), &search); outcome != lookupSuccess {
		return nil, "", outcome
	}
	var id int64
	for _, result := range search.Results {
		if result.Type == "artist" && exactDiscogsText(result.Title, name) {
			if id != 0 {
				e.markMiss(ctx, "discogs", u.String())
				return nil, "", lookupMiss
			}
			id = result.ID
		}
	}
	if id == 0 {
		e.markMiss(ctx, "discogs", u.String())
		return nil, "", lookupMiss
	}
	detailURL := fmt.Sprintf("%s/artists/%d", e.DiscogsBase, id)
	var detail struct {
		Name   string         `json:"name"`
		Images []discogsImage `json:"images"`
	}
	if outcome := e.discogsGetJSON(ctx, token, detailURL, &detail); outcome != lookupSuccess {
		return nil, "", outcome
	}
	if !exactDiscogsText(detail.Name, name) {
		e.markMiss(ctx, "discogs", detailURL)
		return nil, "", lookupMiss
	}
	image := primaryDiscogsImage(detail.Images)
	if image == "" {
		e.markMiss(ctx, "discogs", detailURL)
		return nil, "", lookupMiss
	}
	return e.discogsFetchImage(ctx, token, image)
}

func (e *Enricher) discogsGetJSON(ctx context.Context, token, rawURL string, out any) lookupOutcome {
	key, err := providercache.CanonicalKey(http.MethodGet, rawURL)
	if err != nil {
		return lookupTransient
	}
	if e.cache != nil {
		if cached, ok := e.cache.Get("discogs", key); ok && cached.Fresh(time.Now()) {
			if cached.Kind == providercache.KindMiss || cached.Status == http.StatusNotFound {
				return lookupMiss
			}
			if cached.Status != http.StatusOK || json.Unmarshal(cached.Body, out) != nil {
				return lookupTransient
			}
			return lookupSuccess
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return lookupTransient
	}
	e.setDiscogsHeaders(req, token, true)
	resp, err := e.do("discogs", req)
	if err != nil {
		return lookupTransient
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		now := time.Now()
		e.putCache(ctx, "discogs", key, providercache.Entry{URL: key, Status: resp.StatusCode, FetchedAt: now, FreshUntil: now.Add(7 * 24 * time.Hour), Kind: providercache.KindMiss})
		return lookupMiss
	}
	if resp.StatusCode != http.StatusOK {
		return lookupTransient
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || json.Unmarshal(body, out) != nil {
		return lookupTransient
	}
	now := time.Now()
	e.putCache(ctx, "discogs", key, providercache.Entry{URL: key, Status: resp.StatusCode, MIME: resp.Header.Get("Content-Type"), FetchedAt: now, FreshUntil: now.Add(30 * 24 * time.Hour), Kind: providercache.KindSuccess, Body: body})
	return lookupSuccess
}

func (e *Enricher) discogsFetchImage(ctx context.Context, token, rawURL string) ([]byte, string, lookupOutcome) {
	if !e.discogsImageAllowed(rawURL) {
		return nil, "", lookupTransient
	}
	key, err := providercache.CanonicalKey(http.MethodGet, rawURL)
	if err != nil {
		return nil, "", lookupTransient
	}
	if e.cache != nil {
		if cached, ok := e.cache.Get("discogs", key); ok && cached.Fresh(time.Now()) {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", lookupTransient
	}
	e.setDiscogsHeaders(req, token, false)
	resp, err := e.do("discogs", req)
	if err != nil {
		return nil, "", lookupTransient
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		now := time.Now()
		e.putCache(ctx, "discogs", key, providercache.Entry{URL: key, Status: resp.StatusCode, FetchedAt: now, FreshUntil: now.Add(7 * 24 * time.Hour), Kind: providercache.KindMiss})
		return nil, "", lookupMiss
	}
	if resp.StatusCode != http.StatusOK {
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
	now := time.Now()
	e.putCache(ctx, "discogs", key, providercache.Entry{URL: key, Status: resp.StatusCode, MIME: mime, FetchedAt: now, FreshUntil: now.Add(30 * 24 * time.Hour), Kind: providercache.KindSuccess, BlobHash: hex.EncodeToString(sum[:])})
	return data, mime, lookupSuccess
}

func (e *Enricher) setDiscogsHeaders(req *http.Request, token string, metadata bool) {
	req.Header.Set("Authorization", "Discogs token="+token)
	req.Header.Set("User-Agent", e.cfg.DiscogsUserAgent)
	if metadata {
		req.Header.Set("Accept", discogsJSONAccept)
	}
}

func (e *Enricher) discogsImageAllowed(rawURL string) bool {
	image, err := url.Parse(rawURL)
	if err != nil || image.Host == "" {
		return false
	}
	base, err := url.Parse(e.DiscogsBase)
	if err != nil || base.Host == "" {
		return false
	}
	if base.Host != "api.discogs.com" {
		return image.Scheme == base.Scheme && image.Host == base.Host
	}
	host := strings.ToLower(image.Hostname())
	return image.Scheme == "https" && (host == "discogs.com" || strings.HasSuffix(host, ".discogs.com"))
}

func exactDiscogsText(got, want string) bool {
	return strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want))
}

func primaryDiscogsImage(images []discogsImage) string {
	for _, image := range images {
		if image.Type == "primary" && image.URI != "" {
			return image.URI
		}
	}
	return ""
}
