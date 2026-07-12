// Package lastfm is the outbound Last.fm adapter. Callers own the small
// interfaces it satisfies; this package contains no BlitterServer domain logic.
package lastfm

import (
	"context"
	"crypto/md5" // Last.fm's current signature protocol requires MD5.
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	APIKey, Secret, BaseURL string
	HTTP                    *http.Client
}
type Session struct{ Username, Key string }
type Track struct {
	Artist, Title, Album string
	Duration             time.Duration
	StartedAt            time.Time
}
type Artist struct {
	Name, MBID, Bio string
	Match           float64
}
type ErrorKind string

const (
	ErrorTemporary      ErrorKind = "temporary"
	ErrorInvalidSession ErrorKind = "invalid_session"
	ErrorPermanent      ErrorKind = "permanent"
	ErrorNotFound       ErrorKind = "not_found"
)

type ProviderError struct {
	Kind ErrorKind
	Code int
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("last.fm request failed (code %d, %s)", e.Code, e.Kind)
}

type ScrobbleOutcome string

const (
	ScrobbleAccepted ScrobbleOutcome = "accepted"
	ScrobbleIgnored  ScrobbleOutcome = "ignored"
)

type ScrobbleResult struct {
	Outcome     ScrobbleOutcome
	IgnoredCode string
}
type flexibleInt int

func (n *flexibleInt) UnmarshalJSON(data []byte) error {
	raw := strings.Trim(string(data), `"`)
	v, err := strconv.Atoi(raw)
	if err == nil {
		*n = flexibleInt(v)
	}
	return err
}

func New(key, secret string) *Client {
	return &Client{APIKey: key, Secret: secret, BaseURL: "https://ws.audioscrobbler.com/2.0/", HTTP: &http.Client{Timeout: 5 * time.Second}}
}

func signature(values url.Values, secret string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		if k != "format" && k != "callback" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(values.Get(k))
	}
	b.WriteString(secret)
	s := md5.Sum([]byte(b.String()))
	return hex.EncodeToString(s[:])
}

