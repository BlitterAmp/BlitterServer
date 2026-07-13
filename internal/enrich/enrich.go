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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/mbresolver"
	"github.com/BlitterAmp/BlitterServer/internal/musicbrainz"
	"github.com/BlitterAmp/BlitterServer/internal/providercache"
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
	MusicBrainz *musicbrainz.Client
	// ProviderCache persists public provider responses outside the library database.
	ProviderCache *providercache.Cache
}

// Enricher runs enrichment passes. Base URLs are fields so tests can point them
// at local servers.
type Enricher struct {
	st             *store.Store
	bus            *events.Bus
	artDir         string
	http           *http.Client
	cfg            Config
	resolver       *mbresolver.Resolver
	mbClient       *musicbrainz.Client
	cache          *providercache.Cache
	providerPacers map[string]*providerPacer

	CAABase    string
	LastfmBase string
	FanartBase string
	// ProgressInterval rate-limits intermediate library.changed publishes
	// during a long pass so clients see progress without event spam.
	ProgressInterval time.Duration
	// ArtSliceBudget bounds the pre-identity artwork slice per pass: covers
	// appear quickly without starving identity matching across restarts.
	ArtSliceBudget time.Duration
}

func New(st *store.Store, bus *events.Bus, artDir string, cfg Config) *Enricher {
	e := &Enricher{
		st:     st,
		bus:    bus,
		artDir: artDir,
		http:   &http.Client{Timeout: 20 * time.Second},
		cfg:    cfg,
		cache:  cfg.ProviderCache,
		providerPacers: map[string]*providerPacer{
			"caa": newProviderPacer(time.Second), "fanart": newProviderPacer(time.Second), "lastfm": newProviderPacer(time.Second),
		},
		CAABase:          "https://coverartarchive.org",
		LastfmBase:       "https://ws.audioscrobbler.com/2.0",
		FanartBase:       "https://webservice.fanart.tv/v3.2",
		ProgressInterval: 15 * time.Second,
		ArtSliceBudget:   90 * time.Second,
	}
	e.mbClient = cfg.MusicBrainz
	if e.mbClient != nil {
		e.resolver = mbresolver.New(st, e.mbClient)
	}
	return e
}

// mbReleaseGroup remains as a cautious art fallback for package tests and
// future explicit fallback use. It accepts only one exact title result.
func (e *Enricher) mbReleaseGroup(ctx context.Context, artist, album string) string {
	q := fmt.Sprintf(`releasegroup:"%s" AND artist:"%s"`, album, artist)
	if e.mbClient == nil {
		return ""
	}
	var body struct {
		Groups []struct{ ID, Title string } `json:"release-groups"`
	}
	if e.mbClient.GetJSON(ctx, "/release-group?query="+url.QueryEscape(q)+"&fmt=json&limit=5", &body) != nil {
		return ""
	}
	match := ""
	for _, g := range body.Groups {
		if strings.EqualFold(strings.TrimSpace(g.Title), strings.TrimSpace(album)) {
			if match != "" {
				return ""
			}
			match = g.ID
		}
	}
	return match
}

type lookupOutcome int

const (
	lookupMiss lookupOutcome = iota
	lookupSuccess
	lookupTransient
)

type runSummary struct{ attempted, succeeded, missed, transient int }

func (e *Enricher) Run(ctx context.Context) {
	e.RunAt(ctx, time.Now())
}

