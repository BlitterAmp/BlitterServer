// Package enrich fills gaps in album/artist art from public sources. After a
// scan, albums with no embedded cover and artists with no photo are looked up:
// MusicBrainz resolves ids, then Cover Art Archive / last.fm supply album covers
// and fanart.tv supplies artist images. Results are stored via the art cache and
// bump change_seq so clients delta-sync the new art. Entities are marked "tried"
// so we don't re-query every scan.
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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// perRun caps how many entities we resolve in one pass (MusicBrainz is ~1 req/s).
const perRun = 150

var musicBrainzRateGate sync.Mutex

// Config carries the credentials + identity enrichment needs. Keys are read
// from server settings (maintained via the admin API), so they're getters.
type Config struct {
	// LastfmKey returns the current last.fm API key ("" disables last.fm).
	LastfmKey func(context.Context) string
	// FanartKey returns the current fanart.tv key ("" disables artist photos).
	FanartKey func(context.Context) string
	// UserAgent identifies us to MusicBrainz (required by their policy).
	UserAgent string
}

// Enricher runs enrichment passes. Base URLs are fields so tests can point them
// at local servers.
type Enricher struct {
	st     *store.Store
	bus    *events.Bus
	artDir string
	http   *http.Client
	cfg    Config

	MBBase     string
	CAABase    string
	LastfmBase string
	FanartBase string

	mbInterval time.Duration
}

func New(st *store.Store, bus *events.Bus, artDir string, cfg Config) *Enricher {
	if cfg.UserAgent == "" {
		cfg.UserAgent = "BlitterServer/1.0 (https://github.com/BlitterAmp/BlitterServer)"
	}
	return &Enricher{
		st:         st,
		bus:        bus,
		artDir:     artDir,
		http:       &http.Client{Timeout: 20 * time.Second},
		cfg:        cfg,
		MBBase:     "https://musicbrainz.org/ws/2",
		CAABase:    "https://coverartarchive.org",
		LastfmBase: "https://ws.audioscrobbler.com/2.0",
		FanartBase: "https://webservice.fanart.tv/v3.2",
		mbInterval: 1100 * time.Millisecond,
	}
}

// Run does one enrichment pass over untried albums (then artists). Best-effort:
// failures just mark the entity tried and move on.
func (e *Enricher) Run(ctx context.Context) {
	log := logging.From(ctx).With("component", "enrich")
	var seq int64
	nextSeq := func() bool {
		if seq != 0 {
			return true
		}
		var err error
		seq, err = e.st.NextScanSeq(ctx)
		return err == nil
	}
	changed := false

	albums, _ := e.st.AlbumsNeedingArt(ctx, perRun)
	for _, a := range albums {
		if ctx.Err() != nil {
			return
		}
		if data, mime := e.albumArt(ctx, a.ArtistName, a.Title); data != nil {
			if id, err := e.store(ctx, data, mime); err == nil {
				if !nextSeq() {
					return
				}
				applied, err := e.st.SetAlbumArt(ctx, a.AlbumID, id, seq)
				changed = changed || applied
				if err != nil {
					_ = e.st.MarkAlbumArtTried(ctx, a.AlbumID)
				}
				continue
			}
		}
		_ = e.st.MarkAlbumArtTried(ctx, a.AlbumID)
	}

	if e.fanartKey(ctx) != "" {
		artists, _ := e.st.ArtistsNeedingArt(ctx, perRun)
		for _, ar := range artists {
			if ctx.Err() != nil {
				return
			}
			if data, mime := e.artistArt(ctx, ar.Name); data != nil {
				if id, err := e.store(ctx, data, mime); err == nil {
					if !nextSeq() {
						return
					}
					applied, err := e.st.SetArtistArt(ctx, ar.ArtistID, ar.ArtID, id, seq)
					changed = changed || applied
					if err != nil {
						_ = e.st.MarkArtistArtTried(ctx, ar.ArtistID)
					}
					continue
				}
			}
			_ = e.st.MarkArtistArtTried(ctx, ar.ArtistID)
		}
	}

	if changed && e.bus != nil {
		if sum, err := e.st.GetLibrarySummary(ctx); err == nil {
			_ = e.bus.Publish(ctx, "library.changed", "", map[string]any{"libraryId": "lib_local", "updatedAt": sum.UpdatedAt})
		}
		log.Info("art enrichment updated the library")
	}
}

func (e *Enricher) store(ctx context.Context, data []byte, mime string) (string, error) {
	sum := sha256.Sum256(data)
	return e.st.UpsertArt(ctx, hex.EncodeToString(sum[:]), mime, data, filepath.Join(e.artDir))
}

// ── album art ──

func (e *Enricher) albumArt(ctx context.Context, artist, album string) ([]byte, string) {
	if rg := e.mbReleaseGroup(ctx, artist, album); rg != "" {
		return e.albumArtForReleaseGroup(ctx, rg, artist, album)
	}
	return e.lastfmAlbumArt(ctx, artist, album)
}

func (e *Enricher) albumArtForReleaseGroup(ctx context.Context, releaseGroupID, artist, album string) ([]byte, string) {
	if data, mime := e.fetch(ctx, fmt.Sprintf("%s/release-group/%s/front-500", e.CAABase, releaseGroupID), ""); data != nil {
		return data, mime
	}
	if key := e.fanartKey(ctx); key != "" {
		if data, mime := e.fanartAlbumArt(ctx, key, releaseGroupID); data != nil {
			return data, mime
		}
	}
	return e.lastfmAlbumArt(ctx, artist, album)
}

