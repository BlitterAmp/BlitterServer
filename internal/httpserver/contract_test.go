package httpserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	blittarr "github.com/BlitterAmp/Blittarr"
	"github.com/BlitterAmp/Blittarr/internal/api"
	"github.com/BlitterAmp/Blittarr/internal/httpserver"
	"github.com/BlitterAmp/Blittarr/internal/store"
	"gopkg.in/yaml.v3"
)

func setup(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(httpserver.Handler(st, "test"))
	t.Cleanup(ts.Close)
	dev, _ := st.CreateDevice(context.Background(), "d", "ios")
	prf, _ := st.CreateProfile(context.Background(), "p")
	tok, _ := st.CreateProfileToken(context.Background(), dev, prf)
	return ts, st, tok
}

func bearer(tok string) api.RequestEditorFn {
	return func(ctx context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+tok)
		return nil
	}
}

func TestContractPing(t *testing.T) {
	ts, _, _ := setup(t)
	c, _ := api.NewClientWithResponses(ts.URL)
	resp, err := c.GetPingWithResponse(context.Background())
	if err != nil || resp.JSON200 == nil {
		t.Fatalf("ping: %v %s", err, resp.Status())
	}
	if resp.JSON200.Name != "Blittarr" {
		t.Fatalf("bad name %q", resp.JSON200.Name)
	}
}

func TestContractStatusRequiresAuth(t *testing.T) {
	ts, _, tok := setup(t)
	c, _ := api.NewClientWithResponses(ts.URL)
	unauth, err := c.GetStatusWithResponse(context.Background())
	if err != nil || unauth.StatusCode() != 401 {
		t.Fatalf("want 401 without token, got %v %v", err, unauth.StatusCode())
	}
	authed, err := c.GetStatusWithResponse(context.Background(), bearer(tok))
	if err != nil || authed.JSON200 == nil {
		t.Fatalf("authed status: %v %v", err, authed.StatusCode())
	}
	if authed.JSON200.Source.Connected {
		t.Fatal("no source is configured; connected must be false")
	}
}

func TestContractCapabilities(t *testing.T) {
	ts, _, tok := setup(t)
	c, _ := api.NewClientWithResponses(ts.URL)
	resp, err := c.GetCapabilitiesWithResponse(context.Background(), bearer(tok))
	if err != nil || resp.JSON200 == nil {
		t.Fatal(err)
	}
	if len(resp.JSON200.TranscodeFormats) == 0 {
		t.Fatal("transcodeFormats must at least contain original")
	}
}

// The 501 sweep: every contract operation that is not implemented and not
// public must answer 501 not_implemented (authed). Admin-realm ops are NOT
// special-cased here: with the Task 7 middleware, a valid bearer on an admin
// operation passes auth and reaches the 501 just like any other authed op —
// cookie-session enforcement for the admin realm arrives with spec 2. This
// is a controller decision (2026-07-10) resolving the brief's flagged
// ambiguity in favor of the simpler default. The operation list comes from
// the embedded spec, so this test tracks the contract automatically.
func TestEveryUnimplementedOperationAnswersHonestly(t *testing.T) {
	ts, _, tok := setup(t)

	var doc struct {
		Paths map[string]map[string]struct {
			OperationId string                `yaml:"operationId"`
			Security    []map[string][]string `yaml:"security"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(blittarr.OpenAPISpec, &doc); err != nil {
		t.Fatal(err)
	}

	implemented := map[string]bool{"getPing": true, "getStatus": true, "getCapabilities": true}
	client := ts.Client()

	swept, got501, got400, other := 0, 0, 0, 0
	for rawPath, ops := range doc.Paths {
		for method, op := range ops {
			if implemented[op.OperationId] {
				continue
			}
			path := strings.NewReplacer(
				"{artistId}", "art_x", "{albumId}", "alb_x", "{trackId}", "trk_x",
				"{playlistId}", "pl_x", "{itemId}", "pli_x", "{artifactId}", "arf_x",
				"{artId}", "img_x", "{ref}", "trk_x", "{pairingId}", "pair_x",
				"{profileId}", "prf_x", "{deviceId}", "dev_x", "{pinId}", "pin_x",
				"{recommendationId}", "rec_x", "{partyId}", "pty_x", "{mixId}", "mood:x",
				"{genre}", "rock", "{name}", "x",
			).Replace(rawPath)

			req, _ := http.NewRequest(strings.ToUpper(method), ts.URL+path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			isPublicOp := op.Security != nil && len(op.Security) == 0
			if !isPublicOp {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			resp.Body.Close()
			swept++

			want := http.StatusNotImplemented
			switch resp.StatusCode {
			case want:
				got501++
			case http.StatusBadRequest:
				// 400 is tolerated: generated binding may reject the dummy
				// body/params before reaching the handler — still honest.
				got400++
			default:
				other++
				t.Errorf("%s %s: want %d (or 400 binding), got %d", method, path, want, resp.StatusCode)
			}
		}
	}

	t.Logf("501 sweep: swept=%d got501=%d got400=%d other=%d", swept, got501, got400, other)
	if swept > 0 && float64(got400)/float64(swept) > 0.20 {
		t.Errorf("too many 400s from the sweep (%d/%d > 20%%); investigate binding rejections instead of tolerating them", got400, swept)
	}
}