func (e *Enricher) RunAt(ctx context.Context, now time.Time) {
	log := logging.From(ctx).With("component", "enrich")
	stats := runSummary{}
	dirty, anyChange := false, false
	lastPublish := time.Now()
	markDirty := func() { dirty, anyChange = true, true }
	publish := func(force bool) {
		if !dirty || e.bus == nil {
			return
		}
		if !force && time.Since(lastPublish) < e.ProgressInterval {
			return
		}
		publishCtx := context.WithoutCancel(ctx)
		if sum, err := e.st.GetLibrarySummary(publishCtx); err != nil {
			log.Error("read library summary for enrichment event", "err", err)
		} else if libraryID, err := e.st.LibraryID(publishCtx); err != nil {
			log.Error("read library id for enrichment event", "err", err)
		} else if err := e.bus.Publish(publishCtx, "library.changed", "", map[string]any{"libraryId": libraryID, "updatedAt": sum.UpdatedAt}); err != nil {
			log.Error("publish enrichment library change", "err", err)
		} else {
			dirty = false
			lastPublish = time.Now()
		}
	}
	defer func() {
		log.Info("enrichment run complete", "attempted", stats.attempted, "succeeded", stats.succeeded, "missed", stats.missed, "transient", stats.transient)
		publish(true)
		if anyChange {
			log.Info("enrichment updated the library")
		}
	}()
	var seq int64
	nextSeq := func() bool {
		if seq != 0 {
			return true
		}
		var err error
		seq, err = e.st.NextScanSeq(ctx)
		if err != nil {
			log.Error("allocate enrichment change sequence", "err", err)
		}
		return err == nil
	}
	albumArtStage := func(deadline time.Time) {
		pages := 0
		for {
			if ctx.Err() != nil {
				return
			}
			if !deadline.IsZero() && time.Now().After(deadline) {
				log.Info("artwork stage yielding to identity matching", "attempted", stats.attempted)
				return
			}
			albums, err := e.st.AlbumsNeedingArtAt(ctx, now, perRun)
			if err != nil {
				log.Error("list albums needing artwork", "err", err)
				return
			}
			if len(albums) == 0 {
				if pages > 0 {
					log.Info("album artwork stage drained", "succeeded", stats.succeeded, "missed", stats.missed, "transient", stats.transient)
				}
				return
			}
			pages++
			log.Info("album artwork stage", "page", pages, "queued", len(albums), "succeeded_so_far", stats.succeeded)
			progressed := false
			for _, a := range albums {
				if ctx.Err() != nil {
					return
				}
				stats.attempted++
				data, mime, outcome := e.albumArtOutcome(ctx, a.ReleaseGroupID, a.ArtistName, a.Title)
				if ctx.Err() != nil {
					return
				}
				if data != nil {
					if id, err := e.store(ctx, data, mime); err == nil {
						if !nextSeq() {
							return
						}
						applied, err := e.st.SetAlbumArt(ctx, a.AlbumID, id, seq)
						if applied {
							markDirty()
							publish(false)
						}
						if err != nil {
							log.Error("attach album artwork", "album_id", a.AlbumID, "err", err)
						} else {
							stats.succeeded++
							progressed = true
						}
						continue
					} else {
						log.Error("store album artwork", "album_id", a.AlbumID, "err", err)
						outcome = lookupTransient
					}
				}
				if outcome == lookupTransient {
					stats.transient++
					err = e.st.MarkAlbumArtAttempt(ctx, a.AlbumID, store.ArtAttemptTransient, now)
				} else {
					stats.missed++
					err = e.st.MarkAlbumArtAttempt(ctx, a.AlbumID, store.ArtAttemptMiss, now)
				}
				if err != nil {
					log.Error("schedule album artwork retry", "album_id", a.AlbumID, "err", err)
				} else {
					progressed = true
				}
			}
			publish(false)
			if !progressed {
				log.Error("album artwork stage stalled; ending stage")
				return
			}
		}
	}

	artistArtStage := func(deadline time.Time) {
		if e.fanartKey(ctx) == "" {
			return
		}
		pages := 0
		for {
			if ctx.Err() != nil {
				return
			}
			if !deadline.IsZero() && time.Now().After(deadline) {
				log.Info("artist artwork stage yielding to identity matching", "attempted", stats.attempted)
				return
			}
			artists, err := e.st.ArtistsNeedingArtAt(ctx, now, perRun)
			if err != nil {
				log.Error("list artists needing artwork", "err", err)
				return
			}
			if len(artists) == 0 {
				if pages > 0 {
					log.Info("artist artwork stage drained", "succeeded", stats.succeeded)
				}
				return
			}
			pages++
			log.Info("artist artwork stage", "page", pages, "queued", len(artists))
			progressed := false
			for _, ar := range artists {
				if ctx.Err() != nil {
					return
				}
				stats.attempted++
				data, mime, outcome := e.artistArtOutcome(ctx, ar.MusicBrainzID)
				if ctx.Err() != nil {
					return
				}
				if data != nil {
					if id, err := e.store(ctx, data, mime); err == nil {
						if !nextSeq() {
							return
						}
						applied, err := e.st.SetArtistArt(ctx, ar.ArtistID, ar.ArtID, id, seq)
						if applied {
							markDirty()
							publish(false)
						}
						if err != nil {
							log.Error("attach artist artwork", "artist_id", ar.ArtistID, "err", err)
						} else {
							stats.succeeded++
							progressed = true
						}
						continue
					} else {
						log.Error("store artist artwork", "artist_id", ar.ArtistID, "err", err)
						outcome = lookupTransient
					}
				}
				if outcome == lookupTransient {
					stats.transient++
					err = e.st.MarkArtistArtAttempt(ctx, ar.ArtistID, store.ArtAttemptTransient, now)
				} else {
					stats.missed++
					err = e.st.MarkArtistArtAttempt(ctx, ar.ArtistID, store.ArtAttemptMiss, now)
				}
				if err != nil {
					log.Error("schedule artist artwork retry", "artist_id", ar.ArtistID, "err", err)
				} else {
					progressed = true
				}
			}
			publish(false)
			if !progressed {
				log.Error("artist artwork stage stalled; ending stage")
				return
			}
		}
	}

	// Artwork first — but only a bounded slice: covers appear quickly on a
	// fresh library without starving identity matching, which restarts would
	// otherwise re-park behind a full artwork drain forever.
	artDeadline := time.Now().Add(e.ArtSliceBudget)
	albumArtStage(artDeadline)
	artistArtStage(artDeadline)

	if e.resolver != nil {
		due, _ := e.st.CountDueMusicBrainzAlbums(ctx, now)
		log.Info("identity matching started", "due", due)
		matched := 0
		resolved, err := e.resolver.RunWithProgress(ctx, func() {
			matched++
			if matched%25 == 0 {
				log.Info("identity matching progress", "applied", matched)
			}
			markDirty()
			publish(false)
		})
		if resolved {
			markDirty()
		}
		if err != nil {
			log.Warn("MusicBrainz identity resolution failed", "err", err)
		}
		remaining, _ := e.st.CountDueMusicBrainzAlbums(ctx, now)
		log.Info("identity matching finished", "applied", matched, "still_due", remaining)
	}
	publish(false)

	// Identity landed above: newly matched albums and newly identified
	// artists had their art schedules reset, so give them their first
	// identity-aware fetch in the same pass — unbounded this time.
	albumArtStage(time.Time{})
	artistArtStage(time.Time{})
}

