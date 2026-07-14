// Package enrich fills gaps in album/artist art from public sources. After a
// scan, albums with no embedded cover and artists with no photo are looked up:
// MusicBrainz resolves ids when identity-keyed providers need them. Last.fm,
// Discogs, fanart.tv, and Cover Art Archive supply artwork. Results are stored via the art cache and
// bump change_seq so clients delta-sync the new art. Entities are marked "tried"
// so we don't re-query every scan.
package enrich

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/events"
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
	// DiscogsToken returns the current Discogs personal access token.
	DiscogsToken func(context.Context) string
	// DiscogsUserAgent is the versioned, contactable identity sent to Discogs.
	DiscogsUserAgent string
	// UserAgent identifies us to MusicBrainz (required by their policy).
	MusicBrainz *musicbrainz.Client
	// ProviderCache persists public provider responses outside the library database.
	ProviderCache *providercache.Cache
}

// Enricher runs enrichment passes. Base URLs are fields so tests can point them
// at local servers.
type Enricher struct {
	st                         *store.Store
	bus                        *events.Bus
	artDir                     string
	http                       *http.Client
	cfg                        Config
	resolver                   *mbresolver.Resolver
	mbClient                   *musicbrainz.Client
	cache                      *providercache.Cache
	providerPacers             map[string]*providerPacer
	markArtistMetadataTerminal func(context.Context, string, string) error
	consolidateArtists         func(context.Context) (bool, error)
	attachAlbumArtFn           func(context.Context, string, string) (bool, error)
	attachArtistArtFn          func(context.Context, string, string, string) (bool, error)
	countAlbumsNeedingArt      func(context.Context, time.Time) (int, error)
	countArtistsNeedingArt     func(context.Context, time.Time) (int, error)

	CAABase     string
	LastfmBase  string
	FanartBase  string
	DiscogsBase string
	// ProgressInterval rate-limits intermediate library.changed publishes
	// during a long pass so clients see progress without event spam.
	ProgressInterval time.Duration
	// ArtSliceBudget bounds the pre-identity artwork slice per pass: covers
	// appear quickly without starving identity matching across restarts.
	ArtSliceBudget      time.Duration
	LogProgressInterval time.Duration
	Now                 func() time.Time
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
			"caa": newProviderPacer(time.Second), "fanart": newProviderPacer(time.Second), "lastfm": newProviderPacer(time.Second), "discogs": discogsProcessPacer,
		},
		CAABase:             "https://coverartarchive.org",
		LastfmBase:          "https://ws.audioscrobbler.com/2.0",
		FanartBase:          "https://webservice.fanart.tv/v3.2",
		DiscogsBase:         defaultDiscogsBase,
		ProgressInterval:    15 * time.Second,
		ArtSliceBudget:      90 * time.Second,
		LogProgressInterval: 15 * time.Second,
		Now:                 time.Now,
	}
	e.mbClient = cfg.MusicBrainz
	e.markArtistMetadataTerminal = st.MarkMusicBrainzArtistMetadataTerminal
	e.consolidateArtists = st.ConsolidateMusicBrainzArtistsAtNextSequence
	e.attachAlbumArtFn = e.attachAlbumArt
	e.attachArtistArtFn = e.attachArtistArt
	e.countAlbumsNeedingArt = st.CountAlbumsNeedingArtAt
	e.countArtistsNeedingArt = st.CountArtistsNeedingArtAt
	if e.mbClient != nil {
		e.resolver = mbresolver.New(st, e.mbClient)
	}
	return e
}

// mbReleaseGroup remains as a cautious art fallback for package tests and
// future explicit fallback use. It accepts only one exact title result.
func (e *Enricher) mbReleaseGroup(ctx context.Context, artist, album string) string {
	match, _ := e.mbReleaseGroupOutcome(ctx, artist, album)
	return match
}

func (e *Enricher) mbReleaseGroupOutcome(ctx context.Context, artist, album string) (string, lookupOutcome) {
	q := fmt.Sprintf(`releasegroup:"%s" AND artist:"%s"`, album, artist)
	if e.mbClient == nil {
		return "", lookupMiss
	}
	var body struct {
		Groups []struct{ ID, Title string } `json:"release-groups"`
	}
	if e.mbClient.GetJSON(ctx, "/release-group?query="+url.QueryEscape(q)+"&fmt=json&limit=5", &body) != nil {
		return "", lookupTransient
	}
	match := ""
	for _, g := range body.Groups {
		if strings.EqualFold(strings.TrimSpace(g.Title), strings.TrimSpace(album)) {
			if match != "" {
				return "", lookupMiss
			}
			match = g.ID
		}
	}
	if match == "" {
		return "", lookupMiss
	}
	return match, lookupSuccess
}

type lookupOutcome int

