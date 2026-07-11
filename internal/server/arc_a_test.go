package server

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/auth"
	"github.com/BlitterAmp/BlitterServer/internal/store"
	"github.com/oapi-codegen/nullable"
)

func str(s string) *string { return &s }

func srv(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st := testStore(t)
	return New(st, "test"), st
}

func profileCtx(t *testing.T, st *store.Store, name string) (context.Context, string, string) {
	t.Helper()
	ctx := context.Background()
	dev, _ := st.CreateDevice(ctx, "test device", "ios")
	p, err := st.CreateProfileRecord(ctx, name, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return auth.WithIdentity(ctx, auth.Identity{DeviceID: dev, ProfileID: p.ProfileID}), dev, p.ProfileID
}

// ── admin setup ────────────────────────────────────────────────

func TestAdminSetupOnceThenConflict(t *testing.T) {
	s, st := srv(t)
	ctx := context.Background()

	resp, err := s.AdminSetup(ctx, api.AdminSetupRequestObject{
		Body: &api.AdminSetupJSONRequestBody{Password: "a very long password"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.(api.AdminSetup204Response); !ok {
		t.Fatalf("want 204, got %T", resp)
	}
	if done, _ := st.SetupComplete(ctx); !done {
		t.Fatal("setup must be complete after AdminSetup")
	}

	resp, err = s.AdminSetup(ctx, api.AdminSetupRequestObject{
		Body: &api.AdminSetupJSONRequestBody{Password: "another long password"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.(api.AdminSetup409ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("second setup: want 409, got %T", resp)
	}
}

func TestAdminSetupRejectsShortPassword(t *testing.T) {
	s, _ := srv(t)
	_, err := s.AdminSetup(context.Background(), api.AdminSetupRequestObject{
		Body: &api.AdminSetupJSONRequestBody{Password: "short"}})
	var se *api.StatusError
	if !errors.As(err, &se) || se.Status != 400 {
		t.Fatalf("want StatusError 400, got %v", err)
	}
}

// ── identity + profiles ────────────────────────────────────────

func TestGetMeDeviceAndProfileScoped(t *testing.T) {
	s, st := srv(t)
	ctx, dev, prf := profileCtx(t, st, "Nathan")

	resp, err := s.GetMe(ctx, api.GetMeRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	me := resp.(api.GetMe200JSONResponse)
	if me.Device.DeviceId != dev || me.Profile == nil || me.Profile.ProfileId != prf {
		t.Fatalf("profile-scoped me: %+v", me)
	}

	devCtx := auth.WithIdentity(context.Background(), auth.Identity{DeviceID: dev})
	resp, err = s.GetMe(devCtx, api.GetMeRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	me = resp.(api.GetMe200JSONResponse)
	if me.Profile != nil {
		t.Fatalf("device-scoped me must have no profile: %+v", me)
	}
}

func TestListProfilesReflectsStore(t *testing.T) {
	s, st := srv(t)
	ctx := context.Background()
	st.CreateProfileRecord(ctx, "A", "1234", "#123456")
	st.CreateProfileRecord(ctx, "B", "", "")

	resp, err := s.ListProfiles(ctx, api.ListProfilesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	got := resp.(api.ListProfiles200JSONResponse)
	if len(got) != 2 || got[0].Name != "A" || !got[0].HasPin || got[1].HasPin {
		t.Fatalf("profiles: %+v", got)
	}
	if got[0].AvatarColor == nil || *got[0].AvatarColor != "#123456" {
		t.Fatalf("avatar color lost: %+v", got[0])
	}
}

func TestCreateProfileTokenFlow(t *testing.T) {
	s, st := srv(t)
	ctx := context.Background()
	dev, _ := st.CreateDevice(ctx, "d", "ios")
	devCtx := auth.WithIdentity(ctx, auth.Identity{DeviceID: dev})
	pinned, _ := st.CreateProfileRecord(ctx, "P", "2468", "")
	free, _ := st.CreateProfileRecord(ctx, "F", "", "")

	// Pinless profile, no pin needed.
	resp, err := s.CreateProfileToken(devCtx, api.CreateProfileTokenRequestObject{
		Body: &api.CreateProfileTokenJSONRequestBody{ProfileId: free.ProfileID}})
	if err != nil {
		t.Fatal(err)
	}
	ok := resp.(api.CreateProfileToken201JSONResponse)
	id, found, _ := st.ResolveToken(ctx, ok.Token)
	if !found || id.DeviceID != dev || id.ProfileID != free.ProfileID {
		t.Fatalf("minted token must resolve to (device, profile): %+v", id)
	}
	if ok.Profile.ProfileId != free.ProfileID {
		t.Fatalf("response profile: %+v", ok.Profile)
	}

	// PIN required.
	resp, _ = s.CreateProfileToken(devCtx, api.CreateProfileTokenRequestObject{
		Body: &api.CreateProfileTokenJSONRequestBody{ProfileId: pinned.ProfileID}})
	if _, isForbidden := resp.(api.CreateProfileToken403ApplicationProblemPlusJSONResponse); !isForbidden {
		t.Fatalf("missing pin: want 403, got %T", resp)
	}
	// Wrong PIN.
	resp, _ = s.CreateProfileToken(devCtx, api.CreateProfileTokenRequestObject{
		Body: &api.CreateProfileTokenJSONRequestBody{ProfileId: pinned.ProfileID, Pin: str("0000")}})
	if _, isForbidden := resp.(api.CreateProfileToken403ApplicationProblemPlusJSONResponse); !isForbidden {
		t.Fatalf("wrong pin: want 403, got %T", resp)
	}
	// Right PIN.
	resp, err = s.CreateProfileToken(devCtx, api.CreateProfileTokenRequestObject{
		Body: &api.CreateProfileTokenJSONRequestBody{ProfileId: pinned.ProfileID, Pin: str("2468")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, isOK := resp.(api.CreateProfileToken201JSONResponse); !isOK {
		t.Fatalf("right pin: want 201, got %T", resp)
	}
	// Unknown profile.
	resp, _ = s.CreateProfileToken(devCtx, api.CreateProfileTokenRequestObject{
		Body: &api.CreateProfileTokenJSONRequestBody{ProfileId: "prf_nope"}})
	if _, isNotFound := resp.(api.CreateProfileToken404ApplicationProblemPlusJSONResponse); !isNotFound {
		t.Fatalf("unknown profile: want 404, got %T", resp)
	}
}

func TestMySettingsRoundTrip(t *testing.T) {
	s, st := srv(t)
	ctx, _, prf := profileCtx(t, st, "N")

	resp, err := s.GetMySettings(ctx, api.GetMySettingsRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.(api.GetMySettings200JSONResponse); !got.ShareListening {
		t.Fatalf("default shareListening must be true: %+v", got)
	}

	off := false
	updResp, err := s.UpdateMySettings(ctx, api.UpdateMySettingsRequestObject{
		Body: &api.UpdateMySettingsJSONRequestBody{ShareListening: &off}})
	if err != nil {
		t.Fatal(err)
	}
	if got := updResp.(api.UpdateMySettings200JSONResponse); got.ShareListening {
		t.Fatalf("patch must apply: %+v", got)
	}
	p, _, _ := st.GetProfileRecord(context.Background(), prf)
	if p.ShareListening {
		t.Fatal("patch must persist")
	}
}

// ── pairing ────────────────────────────────────────────────────

func TestPairingEndpointsFullFlow(t *testing.T) {
	s, st := srv(t)
	ctx := context.Background()

	started, err := s.StartPairing(ctx, api.StartPairingRequestObject{
		Body: &api.StartPairingJSONRequestBody{DeviceName: "iPhone", DeviceType: "ios"}})
	if err != nil {
		t.Fatal(err)
	}
	pr := started.(api.StartPairing201JSONResponse)
	if pr.PairingId == "" || len(pr.Code) != 6 {
		t.Fatalf("start: %+v", pr)
	}

	poll, err := s.GetPairing(ctx, api.GetPairingRequestObject{PairingId: pr.PairingId})
	if err != nil {
		t.Fatal(err)
	}
	state := poll.(api.GetPairing200JSONResponse)
	if state.Status != "pending" || state.Token != nil {
		t.Fatalf("pending poll: %+v", state)
	}

	if err := st.ApprovePairing(ctx, pr.PairingId); err != nil {
		t.Fatal(err)
	}
	poll, _ = s.GetPairing(ctx, api.GetPairingRequestObject{PairingId: pr.PairingId})
	state = poll.(api.GetPairing200JSONResponse)
	if state.Status != "approved" || state.Token == nil || state.DeviceId == nil {
		t.Fatalf("approved poll must deliver token: %+v", state)
	}
	if id, found, _ := st.ResolveToken(ctx, *state.Token); !found || id.DeviceID != *state.DeviceId {
		t.Fatal("delivered token must resolve")
	}

	poll, _ = s.GetPairing(ctx, api.GetPairingRequestObject{PairingId: pr.PairingId})
	state = poll.(api.GetPairing200JSONResponse)
	if state.Status != "approved" || state.Token != nil {
		t.Fatalf("second poll must not re-deliver: %+v", state)
	}

	notFound, _ := s.GetPairing(ctx, api.GetPairingRequestObject{PairingId: "pair_nope"})
	if _, is404 := notFound.(api.GetPairing404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("unknown pairing: want 404, got %T", notFound)
	}
}

func TestClaimPairCodeEndpoint(t *testing.T) {
	s, st := srv(t)
	ctx := context.Background()
	code, _, _ := st.CreatePairCode(ctx)

	resp, err := s.ClaimPairCode(ctx, api.ClaimPairCodeRequestObject{
		Body: &api.ClaimPairCodeJSONRequestBody{Code: code, DeviceName: "iPhone", DeviceType: "ios"}})
	if err != nil {
		t.Fatal(err)
	}
	claimed := resp.(api.ClaimPairCode201JSONResponse)
	if claimed.Token == "" || !strings.HasPrefix(claimed.DeviceId, "dev_") {
		t.Fatalf("claim: %+v", claimed)
	}

	resp, _ = s.ClaimPairCode(ctx, api.ClaimPairCodeRequestObject{
		Body: &api.ClaimPairCodeJSONRequestBody{Code: code, DeviceName: "again", DeviceType: "ios"}})
	if _, is410 := resp.(api.ClaimPairCode410ApplicationProblemPlusJSONResponse); !is410 {
		t.Fatalf("used code: want 410, got %T", resp)
	}
	resp, _ = s.ClaimPairCode(ctx, api.ClaimPairCodeRequestObject{
		Body: &api.ClaimPairCodeJSONRequestBody{Code: "ZZZZZZ", DeviceName: "x", DeviceType: "ios"}})
	if _, is404 := resp.(api.ClaimPairCode404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("unknown code: want 404, got %T", resp)
	}
}

// ── admin: state, profiles, pairings, pair codes, devices ─────

func TestAdminGetStateCountsAndFlags(t *testing.T) {
	s, st := srv(t)
	ctx := context.Background()
	st.CreateProfileRecord(ctx, "N", "", "")
	st.CreateDevice(ctx, "d", "ios")
	st.StartPairing(ctx, "p", "ios", "")

	resp, err := s.AdminGetState(ctx, api.AdminGetStateRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	state := resp.(api.AdminGetState200JSONResponse)
	if state.SetupComplete || state.ProfileCount != 1 || state.DeviceCount != 1 || state.PendingPairings != 1 {
		t.Fatalf("state: %+v", state)
	}
	if state.CanonicalUrlSet == nil || *state.CanonicalUrlSet {
		t.Fatal("canonicalUrlSet must be false on a fresh instance")
	}
}

func TestAdminProfileCRUD(t *testing.T) {
	s, _ := srv(t)
	ctx := context.Background()

	created, err := s.AdminCreateProfile(ctx, api.AdminCreateProfileRequestObject{
		Body: &api.AdminCreateProfileJSONRequestBody{Name: "Nathan", Pin: str("1234"), AvatarColor: str("#abc")}})
	if err != nil {
		t.Fatal(err)
	}
	p := created.(api.AdminCreateProfile201JSONResponse)
	if p.Name != "Nathan" || !p.HasPin {
		t.Fatalf("create: %+v", p)
	}

	// Rename, clear PIN via explicit null.
	updated, err := s.AdminUpdateProfile(ctx, api.AdminUpdateProfileRequestObject{
		ProfileId: p.ProfileId,
		Body: &api.AdminUpdateProfileJSONRequestBody{
			Name: str("Nate"), Pin: nullable.NewNullNullable[string]()}})
	if err != nil {
		t.Fatal(err)
	}
	u := updated.(api.AdminUpdateProfile200JSONResponse)
	if u.Name != "Nate" || u.HasPin {
		t.Fatalf("update must rename and clear pin: %+v", u)
	}

	// Absent pin field keeps state.
	updated, err = s.AdminUpdateProfile(ctx, api.AdminUpdateProfileRequestObject{
		ProfileId: p.ProfileId,
		Body:      &api.AdminUpdateProfileJSONRequestBody{AvatarColor: str("#def")}})
	if err != nil {
		t.Fatal(err)
	}
	u = updated.(api.AdminUpdateProfile200JSONResponse)
	if u.HasPin || u.AvatarColor == nil || *u.AvatarColor != "#def" {
		t.Fatalf("partial update: %+v", u)
	}

	list, _ := s.AdminListProfiles(ctx, api.AdminListProfilesRequestObject{})
	if got := list.(api.AdminListProfiles200JSONResponse); len(got) != 1 {
		t.Fatalf("list: %+v", got)
	}

	del, err := s.AdminDeleteProfile(ctx, api.AdminDeleteProfileRequestObject{ProfileId: p.ProfileId})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := del.(api.AdminDeleteProfile204Response); !is204 {
		t.Fatalf("delete: want 204, got %T", del)
	}
	del, _ = s.AdminDeleteProfile(ctx, api.AdminDeleteProfileRequestObject{ProfileId: p.ProfileId})
	if _, is404 := del.(api.AdminDeleteProfile404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("double delete: want 404, got %T", del)
	}
	upd404, _ := s.AdminUpdateProfile(ctx, api.AdminUpdateProfileRequestObject{
		ProfileId: "prf_nope", Body: &api.AdminUpdateProfileJSONRequestBody{}})
	if _, is404 := upd404.(api.AdminUpdateProfile404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("update unknown: want 404, got %T", upd404)
	}
}

func TestAdminPairingModeration(t *testing.T) {
	s, st := srv(t)
	ctx := context.Background()
	p1, _ := st.StartPairing(ctx, "a", "ios", "")
	p2, _ := st.StartPairing(ctx, "b", "desktop", "")

	list, err := s.AdminListPairings(ctx, api.AdminListPairingsRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if got := list.(api.AdminListPairings200JSONResponse); len(got) != 2 || got[0].PairingId != p1.PairingID {
		t.Fatalf("pending list: %+v", got)
	}

	ok, err := s.AdminApprovePairing(ctx, api.AdminApprovePairingRequestObject{PairingId: p1.PairingID})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := ok.(api.AdminApprovePairing204Response); !is204 {
		t.Fatalf("approve: want 204, got %T", ok)
	}
	deny, _ := s.AdminDenyPairing(ctx, api.AdminDenyPairingRequestObject{PairingId: p2.PairingID})
	if _, is204 := deny.(api.AdminDenyPairing204Response); !is204 {
		t.Fatalf("deny: want 204, got %T", deny)
	}
	nf, _ := s.AdminApprovePairing(ctx, api.AdminApprovePairingRequestObject{PairingId: "pair_nope"})
	if _, is404 := nf.(api.AdminApprovePairing404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("approve unknown: want 404, got %T", nf)
	}
}

func TestAdminCreatePairCodeNeedsCanonicalURL(t *testing.T) {
	s, st := srv(t)
	ctx := context.Background()

	resp, err := s.AdminCreatePairCode(ctx, api.AdminCreatePairCodeRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if _, is409 := resp.(api.AdminCreatePairCode409ApplicationProblemPlusJSONResponse); !is409 {
		t.Fatalf("no canonical url: want 409, got %T", resp)
	}

	st.SetSetting(ctx, "canonical_url", "https://music.example.net")
	resp, err = s.AdminCreatePairCode(ctx, api.AdminCreatePairCodeRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	pc := resp.(api.AdminCreatePairCode201JSONResponse)
	if pc.CanonicalUrl != "https://music.example.net" || len(pc.Code) != 6 {
		t.Fatalf("pair code: %+v", pc)
	}
	want := "blitteramp://pair?server=" + url.QueryEscape("https://music.example.net") + "&code=" + pc.Code
	if pc.QrPayload != want {
		t.Fatalf("qrPayload: want %q got %q", want, pc.QrPayload)
	}
}

func TestAdminDevices(t *testing.T) {
	s, st := srv(t)
	ctx := context.Background()
	dev, _ := st.CreateDevice(ctx, "iPhone", "ios")
	tok, _ := st.CreateDeviceToken(ctx, dev)

	list, err := s.AdminListDevices(ctx, api.AdminListDevicesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if got := list.(api.AdminListDevices200JSONResponse); len(got) != 1 || got[0].DeviceId != dev || got[0].Type != "ios" {
		t.Fatalf("devices: %+v", got)
	}

	rev, err := s.AdminRevokeDevice(ctx, api.AdminRevokeDeviceRequestObject{DeviceId: dev})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := rev.(api.AdminRevokeDevice204Response); !is204 {
		t.Fatalf("revoke: want 204, got %T", rev)
	}
	if _, found, _ := st.ResolveToken(ctx, tok); found {
		t.Fatal("revoked device's token must stop resolving")
	}
	rev, _ = s.AdminRevokeDevice(ctx, api.AdminRevokeDeviceRequestObject{DeviceId: dev})
	if _, is404 := rev.(api.AdminRevokeDevice404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("revoke unknown: want 404, got %T", rev)
	}
}

// ── admin settings ─────────────────────────────────────────────

func TestServerSettingsRoundTrip(t *testing.T) {
	s, _ := srv(t)
	ctx := context.Background()

	resp, err := s.AdminGetServerSettings(ctx, api.AdminGetServerSettingsRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.(api.AdminGetServerSettings200JSONResponse); got.CanonicalUrl != nil {
		t.Fatalf("fresh canonicalUrl must be null: %+v", got)
	}

	set, err := s.AdminSetServerSettings(ctx, api.AdminSetServerSettingsRequestObject{
		Body: &api.AdminSetServerSettingsJSONRequestBody{CanonicalUrl: str("https://music.example.net")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := set.(api.AdminSetServerSettings204Response); !is204 {
		t.Fatalf("set: want 204, got %T", set)
	}
	resp, _ = s.AdminGetServerSettings(ctx, api.AdminGetServerSettingsRequestObject{})
	got := resp.(api.AdminGetServerSettings200JSONResponse)
	if got.CanonicalUrl == nil || *got.CanonicalUrl != "https://music.example.net" {
		t.Fatalf("round trip: %+v", got)
	}
}

func TestTranscodeSettingsDefaultsAndRoundTrip(t *testing.T) {
	s, _ := srv(t)
	ctx := context.Background()

	resp, err := s.AdminGetTranscodeSettings(ctx, api.AdminGetTranscodeSettingsRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	got := resp.(api.AdminGetTranscodeSettings200JSONResponse)
	if got.DefaultFormat != "original" || got.DefaultBitrateKbps != 256 || got.ArtifactCacheMaxBytes <= 0 {
		t.Fatalf("defaults: %+v", got)
	}

	set, err := s.AdminSetTranscodeSettings(ctx, api.AdminSetTranscodeSettingsRequestObject{
		Body: &api.AdminSetTranscodeSettingsJSONRequestBody{
			DefaultFormat: "aac", DefaultBitrateKbps: 192, ArtifactCacheMaxBytes: 1 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := set.(api.AdminSetTranscodeSettings204Response); !is204 {
		t.Fatalf("set: want 204, got %T", set)
	}
	resp, _ = s.AdminGetTranscodeSettings(ctx, api.AdminGetTranscodeSettingsRequestObject{})
	got = resp.(api.AdminGetTranscodeSettings200JSONResponse)
	if got.DefaultFormat != "aac" || got.DefaultBitrateKbps != 192 || got.ArtifactCacheMaxBytes != 1<<30 {
		t.Fatalf("round trip: %+v", got)
	}
}

// ── last.fm (honest absent-integration answers) ───────────────

func TestMyLastfmHonestlyAbsent(t *testing.T) {
	s, st := srv(t)
	ctx, _, _ := profileCtx(t, st, "N")

	resp, err := s.GetMyLastfm(ctx, api.GetMyLastfmRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	got := resp.(api.GetMyLastfm200JSONResponse)
	if got.Available || got.Connected {
		t.Fatalf("no instance creds: %+v", got)
	}

	conn, err := s.ConnectMyLastfm(ctx, api.ConnectMyLastfmRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if _, is409 := conn.(api.ConnectMyLastfm409ApplicationProblemPlusJSONResponse); !is409 {
		t.Fatalf("connect without creds: want 409, got %T", conn)
	}

	disc, err := s.DisconnectMyLastfm(ctx, api.DisconnectMyLastfmRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := disc.(api.DisconnectMyLastfm204Response); !is204 {
		t.Fatalf("disconnect: want 204, got %T", disc)
	}
}
