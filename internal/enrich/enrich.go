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
	"strings"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// perRun caps how many entities we resolve in one pass (MusicBrainz is ~1 req/s).
const perRun = 150

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

	mb <-chan time.Time // MusicBrainz rate limiter
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
		FanartBase: "https://webservice.fanart.tv/v3",
		mb:         time.NewTicker(1100 * time.Millisecond).C,
	}
}

// Run does one enrichment pass over untried albums (then artists). Best-effort:
// failures just mark the entity tried and move on.
func (e *Enricher) Run(ctx context.Context) {
	log := logging.From(ctx).With("component", "enrich")
	seq, err := e.st.NextScanSeq(ctx)
	if err != nil {
		return
	}
	changed := false

	albums, _ := e.st.AlbumsNeedingArt(ctx, perRun)
	for _, a := range albums {
		if ctx.Err() != nil {
			return
		}
		if data, mime := e.albumArt(ctx, a.ArtistName, a.Title); data != nil {
			if id, err := e.store(ctx, data, mime); err == nil {
				_ = e.st.SetAlbumArt(ctx, a.AlbumID, id, seq)
				changed = true
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
					_ = e.st.SetArtistArt(ctx, ar.ArtistID, id, seq)
					changed = true
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
		if data, mime := e.fetch(ctx, fmt.Sprintf("%s/release-group/%s/front-500", e.CAABase, rg), ""); data != nil {
			return data, mime
		}
	}
	if key := e.lastfmKey(ctx); key != "" {
		if u := e.lastfmAlbumImage(ctx, key, artist, album); u != "" {
			return e.fetch(ctx, u, "")
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
	u := fmt.Sprintf("%s/?method=album.getinfo&api_key=%s&artist=%s&album=%s&format=json",
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
	<-e.mb // rate limit
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
	resp, err := e.http.Do(req)
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
	resp, err := e.http.Do(req)
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