func (e *Enricher) store(ctx context.Context, data []byte, mime string) (string, error) {
	sum := sha256.Sum256(data)
	return e.st.UpsertArt(ctx, hex.EncodeToString(sum[:]), mime, data, filepath.Join(e.artDir))
}

// ── album art ──

func (e *Enricher) albumArt(ctx context.Context, releaseGroupID, artist, album string) ([]byte, string) {
	data, mime, _ := e.albumArtOutcome(ctx, releaseGroupID, artist, album)
	return data, mime
}

func (e *Enricher) albumArtOutcome(ctx context.Context, releaseGroupID, artist, album string) ([]byte, string, lookupOutcome) {
	if releaseGroupID != "" {
		return e.albumArtForReleaseGroupOutcome(ctx, releaseGroupID, artist, album)
	}
	if fallback := e.mbReleaseGroup(ctx, artist, album); fallback != "" {
		return e.albumArtForReleaseGroupOutcome(ctx, fallback, artist, album)
	}
	data, mime, outcome := e.lastfmAlbumArtOutcome(ctx, artist, album)
	if data == nil && outcome == lookupMiss && e.mbClient == nil && e.lastfmKey(ctx) == "" {
		// No provider made a definitive statement: identity resolution and the
		// only name-based fallback were unavailable, so preserve the miss tier.
		outcome = lookupTransient
	}
	return data, mime, outcome
}

func (e *Enricher) albumArtForReleaseGroup(ctx context.Context, releaseGroupID, artist, album string) ([]byte, string) {
	data, mime, _ := e.albumArtForReleaseGroupOutcome(ctx, releaseGroupID, artist, album)
	return data, mime
}

func (e *Enricher) albumArtForReleaseGroupOutcome(ctx context.Context, releaseGroupID, artist, album string) ([]byte, string, lookupOutcome) {
	transient := false
	if data, mime, outcome := e.fetchOutcome(ctx, "caa", fmt.Sprintf("%s/release-group/%s/front-500", e.CAABase, releaseGroupID), ""); data != nil {
		return data, mime, lookupSuccess
	} else if outcome == lookupTransient {
		transient = true
	}
	if key := e.fanartKey(ctx); key != "" {
		if data, mime, outcome := e.fanartAlbumArtOutcome(ctx, key, releaseGroupID); data != nil {
			return data, mime, lookupSuccess
		} else if outcome == lookupTransient {
			transient = true
		}
	}
	data, mime, outcome := e.lastfmAlbumArtOutcome(ctx, artist, album)
	if data != nil {
		return data, mime, lookupSuccess
	}
	if transient || outcome == lookupTransient {
		return nil, "", lookupTransient
	}
	return nil, "", lookupMiss
}