const (
	lookupMiss lookupOutcome = iota
	lookupSuccess
	lookupTransient
)

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
	transient := false
	if data, mime, outcome := e.lastfmAlbumArtOutcome(ctx, artist, album); data != nil {
		return data, mime, lookupSuccess
	} else if outcome == lookupTransient {
		transient = true
	}
	if data, mime, outcome := e.discogsAlbumArtOutcome(ctx, artist, album); data != nil {
		return data, mime, lookupSuccess
	} else if outcome == lookupTransient {
		transient = true
	}
	if releaseGroupID == "" {
		var outcome lookupOutcome
		releaseGroupID, outcome = e.mbReleaseGroupOutcome(ctx, artist, album)
		if outcome == lookupTransient {
			transient = true
		}
	}
	if releaseGroupID == "" {
		if transient || e.mbClient == nil && e.lastfmKey(ctx) == "" && e.discogsToken(ctx) == "" {
			return nil, "", lookupTransient
		}
		return nil, "", lookupMiss
	}
	if key := e.fanartKey(ctx); key != "" {
		if data, mime, outcome := e.fanartAlbumArtOutcome(ctx, key, releaseGroupID); data != nil {
			return data, mime, lookupSuccess
		} else if outcome == lookupTransient {
			transient = true
		}
	}
	if data, mime, outcome := e.fetchOutcome(ctx, "caa", fmt.Sprintf("%s/release-group/%s/front-500", e.CAABase, releaseGroupID), ""); data != nil {
		return data, mime, lookupSuccess
	} else if outcome == lookupTransient {
		transient = true
	}
	if transient {
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

// ── artist art ──

func (e *Enricher) artistArt(ctx context.Context, mbid string) ([]byte, string) {
	data, mime, _ := e.artistArtOutcome(ctx, "", mbid)
	return data, mime
}

func (e *Enricher) artistArtOutcome(ctx context.Context, name, mbid string) ([]byte, string, lookupOutcome) {
	transient := false
	if data, mime, outcome := e.lastfmArtistArtOutcome(ctx, name); data != nil {
		return data, mime, lookupSuccess
	} else if outcome == lookupTransient {
		transient = true
	}
	if data, mime, outcome := e.discogsArtistArtOutcome(ctx, name); data != nil {
		return data, mime, lookupSuccess
	} else if outcome == lookupTransient {
		transient = true
	}
	if e.fanartKey(ctx) == "" {
		if transient {
			return nil, "", lookupTransient
		}
		return nil, "", lookupMiss
	}
	if mbid == "" {
		var outcome lookupOutcome
		mbid, outcome = e.musicBrainzArtistID(ctx, name)
		if outcome == lookupTransient {
			transient = true
		}
		if mbid == "" {
			if transient {
				return nil, "", lookupTransient
			}
			return nil, "", lookupMiss
		}
	}
	data, mime, outcome := e.fanartArtistArtOutcome(ctx, mbid)
	if data != nil {
		return data, mime, lookupSuccess
	}
	if transient || outcome == lookupTransient {
		return nil, "", lookupTransient
	}
	return nil, "", lookupMiss
}

func (e *Enricher) lastfmArtistArtOutcome(ctx context.Context, name string) ([]byte, string, lookupOutcome) {
	key := e.lastfmKey(ctx)
	if key == "" {
		return nil, "", lookupMiss
	}
	u := fmt.Sprintf("%s/?method=artist.getinfo&api_key=%s&artist=%s&autocorrect=1&format=json", e.LastfmBase, url.QueryEscape(key), url.QueryEscape(name))
	var body struct {
		Artist struct {
			Image []struct {
				Text string `json:"#text"`
				Size string `json:"size"`
			} `json:"image"`
		} `json:"artist"`
	}
	if outcome := e.getJSONOutcome(ctx, u, "", &body); outcome != lookupSuccess {
		return nil, "", outcome
	}
	image := largestImage(body.Artist.Image)
	if image == "" {
		e.markMiss(ctx, "lastfm", u)
		return nil, "", lookupMiss
	}
	return e.fetchOutcome(ctx, "lastfm", image, "")
}

func (e *Enricher) musicBrainzArtistID(ctx context.Context, name string) (string, lookupOutcome) {
	if e.mbClient == nil {
		return "", lookupTransient
	}
	q := fmt.Sprintf(`artist:"%s"`, name)
	var body struct {
		Artists []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"artists"`
	}
	if err := e.mbClient.GetJSON(ctx, "/artist?query="+url.QueryEscape(q)+"&fmt=json&limit=5", &body); err != nil {
		return "", lookupTransient
	}
	match := ""
	for _, artist := range body.Artists {
		if strings.EqualFold(strings.TrimSpace(artist.Name), strings.TrimSpace(name)) {
			if match != "" {
				return "", lookupMiss
			}
			match = artist.ID
		}
	}
	if match == "" {
		return "", lookupMiss
	}
	return match, lookupSuccess
}

func (e *Enricher) fanartArtistArtOutcome(ctx context.Context, mbid string) ([]byte, string, lookupOutcome) {
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

func (e *Enricher) discogsToken(ctx context.Context) string {
	if e.cfg.DiscogsToken == nil {
		return ""
	}
	return e.cfg.DiscogsToken(ctx)
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
