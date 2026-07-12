package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/lastfm"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
)

const lastfmWorkerBatch = 20

// lastfmWorker coalesces request notifications into a bounded, single-worker
// scan. SQLite claims provide cross-worker exclusion if that changes later.
type lastfmWorker struct {
	server *Server
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}
	once   sync.Once
}

func newLastfmWorker(s *Server) *lastfmWorker {
	ctx, cancel := context.WithCancel(context.Background())
	return &lastfmWorker{server: s, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{})}
}
func (w *lastfmWorker) start() {
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-w.ctx.Done():
				return
			case <-w.wake:
				w.logError(w.runOnce(time.Now().UTC()))
			case now := <-ticker.C:
				w.logError(w.runOnce(now.UTC()))
			}
		}
	}()
}
func (w *lastfmWorker) notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *lastfmWorker) stop() { w.once.Do(func() { w.cancel(); <-w.done }) }

func (w *lastfmWorker) runOnce(now time.Time) error {
	_, _ = w.server.st.CleanupLastfmPlaySessions(w.ctx, now)
	client, configured, err := w.server.lastfmClient(w.ctx)
	if err != nil || !configured {
		return err
	}
	nowPlaying, err := w.server.st.ClaimPendingNowPlaying(w.ctx, lastfmWorkerBatch)
	if err != nil {
		return err
	}
	for _, p := range nowPlaying {
		conn, ok, e := w.server.st.GetLastfmConnection(w.ctx, p.ProfileID)
		if e != nil || !ok {
			continue
		}
		e = client.NowPlaying(w.ctx, conn.SessionKey, lastfm.Track{Artist: p.Artist, Title: p.Title, Album: p.Album, Duration: time.Duration(p.DurationMs) * time.Millisecond, StartedAt: p.StartedAt})
		_ = w.server.st.FinishNowPlaying(w.ctx, p.SessionID, e == nil)
		if providerKind(e) == lastfm.ErrorInvalidSession {
			_, _ = w.server.st.DeleteLastfmData(w.ctx, p.ProfileID)
		}
	}
	claimRaw := make([]byte, 16)
	if _, err := rand.Read(claimRaw); err != nil {
		return err
	}
	claimID := base64.RawURLEncoding.EncodeToString(claimRaw)
	pending, err := w.server.st.ClaimPendingLastfmPlays(w.ctx, now, claimID, lastfmWorkerBatch)
	if err != nil {
		return err
	}
	for _, p := range pending {
		conn, ok, e := w.server.st.GetLastfmConnection(w.ctx, p.ProfileID)
		if e != nil || !ok {
			continue
		}
		result, e := client.Scrobble(w.ctx, conn.SessionKey, lastfm.Track{Artist: p.Artist, Title: p.Title, Album: p.Album, Duration: time.Duration(p.DurationMs) * time.Millisecond, StartedAt: p.StartedAt})
		state, reason := "terminal", "accepted"
		if e != nil {
			switch providerKind(e) {
			case lastfm.ErrorTemporary:
				state, reason = "pending", "temporary"
			case lastfm.ErrorInvalidSession:
				reason = "invalid_session"
				_, _ = w.server.st.DeleteLastfmData(w.ctx, p.ProfileID)
			default:
				reason = "permanent"
			}
		} else if result.Outcome == lastfm.ScrobbleIgnored {
			reason = "ignored:" + result.IgnoredCode
		}
		_ = w.server.st.FinishLastfmPlay(w.ctx, p.SessionID, claimID, state, reason)
	}
	return nil
}

func (w *lastfmWorker) logError(err error) {
	if err != nil {
		logging.From(context.Background()).Warn("last.fm worker", "err", err)
	}
}