func (e *Enricher) lastfmAlbumArt(ctx context.Context, artist, album string) ([]byte, string) {
	data, mime, _ := e.lastfmAlbumArtOutcome(ctx, artist, album)
	return data, mime
}

func (e *Enricher) lastfmAlbumArtOutcome(ctx context.Context, artist, album string) ([]byte, string, lookupOutcome) {
	if key := e.lastfmKey(ctx); key != "" {
		if u, outcome := e.lastfmAlbumImageOutcome(ctx, key, artist, album); u != "" {
			return e.fetchOutcome(ctx, "lastfm", u, "")
		} else if outcome == lookupTransient {
			return nil, "", outcome
		}
	}
	return nil, "", lookupMiss
}

func (e *Enricher) fanartAlbumArt(ctx context.Context, key, releaseGroupID string) ([]byte, string) {
	data, mime, _ := e.fanartAlbumArtOutcome(ctx, key, releaseGroupID)
	return data, mime
}

func (e *Enricher) fanartAlbumArtOutcome(ctx context.Context, key, releaseGroupID string) ([]byte, string, lookupOutcome) {
	u := fmt.Sprintf("%s/music/albums/%s?api_key=%s", e.FanartBase, url.PathEscape(releaseGroupID), url.QueryEscape(key))
	var body struct {
		Albums []struct {
			ReleaseGroupID string `json:"release_group_id"`
			AlbumCover     []struct {
				URL string `json:"url"`
			} `json:"albumcover"`
		} `json:"albums"`
	}
	if outcome := e.getJSONOutcome(ctx, u, "", &body); outcome != lookupSuccess {
		return nil, "", outcome
	}
	for _, album := range body.Albums {
		if album.ReleaseGroupID == releaseGroupID && len(album.AlbumCover) > 0 && album.AlbumCover[0].URL != "" {
			return e.fetchOutcome(ctx, "fanart", album.AlbumCover[0].URL, "")
		}
	}
	e.markMiss(ctx, "fanart", u)
	return nil, "", lookupMiss
}

func (e *Enricher) lastfmAlbumImage(ctx context.Context, key, artist, album string) string {
	image, _ := e.lastfmAlbumImageOutcome(ctx, key, artist, album)
	return image
}

func (e *Enricher) lastfmAlbumImageOutcome(ctx context.Context, key, artist, album string) (string, lookupOutcome) {
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
	if outcome := e.getJSONOutcome(ctx, u, "", &body); outcome != lookupSuccess {
		return "", outcome
	}
	image := largestImage(body.Album.Image)
	if image == "" {
		e.markMiss(ctx, "lastfm", u)
	}
	if image == "" {
		return "", lookupMiss
	}
	return image, lookupSuccess
}

// ── artist art (fanart.tv) ──

func (e *Enricher) artistArt(ctx context.Context, mbid string) ([]byte, string) {
	data, mime, _ := e.artistArtOutcome(ctx, mbid)
	return data, mime
}

func (e *Enricher) artistArtOutcome(ctx context.Context, mbid string) ([]byte, string, lookupOutcome) {
	if mbid == "" {
		return nil, "", lookupMiss
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
	if outcome := e.getJSONOutcome(ctx, u, "", &body); outcome != lookupSuccess {
		return nil, "", outcome
	}
	pick := ""
	if len(body.Thumb) > 0 {
		pick = body.Thumb[0].URL
	} else if len(body.Background) > 0 {
		pick = body.Background[0].URL
	}
	if pick == "" {
		e.markMiss(ctx, "fanart", u)
		return nil, "", lookupMiss
	}
	return e.fetchOutcome(ctx, "fanart", pick, "")
}

// ── http helpers ──

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
			if json.Unmarshal(cached.Body, out) != nil {
				return lookupTransient
			}
			return lookupSuccess
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
	if err != nil || json.Unmarshal(body, out) != nil {
		return lookupTransient
	}
	e.putCache(ctx, provider, key, providercache.Entry{URL: key, Status: resp.StatusCode, MIME: resp.Header.Get("Content-Type"), FetchedAt: time.Now(), FreshUntil: time.Now().Add(30 * 24 * time.Hour), Kind: providercache.KindSuccess, Body: body})
	return lookupSuccess
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