func (e *Enricher) lastfmAlbumArt(ctx context.Context, artist, album string) ([]byte, string) {
	if key := e.lastfmKey(ctx); key != "" {
		if u := e.lastfmAlbumImage(ctx, key, artist, album); u != "" {
			return e.fetch(ctx, u, "")
		}
	}
	return nil, ""
}

func (e *Enricher) fanartAlbumArt(ctx context.Context, key, releaseGroupID string) ([]byte, string) {
	u := fmt.Sprintf("%s/music/albums/%s?api_key=%s", e.FanartBase, url.PathEscape(releaseGroupID), url.QueryEscape(key))
	var body struct {
		Albums []struct {
			ReleaseGroupID string `json:"release_group_id"`
			AlbumCover     []struct {
				URL string `json:"url"`
			} `json:"albumcover"`
		} `json:"albums"`
	}
	if !e.getJSON(ctx, u, "", &body) {
		return nil, ""
	}
	for _, album := range body.Albums {
		if album.ReleaseGroupID == releaseGroupID && len(album.AlbumCover) > 0 && album.AlbumCover[0].URL != "" {
			return e.fetch(ctx, album.AlbumCover[0].URL, "")
		}
	}
	return nil, ""
}

func (e *Enricher) mbReleaseGroup(ctx context.Context, artist, album string) string {
	q := fmt.Sprintf(`artist:"%s" AND releasegroup:"%s"`, mbEscape(artist), mbEscape(album))
	var body struct {
		Groups []struct {
			ID string `json:"id"`
		} `json:"release-groups"`
	}
	if e.mbQuery(ctx, "release-group", q, &body) && len(body.Groups) > 0 {
		return body.Groups[0].ID
	}
	return ""
}

func (e *Enricher) lastfmAlbumImage(ctx context.Context, key, artist, album string) string {
	u := fmt.Sprintf("%s/?method=album.getinfo&api_key=%s&artist=%s&album=%s&autocorrect=1&format=json",
		e.LastfmBase, url.QueryEscape(key), url.QueryEscape(artist), url.QueryEscape(album))
	var body struct {
		Album struct {
			Image []struct {
				Text string `json:"#text"`
				Size string `json:"size"`
			} `json:"image"`
		} `json:"album"`
	}
	if !e.getJSON(ctx, u, "", &body) {
		return ""
	}
	return largestImage(body.Album.Image)
}

// ── artist art (fanart.tv) ──

func (e *Enricher) artistArt(ctx context.Context, name string) ([]byte, string) {
	mbid := e.mbArtist(ctx, name)
	if mbid == "" {
		return nil, ""
	}
	u := fmt.Sprintf("%s/music/%s?api_key=%s", e.FanartBase, mbid, url.QueryEscape(e.fanartKey(ctx)))
	var body struct {
		Thumb []struct {
			URL string `json:"url"`
		} `json:"artistthumb"`
		Background []struct {
			URL string `json:"url"`
		} `json:"artistbackground"`
	}
	if !e.getJSON(ctx, u, "", &body) {
		return nil, ""
	}
	pick := ""
	if len(body.Thumb) > 0 {
		pick = body.Thumb[0].URL
	} else if len(body.Background) > 0 {
		pick = body.Background[0].URL
	}
	if pick == "" {
		return nil, ""
	}
	return e.fetch(ctx, pick, "")
}

func (e *Enricher) mbArtist(ctx context.Context, name string) string {
	var body struct {
		Artists []struct {
			ID string `json:"id"`
		} `json:"artists"`
	}
	if e.mbQuery(ctx, "artist", fmt.Sprintf(`artist:"%s"`, mbEscape(name)), &body) && len(body.Artists) > 0 {
		return body.Artists[0].ID
	}
	return ""
}

// ── http helpers ──

func (e *Enricher) mbQuery(ctx context.Context, entity, query string, out any) bool {
	musicBrainzRateGate.Lock()
	defer musicBrainzRateGate.Unlock()
	if e.mbInterval > 0 {
		timer := time.NewTimer(e.mbInterval)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return false
		}
	}
	u := fmt.Sprintf("%s/%s?query=%s&fmt=json&limit=1", e.MBBase, entity, url.QueryEscape(query))
	return e.getJSON(ctx, u, e.cfg.UserAgent, out)
}

func (e *Enricher) getJSON(ctx context.Context, u, userAgent string, out any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := e.do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out) == nil
}

// fetch downloads binary art; returns the bytes + content type.
func (e *Enricher) fetch(ctx context.Context, u, userAgent string) ([]byte, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, ""
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := e.do(req)
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil || len(data) == 0 {
		return nil, ""
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" || !strings.HasPrefix(mime, "image/") {
		mime = "image/jpeg"
	}
	return data, mime
}

func (e *Enricher) do(req *http.Request) (*http.Response, error) {
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

func (e *Enricher) lastfmKey(ctx context.Context) string {
	if e.cfg.LastfmKey == nil {
		return ""
	}
	return e.cfg.LastfmKey(ctx)
}

func (e *Enricher) fanartKey(ctx context.Context) string {
	if e.cfg.FanartKey == nil {
		return ""
	}
	return e.cfg.FanartKey(ctx)
}

func mbEscape(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

func largestImage(images []struct {
	Text string `json:"#text"`
	Size string `json:"size"`
}) string {
	order := map[string]int{"small": 1, "medium": 2, "large": 3, "extralarge": 4, "mega": 5, "": 0}
	best, bestRank := "", -1
	for _, im := range images {
		if im.Text == "" {
			continue
		}
		if r := order[im.Size]; r >= bestRank {
			best, bestRank = im.Text, r
		}
	}
	return best
}
