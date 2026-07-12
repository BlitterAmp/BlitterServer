package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/auth"
	"github.com/BlitterAmp/BlitterServer/internal/lastfm"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

type fakeLastfm struct {
	mu                                    sync.Mutex
	exchange                              lastfm.Session
	exchangeErr                           error
	nowPlaying, scrobbles, loves, unloves int
	scrobbleResults                       []lastfm.ScrobbleResult
	scrobbleErrors                        []error
	top                                   []lastfm.Artist
	similar                               []lastfm.Artist
	artist                                lastfm.Artist
	discoveryErr                          error
	nowPlayingEntered, nowPlayingRelease  chan struct{}
}

func (f *fakeLastfm) Exchange(context.Context, string) (lastfm.Session, error) {
	return f.exchange, f.exchangeErr
}
func (f *fakeLastfm) NowPlaying(context.Context, string, lastfm.Track) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nowPlaying++
	if f.nowPlayingEntered != nil {
		select {
		case <-f.nowPlayingEntered:
		default:
			close(f.nowPlayingEntered)
		}
		release := f.nowPlayingRelease
		f.mu.Unlock()
		<-release
		f.mu.Lock()
	}
	return f.exchangeErr
}
func (f *fakeLastfm) Scrobble(context.Context, string, lastfm.Track) (lastfm.ScrobbleResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scrobbles++
	var r lastfm.ScrobbleResult
	var e error
	if len(f.scrobbleResults) > 0 {
		r = f.scrobbleResults[0]
		f.scrobbleResults = f.scrobbleResults[1:]
	}
	if len(f.scrobbleErrors) > 0 {
		e = f.scrobbleErrors[0]
		f.scrobbleErrors = f.scrobbleErrors[1:]
	}
	return r, e
}
func (f *fakeLastfm) Love(_ context.Context, _ string, _ lastfm.Track, loved bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if loved {
		f.loves++
	} else {
		f.unloves++
	}
	return f.exchangeErr
}
func (f *fakeLastfm) Similar(context.Context, string, int) ([]lastfm.Artist, error) {
	return f.similar, f.discoveryErr
}
func (f *fakeLastfm) ArtistInfo(context.Context, string) (lastfm.Artist, error) {
	return f.artist, f.discoveryErr
}
func (f *fakeLastfm) TopArtists(context.Context, string, int) ([]lastfm.Artist, error) {
	return f.top, f.discoveryErr
}