func (c *Client) call(ctx context.Context, post bool, values url.Values, out any) error {
	values.Set("api_key", c.APIKey)
	values.Set("format", "json")
	if post || values.Get("method") == "auth.getSession" {
		values.Set("api_sig", signature(values, c.Secret))
	}
	method, target, body := http.MethodGet, c.BaseURL+"?"+values.Encode(), io.Reader(nil)
	if post {
		method, target, body = http.MethodPost, c.BaseURL, strings.NewReader(values.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	if post {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &ProviderError{Kind: ErrorTemporary}
	}
	defer resp.Body.Close()
	var envelope struct {
		Error   int    `json:"error"`
		Message string `json:"message"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	_ = json.Unmarshal(raw, &envelope)
	if resp.StatusCode/100 != 2 || envelope.Error != 0 {
		kind := ErrorPermanent
		if envelope.Error == 6 && values.Get("method") == "artist.getInfo" {
			kind = ErrorNotFound
		} else if envelope.Error == 9 {
			kind = ErrorInvalidSession
		} else if envelope.Error == 11 || envelope.Error == 16 || envelope.Error == 29 || resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			kind = ErrorTemporary
		}
		return &ProviderError{Kind: kind, Code: envelope.Error}
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return errorsNewResponse()
	}
	return nil
}
func errorsNewResponse() error { return &ProviderError{Kind: ErrorPermanent} }

func (c *Client) Exchange(ctx context.Context, token string) (Session, error) {
	v := url.Values{"method": {"auth.getSession"}, "token": {token}}
	var r struct {
		Session struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"session"`
	}
	err := c.call(ctx, false, v, &r)
	return Session{Username: r.Session.Name, Key: r.Session.Key}, err
}
func (c *Client) write(ctx context.Context, method, sk string, t Track) error {
	v := url.Values{"method": {method}, "sk": {sk}, "artist": {t.Artist}, "track": {t.Title}}
	if t.Album != "" {
		v.Set("album", t.Album)
	}
	if t.Duration > 0 {
		v.Set("duration", fmt.Sprint(int(t.Duration.Seconds())))
	}
	if method == "track.scrobble" {
		v.Set("timestamp", fmt.Sprint(t.StartedAt.Unix()))
	}
	return c.call(ctx, true, v, nil)
}
func (c *Client) NowPlaying(ctx context.Context, sk string, t Track) error {
	return c.write(ctx, "track.updateNowPlaying", sk, t)
}
func (c *Client) Scrobble(ctx context.Context, sk string, t Track) (ScrobbleResult, error) {
	v := url.Values{"method": {"track.scrobble"}, "sk": {sk}, "artist": {t.Artist}, "track": {t.Title}, "timestamp": {fmt.Sprint(t.StartedAt.Unix())}}
	if t.Album != "" {
		v.Set("album", t.Album)
	}
	if t.Duration > 0 {
		v.Set("duration", fmt.Sprint(int(t.Duration.Seconds())))
	}
	var r struct {
		Scrobbles struct {
			Attr struct {
				Accepted flexibleInt `json:"accepted"`
				Ignored  flexibleInt `json:"ignored"`
			} `json:"@attr"`
			Scrobble struct {
				Ignored struct {
					Code string `json:"code"`
				} `json:"ignoredMessage"`
			} `json:"scrobble"`
		} `json:"scrobbles"`
	}
	if err := c.call(ctx, true, v, &r); err != nil {
		return ScrobbleResult{}, err
	}
	if r.Scrobbles.Attr.Accepted > 0 {
		return ScrobbleResult{Outcome: ScrobbleAccepted}, nil
	}
	return ScrobbleResult{Outcome: ScrobbleIgnored, IgnoredCode: r.Scrobbles.Scrobble.Ignored.Code}, nil
}
func (c *Client) Love(ctx context.Context, sk string, t Track, loved bool) error {
	method := "track.unlove"
	if loved {
		method = "track.love"
	}
	return c.write(ctx, method, sk, t)
}

func (c *Client) Similar(ctx context.Context, name string, limit int) ([]Artist, error) {
	v := url.Values{"method": {"artist.getSimilar"}, "artist": {name}, "limit": {fmt.Sprint(limit)}}
	var r struct {
		Similar struct {
			Artist []struct {
				Name, MBID string
				Match      json.Number
			} `json:"artist"`
		} `json:"similarartists"`
	}
	if err := c.call(ctx, false, v, &r); err != nil {
		return nil, err
	}
	out := make([]Artist, 0, len(r.Similar.Artist))
	for _, a := range r.Similar.Artist {
		m, _ := a.Match.Float64()
		out = append(out, Artist{Name: a.Name, MBID: a.MBID, Match: m})
	}
	return out, nil
}
func (c *Client) ArtistInfo(ctx context.Context, name string) (Artist, error) {
	v := url.Values{"method": {"artist.getInfo"}, "artist": {name}, "autocorrect": {"1"}}
	var r struct {
		Artist struct {
			Name, MBID string
			Bio        struct {
				Summary string `json:"summary"`
			} `json:"bio"`
		} `json:"artist"`
	}
	err := c.call(ctx, false, v, &r)
	return Artist{Name: r.Artist.Name, MBID: r.Artist.MBID, Bio: stripHTML(r.Artist.Bio.Summary)}, err
}
func (c *Client) TopArtists(ctx context.Context, username string, limit int) ([]Artist, error) {
	v := url.Values{"method": {"user.getTopArtists"}, "user": {username}, "period": {"3month"}, "limit": {fmt.Sprint(limit)}}
	var r struct {
		Top struct {
			Artist []struct{ Name, MBID string } `json:"artist"`
		} `json:"topartists"`
	}
	if err := c.call(ctx, false, v, &r); err != nil {
		return nil, err
	}
	out := make([]Artist, 0, len(r.Top.Artist))
	for _, a := range r.Top.Artist {
		out = append(out, Artist{Name: a.Name, MBID: a.MBID})
	}
	return out, nil
}
func stripHTML(s string) string {
	for {
		a := strings.IndexByte(s, '<')
		if a < 0 {
			return strings.TrimSpace(s)
		}
		b := strings.IndexByte(s[a:], '>')
		if b < 0 {
			return strings.TrimSpace(s[:a])
		}
		s = s[:a] + s[a+b+1:]
	}
}
