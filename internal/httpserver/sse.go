package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/auth"
	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
)

const sseHeartbeat = 25 * time.Second

// handleStreamEvents is the SSE endpoint. It lives on the overlay because a
// strict response object cannot hold an unbounded stream. Auth has already
// resolved a profile identity (device tokens are 403'd by the middleware).
func handleStreamEvents(bus *events.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.IdentityFrom(r.Context())
		if !ok || id.ProfileID == "" {
			WriteProblem(w, http.StatusForbidden, "Forbidden", "profile_token_required")
			return
		}
		// Live-only by default: a fresh client bootstraps state through
		// /v1/library + /v1/changes, and replaying the whole event log at
		// connect turned every app launch into a sync storm that grew with
		// the instance's age. Explicit Last-Event-ID still resumes losslessly.
		since := bus.LatestSeq(r.Context())
		if v := r.Header.Get("Last-Event-ID"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				since = n
			}
		}

		rc := http.NewResponseController(w)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		if err := rc.Flush(); err != nil {
			logging.From(r.Context()).Warn("sse: writer does not flush", "err", err)
			return
		}
		// An SSE frame must never sit in a write buffer past the heartbeat.
		_ = rc.SetWriteDeadline(time.Time{})

		sub, cancel := bus.Subscribe(id.ProfileID, since)
		defer cancel()
		heartbeat := time.NewTicker(sseHeartbeat)
		defer heartbeat.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-heartbeat.C:
				if _, err := fmt.Fprint(w, ": hb\n\n"); err != nil {
					return
				}
				if err := rc.Flush(); err != nil {
					return
				}
			case ev, open := <-sub:
				if !open {
					return
				}
				envelope, err := json.Marshal(map[string]any{
					"type": ev.Type,
					"at":   ev.At.Format(time.RFC3339),
					"data": json.RawMessage(ev.Data),
				})
				if err != nil {
					logging.From(r.Context()).Error("sse envelope", "err", err)
					continue
				}
				if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, envelope); err != nil {
					return
				}
				if err := rc.Flush(); err != nil {
					return
				}
			}
		}
	}
}