func wireFake(t *testing.T) (*Server, *store.Store, context.Context, context.Context, *fakeLastfm) {
	t.Helper()
	s, st, _, ctx1, ctx2 := dataSrv(t)
	s.Close()
	s.lastfmWorker = newLastfmWorker(s)
	f := &fakeLastfm{exchange: lastfm.Session{Username: "synthetic_user", Key: "synthetic_session"}, scrobbleResults: []lastfm.ScrobbleResult{{Outcome: lastfm.ScrobbleAccepted}}}
	s.lastfmFactory = func(string, string) lastfmProvider { return f }
	_ = st.SetSetting(context.Background(), settingLastfmAPIKey, "synthetic_api_key")
	_ = st.SetSetting(context.Background(), settingLastfmSharedSecret, "synthetic_secret")
	return s, st, ctx1, ctx2, f
}
func runLastfmWorker(t *testing.T, s *Server, now time.Time) {
	t.Helper()
	if err := s.lastfmWorker.runOnce(now); err != nil {
		t.Fatal(err)
	}
}
func connectFake(t *testing.T, st *store.Store, ctx context.Context) {
	t.Helper()
	id, _ := auth.IdentityFrom(ctx)
	if err := st.SetLastfmConnection(context.Background(), id.ProfileID, "synthetic_user", "synthetic_session"); err != nil {
		t.Fatal(err)
	}
}
func report(t *testing.T, s *Server, ctx context.Context, id, session, kind, track string, pos *float32, at time.Time) {
	t.Helper()
	var sessionID *string
	if session != "" {
		sessionID = &session
	}
	_, err := s.ReportPlaybackEvents(ctx, api.ReportPlaybackEventsRequestObject{Body: &api.ReportPlaybackEventsJSONRequestBody{Events: []api.PlaybackEvent{{EventId: id, PlaySessionId: sessionID, Type: api.PlaybackEventType(kind), TrackId: track, PositionSec: pos, At: at}}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLastfmCallbackStateAndProfileIsolation(t *testing.T) {
	s, st, ctx1, ctx2, f := wireFake(t)
	_ = st.SetSetting(context.Background(), settingCanonicalURL, "https://music.example.test/base")
	resp, err := s.ConnectMyLastfm(ctx1, api.ConnectMyLastfmRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	authURL, _ := url.Parse(resp.(api.ConnectMyLastfm201JSONResponse).Url)
	cb, _ := url.Parse(authURL.Query().Get("cb"))
	if cb.Scheme != "https" || cb.Path != "/v1/lastfm/callback" {
		t.Fatalf("callback: %s", cb)
	}
	state := cb.Query().Get("state")
	bad, _ := s.CompleteLastfmAuth(context.Background(), api.CompleteLastfmAuthRequestObject{Params: api.CompleteLastfmAuthParams{State: state, Token: "bad"}})
	if _, ok := bad.(api.CompleteLastfmAuth400TexthtmlResponse); !ok {
		t.Fatalf("malformed: %T", bad)
	}
	f.exchangeErr = &lastfm.ProviderError{Kind: lastfm.ErrorTemporary, Code: 11}
	valid := strings.Repeat("a", 32)
	_, _ = s.CompleteLastfmAuth(context.Background(), api.CompleteLastfmAuthRequestObject{Params: api.CompleteLastfmAuthParams{State: state, Token: valid}})
	f.exchangeErr = nil
	okResp, err := s.CompleteLastfmAuth(context.Background(), api.CompleteLastfmAuthRequestObject{Params: api.CompleteLastfmAuthParams{State: state, Token: valid}})
	if err != nil {
		t.Fatal(err)
	}
	ok := okResp.(api.CompleteLastfmAuth200TexthtmlResponse)
	if ok.Headers.CacheControl == nil || *ok.Headers.CacheControl != "no-store" || ok.Headers.ReferrerPolicy == nil || ok.Headers.ContentSecurityPolicy == nil {
		t.Fatalf("headers: %#v", ok.Headers)
	}
	mine, _ := s.GetMyLastfm(ctx1, api.GetMyLastfmRequestObject{})
	other, _ := s.GetMyLastfm(ctx2, api.GetMyLastfmRequestObject{})
	if !mine.(api.GetMyLastfm200JSONResponse).Connected || other.(api.GetMyLastfm200JSONResponse).Connected {
		t.Fatal("profile isolation failed")
	}
	again, _ := s.CompleteLastfmAuth(context.Background(), api.CompleteLastfmAuthRequestObject{Params: api.CompleteLastfmAuthParams{State: state, Token: valid}})
	if _, ok := again.(api.CompleteLastfmAuth400TexthtmlResponse); !ok {
		t.Fatal("state reused")
	}
}

func TestLastfmCanonicalURLPolicy(t *testing.T) {
	for _, tc := range []struct {
		u  string
		ok bool
	}{{"http://example.test", false}, {"%%%", false}, {"http://127.0.0.1:8484", true}, {"http://localhost:8484", true}, {"https://music.example.test", true}} {
		s, st, ctx, _, _ := wireFake(t)
		_ = st.SetSetting(context.Background(), settingCanonicalURL, tc.u)
		r, _ := s.ConnectMyLastfm(ctx, api.ConnectMyLastfmRequestObject{})
		_, got := r.(api.ConnectMyLastfm201JSONResponse)
		if got != tc.ok {
			t.Fatalf("%s allowed=%v", tc.u, got)
		}
		if !tc.ok {
			cats, _ := st.LastfmDataCategories(context.Background(), authID(ctx))
			if len(cats) != 0 {
				t.Fatalf("rejected URL persisted attempt: %v", cats)
			}
		}
	}
}

func TestNowPlayingFailureIsAttemptedFailedAndNeverRetried(t *testing.T) {
	s, st, ctx, _, f := wireFake(t)
	connectFake(t, st, ctx)
	tr := firstTrack(t, st)
	f.exchangeErr = &lastfm.ProviderError{Kind: lastfm.ErrorTemporary, Code: 11}
	zero := float32(0)
	at := time.Now().UTC()
	report(t, s, ctx, "np-event", "np-session", "started", tr.TrackID, &zero, at)
	runLastfmWorker(t, s, at)
	report(t, s, ctx, "np-event", "changed-session", "started", tr.TrackID, &zero, at.Add(time.Minute))
	runLastfmWorker(t, s, at.Add(time.Minute))
	if f.nowPlaying != 1 {
		t.Fatalf("now-playing attempts=%d", f.nowPlaying)
	}
	state, outcome, err := st.LastfmNowPlayingStatus(context.Background(), "np-session")
	if err != nil || state != "attempted" || outcome != "failed" {
		t.Fatalf("status: %s/%s %v", state, outcome, err)
	}
}

func TestDuplicatePlaybackEventCannotTriggerScrobble(t *testing.T) {
	s, st, ctx, _, f := wireFake(t)
	connectFake(t, st, ctx)
	tr := firstTrack(t, st)
	zero := float32(0)
	at := time.Now().UTC()
	report(t, s, ctx, "immutable-event", "original-session", "started", tr.TrackID, &zero, at)
	runLastfmWorker(t, s, at)
	threshold := float32(240)
	report(t, s, ctx, "immutable-event", "changed-session", "progress", tr.TrackID, &threshold, at.Add(4*time.Minute))
	runLastfmWorker(t, s, at.Add(4*time.Minute))
	if f.scrobbles != 0 {
		t.Fatalf("duplicate triggered %d scrobbles", f.scrobbles)
	}
}

func TestLastfmPlaybackThresholdDedupAndOutcomes(t *testing.T) {
	s, st, ctx, _, f := wireFake(t)
	connectFake(t, st, ctx)
	tr := firstTrack(t, st)
	start := time.Now().UTC().Add(-time.Minute)
	zero := float32(0)
	report(t, s, ctx, "start", "", "started", tr.TrackID, &zero, start)
	runLastfmWorker(t, s, start)
	early := float32(20)
	report(t, s, ctx, "early-end", "", "ended", tr.TrackID, &early, start.Add(20*time.Second))
	runLastfmWorker(t, s, start.Add(20*time.Second))
	if f.scrobbles != 0 {
		t.Fatal("early ended scrobbled")
	}
	threshold := float32(150)
	var wg sync.WaitGroup
	for _, id := range []string{"progress-a", "progress-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			report(t, s, ctx, id, "", "progress", tr.TrackID, &threshold, start.Add(3*time.Minute))
		}()
	}
	wg.Wait()
	runLastfmWorker(t, s, start.Add(3*time.Minute))
	if f.scrobbles != 1 {
		t.Fatalf("scrobbles=%d", f.scrobbles)
	}
	report(t, s, ctx, "ended", "", "ended", tr.TrackID, &threshold, start.Add(4*time.Minute))
	runLastfmWorker(t, s, start.Add(4*time.Minute))
	if f.scrobbles != 1 {
		t.Fatal("ended duplicated scrobble")
	}
	if f.nowPlaying != 1 {
		t.Fatalf("now playing=%d", f.nowPlaying)
	}

	// Temporary failures retry; ignored outcomes are terminal.
	f.scrobbleErrors = []error{&lastfm.ProviderError{Kind: lastfm.ErrorTemporary, Code: 11}, nil}
	f.scrobbleResults = []lastfm.ScrobbleResult{{}, {Outcome: lastfm.ScrobbleIgnored, IgnoredCode: "2"}}
	start2 := start.Add(time.Hour)
	report(t, s, ctx, "s2", "session2", "started", tr.TrackID, &zero, start2)
	report(t, s, ctx, "p2", "session2", "progress", tr.TrackID, &threshold, start2.Add(3*time.Minute))
	runLastfmWorker(t, s, start2.Add(3*time.Minute))
	// No further playback event arrives; the scheduled scan retries durably.
	runLastfmWorker(t, s, start2.Add(5*time.Minute))
	if f.scrobbles != 3 {
		t.Fatalf("temporary/ignored calls=%d", f.scrobbles)
	}
	// Permanent outcomes are terminal; invalid sessions also disconnect.
	f.scrobbleErrors = []error{&lastfm.ProviderError{Kind: lastfm.ErrorPermanent, Code: 6}}
	start3 := start2.Add(time.Hour)
	report(t, s, ctx, "s3", "session3", "started", tr.TrackID, &zero, start3)
	report(t, s, ctx, "p3", "session3", "progress", tr.TrackID, &threshold, start3.Add(3*time.Minute))
	runLastfmWorker(t, s, start3.Add(3*time.Minute))
	report(t, s, ctx, "e3", "session3", "ended", tr.TrackID, &threshold, start3.Add(4*time.Minute))
	runLastfmWorker(t, s, start3.Add(4*time.Minute))
	if f.scrobbles != 4 {
		t.Fatalf("permanent retried: %d", f.scrobbles)
	}
	f.scrobbleErrors = []error{&lastfm.ProviderError{Kind: lastfm.ErrorInvalidSession, Code: 9}}
	start4 := start3.Add(time.Hour)
	report(t, s, ctx, "s4", "session4", "started", tr.TrackID, &zero, start4)
	report(t, s, ctx, "p4", "session4", "progress", tr.TrackID, &threshold, start4.Add(3*time.Minute))
	runLastfmWorker(t, s, start4.Add(3*time.Minute))
	if _, connected, _ := st.GetLastfmConnection(context.Background(), authID(ctx)); connected {
		t.Fatal("invalid scrobble session remained connected")
	}
}

func TestPlaybackRequestQueuesPromptlyAndEnforcesBatchLimit(t *testing.T) {
	s, st, ctx, _, f := wireFake(t)
	connectFake(t, st, ctx)
	tr := firstTrack(t, st)
	zero := float32(0)
	report(t, s, ctx, "queued", "queued-session", "started", tr.TrackID, &zero, time.Now().UTC())
	if f.nowPlaying != 0 || f.scrobbles != 0 {
		t.Fatal("request path dispatched provider I/O")
	}
	events := make([]api.PlaybackEvent, 501)
	for i := range events {
		events[i] = api.PlaybackEvent{EventId: fmt.Sprintf("event-%d", i), Type: "started", TrackId: tr.TrackID, At: time.Now().UTC()}
	}
	if _, err := s.ReportPlaybackEvents(ctx, api.ReportPlaybackEventsRequestObject{Body: &api.ReportPlaybackEventsJSONRequestBody{Events: events}}); err == nil {
		t.Fatal("oversized batch accepted")
	}
}

func TestLastfmWorkerShutdown(t *testing.T) {
	s, _, _, _, _ := dataSrv(t)
	s.Close()
	select {
	case <-s.lastfmWorker.done:
	default:
		t.Fatal("worker did not stop")
	}
}

func TestLastfmWorkerCloseWaitsForInFlightWork(t *testing.T) {
	s, st, _, ctx, _ := dataSrv(t)
	f := &fakeLastfm{nowPlayingEntered: make(chan struct{}), nowPlayingRelease: make(chan struct{})}
	s.lastfmFactory = func(string, string) lastfmProvider { return f }
	_ = st.SetSetting(context.Background(), settingLastfmAPIKey, "key")
	_ = st.SetSetting(context.Background(), settingLastfmSharedSecret, "secret")
	connectFake(t, st, ctx)
	tr := firstTrack(t, st)
	zero := float32(0)
	report(t, s, ctx, "shutdown-event", "shutdown-session", "started", tr.TrackID, &zero, time.Now().UTC())
	<-f.nowPlayingEntered
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned before worker finished")
	default:
	}
	close(f.nowPlayingRelease)
	<-closed
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLastfmWorkerBoundsEachScan(t *testing.T) {
	s, st, ctx, _, f := wireFake(t)
	connectFake(t, st, ctx)
	tr := firstTrack(t, st)
	at := time.Now().UTC()
	zero, threshold := float32(0), float32(150)
	f.scrobbleResults = make([]lastfm.ScrobbleResult, 25)
	for i := range f.scrobbleResults {
		f.scrobbleResults[i].Outcome = lastfm.ScrobbleAccepted
		session := fmt.Sprintf("bounded-%d", i)
		report(t, s, ctx, "start-"+session, session, "started", tr.TrackID, &zero, at)
		report(t, s, ctx, "progress-"+session, session, "progress", tr.TrackID, &threshold, at.Add(3*time.Minute))
	}
	runLastfmWorker(t, s, at.Add(4*time.Minute))
	if f.scrobbles != lastfmWorkerBatch {
		t.Fatalf("first scan dispatched %d", f.scrobbles)
	}
	runLastfmWorker(t, s, at.Add(5*time.Minute))
	if f.scrobbles != 25 {
		t.Fatalf("second scan total=%d", f.scrobbles)
	}
}

func TestLastfmInvalidSessionDisconnectsAndLovesStayLocal(t *testing.T) {
	s, st, ctx, _, f := wireFake(t)
	connectFake(t, st, ctx)
	tr := firstTrack(t, st)
	f.exchangeErr = &lastfm.ProviderError{Kind: lastfm.ErrorInvalidSession, Code: 9}
	_, err := s.SetLove(ctx, api.SetLoveRequestObject{Ref: tr.TrackID, Body: &api.SetLoveJSONRequestBody{State: "loved"}})
	if err != nil {
		t.Fatal(err)
	}
	states, _ := st.GetLoveStates(context.Background(), authID(ctx), []string{tr.TrackID})
	if states[tr.TrackID] != "loved" {
		t.Fatal("local love rolled back")
	}
	if _, connected, _ := st.GetLastfmConnection(context.Background(), authID(ctx)); connected {
		t.Fatal("invalid session remained connected")
	}
}

func TestLastfmLoveUnloveAndErasureEndpoints(t *testing.T) {
	s, st, ctx, _, f := wireFake(t)
	connectFake(t, st, ctx)
	tr := firstTrack(t, st)
	_, _ = s.SetLove(ctx, api.SetLoveRequestObject{Ref: tr.TrackID, Body: &api.SetLoveJSONRequestBody{State: "loved"}})
	_, _ = s.SetLove(ctx, api.SetLoveRequestObject{Ref: tr.TrackID, Body: &api.SetLoveJSONRequestBody{State: "neutral"}})
	if f.loves != 1 || f.unloves != 1 {
		t.Fatalf("love relay: %d/%d", f.loves, f.unloves)
	}
	id := authID(ctx)
	dry, _ := s.AdminAnonymizeProfileData(context.Background(), api.AdminAnonymizeProfileDataRequestObject{ProfileId: id, Body: &api.AdminAnonymizeProfileDataJSONRequestBody{DryRun: true}})
	if len(dry.(api.AdminAnonymizeProfileData200JSONResponse).Categories) != 2 {
		t.Fatalf("dry run: %#v", dry)
	}
	live, _ := s.AdminAnonymizeProfileData(context.Background(), api.AdminAnonymizeProfileDataRequestObject{ProfileId: id, Body: &api.AdminAnonymizeProfileDataJSONRequestBody{DryRun: false}})
	if len(live.(api.AdminAnonymizeProfileData200JSONResponse).Categories) != 2 {
		t.Fatalf("live: %#v", live)
	}
	repeat, _ := s.AdminAnonymizeProfileData(context.Background(), api.AdminAnonymizeProfileDataRequestObject{ProfileId: id, Body: &api.AdminAnonymizeProfileDataJSONRequestBody{DryRun: false}})
	if len(repeat.(api.AdminAnonymizeProfileData200JSONResponse).Categories) != 0 {
		t.Fatalf("repeat: %#v", repeat)
	}
	connectFake(t, st, ctx)
	_, _ = s.AdminDeleteLastfm(context.Background(), api.AdminDeleteLastfmRequestObject{})
	if n, _ := st.CountLastfmProfiles(context.Background()); n != 0 {
		t.Fatal("credential removal retained connections")
	}
}

func authID(ctx context.Context) string { id, _ := auth.IdentityFrom(ctx); return id.ProfileID }

func TestLastfmDiscoveryErrorsFilteringAndCapabilities(t *testing.T) {
	s, st, ctx, _, f := wireFake(t)
	connectFake(t, st, ctx)
	f.top = []lastfm.Artist{{Name: "Seed"}}
	f.similar = []lastfm.Artist{{Name: "Candidate", Match: .9}}
	disc, err := s.GetMyDiscover(ctx, api.GetMyDiscoverRequestObject{})
	if err != nil || len(disc.(api.GetMyDiscover200JSONResponse)) != 1 {
		t.Fatalf("discover: %T %v", disc, err)
	}
	item := disc.(api.GetMyDiscover200JSONResponse)[0]
	_, _ = st.SetLove(context.Background(), authID(ctx), *item.Ref, "not_for_me")
	disc, _ = s.GetMyDiscover(ctx, api.GetMyDiscoverRequestObject{})
	if len(disc.(api.GetMyDiscover200JSONResponse)) != 0 {
		t.Fatal("not_for_me surfaced")
	}
	f.discoveryErr = errors.New("provider down")
	failed, _ := s.GetMyDiscover(ctx, api.GetMyDiscoverRequestObject{})
	if _, ok := failed.(api.GetMyDiscover502ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("failure: %T", failed)
	}
	caps, _ := s.GetCapabilities(ctx, api.GetCapabilitiesRequestObject{})
	if d := caps.(api.GetCapabilities200JSONResponse).Discovery; d == nil || !*d {
		t.Fatal("instance discovery capability false")
	}
}

func TestExternalArtistNotFoundVersusOutage(t *testing.T) {
	s, _, ctx, _, f := wireFake(t)
	f.discoveryErr = &lastfm.ProviderError{Kind: lastfm.ErrorNotFound, Code: 6}
	missing, _ := s.GetExternalArtist(ctx, api.GetExternalArtistRequestObject{Name: "Synthetic Missing"})
	if _, ok := missing.(api.GetExternalArtist404ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("not found: %T", missing)
	}
	f.discoveryErr = &lastfm.ProviderError{Kind: lastfm.ErrorTemporary, Code: 11}
	outage, _ := s.GetExternalArtist(ctx, api.GetExternalArtistRequestObject{Name: "Synthetic Missing"})
	if _, ok := outage.(api.GetExternalArtist502ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("outage: %T", outage)
	}
}
