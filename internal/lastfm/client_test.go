package lastfm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestSignedSessionExchange(t *testing.T) {
	var got url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{"session": map[string]string{"name": "synthetic_user", "key": "synthetic_session"}})
	}))
	defer ts.Close()
	c := New("synthetic_key", "synthetic_secret")
	c.BaseURL = ts.URL
	session, err := c.Exchange(context.Background(), "synthetic_token")
	if err != nil {
		t.Fatal(err)
	}
	if session.Username != "synthetic_user" || session.Key != "synthetic_session" {
		t.Fatalf("session: %#v", session)
	}
	want := signature(url.Values{"api_key": {"synthetic_key"}, "method": {"auth.getSession"}, "token": {"synthetic_token"}}, "synthetic_secret")
	if got.Get("api_sig") != want || got.Get("format") != "json" {
		t.Fatalf("query: %v", got)
	}
	if got.Has("synthetic_secret") {
		t.Fatal("shared secret was transmitted")
	}
}

func TestScrobbleParsesAcceptedAndIgnored(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		out        ScrobbleOutcome
		code       string
	}{
		{"accepted", `{"scrobbles":{"@attr":{"accepted":"1","ignored":"0"},"scrobble":{"ignoredMessage":{"code":"0"}}}}`, ScrobbleAccepted, ""},
		{"ignored", `{"scrobbles":{"@attr":{"accepted":0,"ignored":1},"scrobble":{"ignoredMessage":{"code":"2"}}}}`, ScrobbleIgnored, "2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var form url.Values
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				form = r.Form
				_, _ = w.Write([]byte(tc.body))
			}))
			defer ts.Close()
			c := New("synthetic_key", "synthetic_secret")
			c.BaseURL = ts.URL
			started := time.Unix(1700000000, 0)
			got, err := c.Scrobble(context.Background(), "synthetic_session", Track{Artist: "Synthetic Artist", Title: "Synthetic Track", Duration: time.Minute, StartedAt: started})
			if err != nil {
				t.Fatal(err)
			}
			if got.Outcome != tc.out || got.IgnoredCode != tc.code {
				t.Fatalf("result: %#v", got)
			}
			if form.Get("timestamp") != "1700000000" || form.Get("api_sig") == "" {
				t.Fatalf("form: %v", form)
			}
		})
	}
}

func TestProviderErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		code int
		kind ErrorKind
	}{{9, ErrorInvalidSession}, {11, ErrorTemporary}, {16, ErrorTemporary}, {29, ErrorTemporary}, {6, ErrorPermanent}} {
		t.Run(fmt.Sprint(tc.code), func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": tc.code, "message": "sensitive provider detail"})
			}))
			defer ts.Close()
			c := New("k", "s")
			c.BaseURL = ts.URL
			_, err := c.Exchange(context.Background(), "token")
			var pe *ProviderError
			if !errors.As(err, &pe) || pe.Kind != tc.kind {
				t.Fatalf("error: %#v", err)
			}
		})
	}
}

func TestArtistInfoCodeSixIsNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": 6, "message": "Artist not found"})
	}))
	defer ts.Close()
	c := New("k", "s")
	c.BaseURL = ts.URL
	_, err := c.ArtistInfo(context.Background(), "Synthetic Missing")
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Kind != ErrorNotFound {
		t.Fatalf("artist info error: %#v", err)
	}
}

func TestProviderErrorDoesNotExposeMessage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": 9, "message": "synthetic_user synthetic_session"})
	}))
	defer ts.Close()
	c := New("k", "s")
	c.BaseURL = ts.URL
	_, err := c.Exchange(context.Background(), "token")
	if err == nil || err.Error() != "last.fm request failed (code 9, invalid_session)" {
		t.Fatalf("sanitized error: %v", err)
	}
}
