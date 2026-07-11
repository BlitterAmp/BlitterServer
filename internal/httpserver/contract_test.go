package httpserver_test

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	blitterserver "github.com/BlitterAmp/BlitterServer"
	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/httpserver"
	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/store"
	"gopkg.in/yaml.v3"
)

func setup(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mgr := library.NewManager(st, t.TempDir())
	ts := httptest.NewServer(httpserver.Handler(st, mgr, t.TempDir(), "test"))
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
	if resp.JSON200.Name != "BlitterServer" {
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

// adminSession completes first-run setup and logs in, returning a client
// whose cookie jar carries the blitter_admin session.
func adminSession(t *testing.T, ts *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	resp, err := client.Post(ts.URL+"/admin/api/setup", "application/json",
		strings.NewReader(`{"password":"contract-test-password"}`))
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("setup: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
	resp, err = client.Post(ts.URL+"/admin/api/session", "application/json",
		strings.NewReader(`{"password":"contract-test-password"}`))
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("login: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
	return client
}

// The 501 sweep: every contract operation that is not implemented must answer
// 501 not_implemented when called with proper credentials for its realm
// (profile bearer for /v1, session cookie for /admin/api). Uncredentialed
// admin calls must 401. The operation list comes from the embedded spec, so
// this test tracks the contract automatically.
func TestEveryUnimplementedOperationAnswersHonestly(t *testing.T) {
	ts, _, tok := setup(t)
	admin := adminSession(t, ts)

	var doc struct {
		Paths map[string]map[string]struct {
			OperationId string                `yaml:"operationId"`
			Security    []map[string][]string `yaml:"security"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(blitterserver.OpenAPISpec, &doc); err != nil {
		t.Fatal(err)
	}

	implemented := map[string]bool{
		"getPing": true, "getStatus": true, "getCapabilities": true,
		"getMe": true, "listProfiles": true, "createProfileToken": true,
		"getMySettings": true, "updateMySettings": true,
		"getMyLastfm": true, "connectMyLastfm": true, "disconnectMyLastfm": true,
		"startPairing": true, "getPairing": true, "claimPairCode": true,
		"adminSetup": true, "adminLogin": true, "adminLogout": true,
		"adminGetState": true, "adminListProfiles": true, "adminCreateProfile": true,
		"adminUpdateProfile": true, "adminDeleteProfile": true,
		"adminListPairings": true, "adminApprovePairing": true, "adminDenyPairing": true,
		"adminCreatePairCode": true, "adminListDevices": true, "adminRevokeDevice": true,
		"adminGetServerSettings": true, "adminSetServerSettings": true,
		"adminGetTranscodeSettings": true, "adminSetTranscodeSettings": true,
		"getLibrary": true, "listArtists": true, "getArtist": true,
		"listArtistAlbums": true, "listArtistTracks": true,
		"listAlbums": true, "getAlbum": true, "listAlbumTracks": true,
		"listTracks": true, "getTrack": true, "listGenres": true, "listGenreTracks": true,
		"search": true, "streamTrack": true, "createStreamGrant": true, "getArt": true,
		"adminGetFilesystemSource": true, "adminSetFilesystemSource": true,
		"adminDeleteFilesystemSource": true, "adminScanFilesystemSource": true,
		"listPlaylists": true, "createPlaylist": true, "getPlaylist": true,
		"updatePlaylist": true, "deletePlaylist": true, "listPlaylistTracks": true,
		"appendPlaylistTracks": true, "removePlaylistTrack": true,
		"setRating": true, "setLove": true, "listLoves": true,
		"reportPlaybackEvents": true, "getTasteSnapshot": true, "getPresence": true,
		"createRecommendation": true, "listRecommendations": true, "markRecommendationSeen": true,
		"streamEvents":     true,
		"requestArtifacts": true, "getArtifact": true, "releaseArtifact": true,
		"downloadArtifact": true,
		"getHome":          true, "listMixes": true, "listMixTracks": true, "getRadioNext": true,
		"getMyDiscover": true, "listSimilarArtists": true, "getExternalArtist": true,
		"getAcquisitionActivity": true,
		"listParties":            true, "createParty": true, "getParty": true, "endParty": true,
		"inviteToParty": true, "joinParty": true, "leaveParty": true,
		"appendPartyQueue": true, "partyTransport": true, "kickFromParty": true,
		"adminGetLidarr": true, "adminSetLidarr": true, "adminDeleteLidarr": true,
		"adminTestLidarr": true,
		"adminGetLastfm":  true, "adminSetLastfm": true, "adminDeleteLastfm": true,
	}
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

			isAdmin := strings.HasPrefix(rawPath, "/admin/")
			if isAdmin {
				// Uncredentialed admin calls must be rejected, not 501'd.
				bare, _ := http.NewRequest(strings.ToUpper(method), ts.URL+path, strings.NewReader("{}"))
				bare.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(bare)
				if err != nil {
					t.Fatalf("%s %s: %v", method, path, err)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s %s without session: want 401, got %d", method, path, resp.StatusCode)
				}
			}

			req, _ := http.NewRequest(strings.ToUpper(method), ts.URL+path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			do := client
			if isAdmin {
				do = admin
			} else if op.Security == nil || len(op.Security) != 0 {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			resp, err := do.Do(req)
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

// ── Arc A end-to-end flows over the wire ───────────────────────

func TestContractAdminSessionLifecycle(t *testing.T) {
	ts, _, _ := setup(t)

	// Login before setup must fail.
	pre, _ := http.Post(ts.URL+"/admin/api/session", "application/json",
		strings.NewReader(`{"password":"whatever-long"}`))
	if pre.StatusCode != 401 {
		t.Fatalf("login before setup: want 401, got %d", pre.StatusCode)
	}
	pre.Body.Close()

	admin := adminSession(t, ts)

	// Wrong password after setup.
	bad, _ := http.Post(ts.URL+"/admin/api/session", "application/json",
		strings.NewReader(`{"password":"wrong-password"}`))
	if bad.StatusCode != 401 {
		t.Fatalf("wrong password: want 401, got %d", bad.StatusCode)
	}
	bad.Body.Close()

	// Session works, then logout kills it.
	state, err := admin.Get(ts.URL + "/admin/api/state")
	if err != nil || state.StatusCode != 200 {
		t.Fatalf("state with session: %v %d", err, state.StatusCode)
	}
	state.Body.Close()

	req, _ := http.NewRequest("DELETE", ts.URL+"/admin/api/session", nil)
	out, err := admin.Do(req)
	if err != nil || out.StatusCode != 204 {
		t.Fatalf("logout: %v %d", err, out.StatusCode)
	}
	out.Body.Close()

	state, _ = admin.Get(ts.URL + "/admin/api/state")
	if state.StatusCode != 401 {
		t.Fatalf("state after logout: want 401, got %d", state.StatusCode)
	}
	state.Body.Close()
}

func TestContractPinPairingEndToEnd(t *testing.T) {
	ts, _, _ := setup(t)
	admin := adminSession(t, ts)
	c, _ := api.NewClientWithResponses(ts.URL)
	ctx := context.Background()

	// Admin creates a profile with a PIN.
	created, err := c.AdminCreateProfileWithResponse(ctx,
		api.AdminCreateProfileJSONRequestBody{Name: "Nathan", Pin: ptr("1234")},
		withClient(admin))
	if err != nil || created.JSON201 == nil {
		t.Fatalf("admin create profile: %v %d", err, created.StatusCode())
	}

	// Device starts PIN pairing.
	started, err := c.StartPairingWithResponse(ctx,
		api.StartPairingJSONRequestBody{DeviceName: "Nathan's iPhone", DeviceType: "ios"})
	if err != nil || started.JSON201 == nil {
		t.Fatalf("start pairing: %v %d", err, started.StatusCode())
	}
	pairingID := started.JSON201.PairingId

	// Admin sees it pending and approves.
	pending, err := c.AdminListPairingsWithResponse(ctx, withClient(admin))
	if err != nil || pending.JSON200 == nil || len(*pending.JSON200) != 1 {
		t.Fatalf("pending pairings: %v %+v", err, pending.JSON200)
	}
	approved, err := c.AdminApprovePairingWithResponse(ctx, pairingID, withClient(admin))
	if err != nil || approved.StatusCode() != 204 {
		t.Fatalf("approve: %v %d", err, approved.StatusCode())
	}

	// Device polls and receives its device token exactly once.
	poll, err := c.GetPairingWithResponse(ctx, pairingID)
	if err != nil || poll.JSON200 == nil || poll.JSON200.Status != "approved" || poll.JSON200.Token == nil {
		t.Fatalf("poll: %v %+v", err, poll.JSON200)
	}
	deviceToken := *poll.JSON200.Token

	again, _ := c.GetPairingWithResponse(ctx, pairingID)
	if again.JSON200 == nil || again.JSON200.Token != nil {
		t.Fatalf("second poll must not re-deliver the token: %+v", again.JSON200)
	}

	// Device token: list profiles, exchange with PIN, then act as the profile.
	profiles, err := c.ListProfilesWithResponse(ctx, bearer(deviceToken))
	if err != nil || profiles.JSON200 == nil {
		t.Fatalf("list profiles with device token: %v %d", err, profiles.StatusCode())
	}
	var target api.Profile
	for _, p := range *profiles.JSON200 {
		if p.Name == "Nathan" {
			target = p
		}
	}
	if target.ProfileId == "" {
		t.Fatalf("created profile missing from list: %+v", *profiles.JSON200)
	}

	denied, _ := c.CreateProfileTokenWithResponse(ctx,
		api.CreateProfileTokenJSONRequestBody{ProfileId: target.ProfileId}, bearer(deviceToken))
	if denied.StatusCode() != 403 {
		t.Fatalf("pin-less exchange for pinned profile: want 403, got %d", denied.StatusCode())
	}
	minted, err := c.CreateProfileTokenWithResponse(ctx,
		api.CreateProfileTokenJSONRequestBody{ProfileId: target.ProfileId, Pin: ptr("1234")},
		bearer(deviceToken))
	if err != nil || minted.JSON201 == nil {
		t.Fatalf("exchange: %v %d", err, minted.StatusCode())
	}

	// Device token may not do profile work; profile token may.
	forbidden, _ := c.GetMySettingsWithResponse(ctx, bearer(deviceToken))
	if forbidden.StatusCode() != 403 {
		t.Fatalf("device token on profile op: want 403, got %d", forbidden.StatusCode())
	}
	me, err := c.GetMeWithResponse(ctx, bearer(minted.JSON201.Token))
	if err != nil || me.JSON200 == nil || me.JSON200.Profile == nil || me.JSON200.Profile.ProfileId != target.ProfileId {
		t.Fatalf("me with profile token: %v %+v", err, me.JSON200)
	}
}

func TestContractQRPairingEndToEnd(t *testing.T) {
	ts, _, _ := setup(t)
	admin := adminSession(t, ts)
	c, _ := api.NewClientWithResponses(ts.URL)
	ctx := context.Background()

	// Pair codes need the canonical URL first.
	blocked, err := c.AdminCreatePairCodeWithResponse(ctx, withClient(admin))
	if err != nil || blocked.StatusCode() != 409 {
		t.Fatalf("pair code without canonical url: %v %d", err, blocked.StatusCode())
	}
	saved, err := c.AdminSetServerSettingsWithResponse(ctx,
		api.AdminSetServerSettingsJSONRequestBody{CanonicalUrl: ptr("https://music.example.net")},
		withClient(admin))
	if err != nil || saved.StatusCode() != 204 {
		t.Fatalf("set canonical url: %v %d", err, saved.StatusCode())
	}

	code, err := c.AdminCreatePairCodeWithResponse(ctx, withClient(admin))
	if err != nil || code.JSON201 == nil {
		t.Fatalf("pair code: %v %d", err, code.StatusCode())
	}
	if !strings.Contains(code.JSON201.QrPayload, "blitteramp://pair?server=") {
		t.Fatalf("qrPayload: %q", code.JSON201.QrPayload)
	}

	claimed, err := c.ClaimPairCodeWithResponse(ctx, api.ClaimPairCodeJSONRequestBody{
		Code: code.JSON201.Code, DeviceName: "Nathan's iPhone", DeviceType: "ios"})
	if err != nil || claimed.JSON201 == nil {
		t.Fatalf("claim: %v %d", err, claimed.StatusCode())
	}

	// Claimed device token is live; the code is single-use.
	me, err := c.GetMeWithResponse(ctx, bearer(claimed.JSON201.Token))
	if err != nil || me.JSON200 == nil || me.JSON200.Device.DeviceId != claimed.JSON201.DeviceId {
		t.Fatalf("me with claimed token: %v %+v", err, me.JSON200)
	}
	replay, _ := c.ClaimPairCodeWithResponse(ctx, api.ClaimPairCodeJSONRequestBody{
		Code: code.JSON201.Code, DeviceName: "attacker", DeviceType: "other"})
	if replay.StatusCode() != 410 {
		t.Fatalf("replayed code: want 410, got %d", replay.StatusCode())
	}
}

func ptr[T any](v T) *T { return &v }

// withClient routes a generated-client call through an *http.Client that
// carries the admin cookie jar.
func withClient(hc *http.Client) api.RequestEditorFn {
	return func(ctx context.Context, req *http.Request) error {
		for _, c := range hc.Jar.Cookies(req.URL) {
			req.AddCookie(c)
		}
		return nil
	}
}

// The admin SPA shell is public (it renders the login screen); the API
// realm behind it stays cookie-gated.
func TestContractAdminSPAServed(t *testing.T) {
	if _, err := fs.Stat(blitterserver.AdminSPA, "web/admin/dist/index.html"); err != nil {
		t.Skip("admin SPA not built into this test binary (run `make web`)")
	}
	ts, _, _ := setup(t)

	resp, err := http.Get(ts.URL + "/admin/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("spa shell: %v %d", err, resp.StatusCode)
	}
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	resp.Body.Close()
	page := string(body[:n])
	if !strings.Contains(page, "BlitterServer Admin") {
		t.Fatalf("spa shell content: %q", page)
	}
	// The shell references hashed assets that must resolve.
	start := strings.Index(page, "/admin/assets/")
	if start < 0 {
		t.Fatalf("no asset reference in shell: %q", page)
	}
	end := start
	for end < len(page) && page[end] != '"' {
		end++
	}
	asset, err := http.Get(ts.URL + page[start:end])
	if err != nil || asset.StatusCode != 200 {
		t.Fatalf("asset %s: %v %d", page[start:end], err, asset.StatusCode)
	}
	asset.Body.Close()

	// Root now lands on the admin console.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	root, err := noRedirect.Get(ts.URL + "/")
	if err != nil || root.StatusCode != http.StatusTemporaryRedirect || root.Header.Get("Location") != "/admin/" {
		t.Fatalf("root redirect: %v %d %q", err, root.StatusCode, root.Header.Get("Location"))
	}
	root.Body.Close()
}
