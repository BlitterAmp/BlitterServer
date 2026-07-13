package httpserver_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

type sseFrame struct {
	ID   int64
	Type string
	Data json.RawMessage
}

// readFrames consumes SSE frames until n arrive or the deadline passes.
func readFrames(t *testing.T, body *bufio.Reader, n int, deadline time.Duration) []sseFrame {
	t.Helper()
	var out []sseFrame
	var cur sseFrame
	done := make(chan struct{})
	go func() {
		defer close(done)
		for len(out) < n {
			line, err := body.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, "id: "):
				id, _ := json.Number(strings.TrimPrefix(line, "id: ")).Int64()
				cur.ID = id
			case strings.HasPrefix(line, "data: "):
				var envelope struct {
					Type string          `json:"type"`
					Data json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &envelope); err == nil {
					cur.Type = envelope.Type
					cur.Data = envelope.Data
				}
			case line == "" && cur.Type != "":
				out = append(out, cur)
				cur = sseFrame{}
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(deadline):
	}
	return out
}

func TestContractSSEStream(t *testing.T) {
	ts, st, tok := setup(t)
	c, _ := api.NewClientWithResponses(ts.URL)
	ctx := context.Background()

	// Seed one track so there is something to love.
	seedOneTrack(t, st)
	tracks, err := c.ListTracksWithResponse(ctx, nil, bearer(tok))
	if err != nil || tracks.JSON200 == nil || len(tracks.JSON200.Items) == 0 {
		t.Fatalf("tracks: %v", err)
	}
	trackID := tracks.JSON200.Items[0].TrackId

	// Device tokens must not stream.
	dev, _ := st.CreateDevice(ctx, "d2", "ios")
	dtok, _ := st.CreateDeviceToken(ctx, dev)
	req, _ := http.NewRequest("GET", ts.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+dtok)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 403 {
		t.Fatalf("device token SSE: want 403, got %d", resp.StatusCode)
	}

	// Open the stream with a profile token.
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	req, _ = http.NewRequestWithContext(streamCtx, "GET", ts.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse open: %v %d %q", err, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)

	// Trigger a profile-scoped event.
	love, err := c.SetLoveWithResponse(ctx, trackID, api.SetLoveJSONRequestBody{State: "loved"}, bearer(tok))
	if err != nil || love.JSON200 == nil {
		t.Fatalf("love: %v %d", err, love.StatusCode())
	}

	frames := readFrames(t, reader, 2, 5*time.Second)
	if len(frames) < 1 {
		t.Fatal("no SSE frames arrived")
	}
	var sawLove bool
	var lastSeq int64
	for _, f := range frames {
		if f.Type == "love.updated" {
			sawLove = true
		}
		if f.ID > lastSeq {
			lastSeq = f.ID
		}
	}
	if !sawLove || lastSeq == 0 {
		t.Fatalf("frames: %+v", frames)
	}
	cancelStream()

	// Reconnect from the beginning: Last-Event-ID replay delivers history.
	req, _ = http.NewRequest("GET", ts.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Last-Event-ID", "0")
	replayCtx, cancelReplay := context.WithCancel(ctx)
	defer cancelReplay()
	req = req.WithContext(replayCtx)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil || resp2.StatusCode != 200 {
		t.Fatalf("sse reconnect: %v", err)
	}
	defer resp2.Body.Close()
	replayed := readFrames(t, bufio.NewReader(resp2.Body), 1, 5*time.Second)
	if len(replayed) == 0 || replayed[0].ID == 0 {
		t.Fatalf("replay: %+v", replayed)
	}
}

// seedOneTrack pushes a single track through the index without touching disk.
func seedOneTrack(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	seq, err := st.NextScanSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTrack(ctx, "filesystem", source.TrackMeta{
		NativeID: "x/one.flac", Title: "One", PrimaryArtist: source.ArtistReference{Name: "Alpha"}, TrackCredits: []source.ArtistCredit{{Name: "Alpha"}}, AlbumCredits: []source.ArtistCredit{{Name: "Alpha"}},
		Album: "AA", Genre: "Rock", Year: 2000, Index: 1, DurationMs: 2000,
		Container: "flac", Codec: "flac", SizeBytes: 10, Version: 1,
	}, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
}

// A connection WITHOUT Last-Event-ID is live-only: persisted history is never
// replayed to it. New clients bootstrap through /v1/library and /v1/changes;
// replaying the whole event log at every app launch produced sync storms
// proportional to the instance's age.
func TestContractSSEWithoutLastEventIDIsLiveOnly(t *testing.T) {
	ts, st, tok := setup(t)
	c, _ := api.NewClientWithResponses(ts.URL)
	ctx := context.Background()
	seedOneTrack(t, st)
	tracks, err := c.ListTracksWithResponse(ctx, nil, bearer(tok))
	if err != nil || tracks.JSON200 == nil || len(tracks.JSON200.Items) == 0 {
		t.Fatalf("tracks: %v", err)
	}
	trackID := tracks.JSON200.Items[0].TrackId

	// History that must NOT be replayed.
	if love, err := c.SetLoveWithResponse(ctx, trackID, api.SetLoveJSONRequestBody{State: "loved"}, bearer(tok)); err != nil || love.JSON200 == nil {
		t.Fatalf("pre-connect love: %v", err)
	}

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	req, _ := http.NewRequestWithContext(streamCtx, "GET", ts.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("sse open: %v", err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)

	// Give any (incorrect) replay a moment to arrive, then publish live.
	time.Sleep(150 * time.Millisecond)
	if love, err := c.SetLoveWithResponse(ctx, trackID, api.SetLoveJSONRequestBody{State: "neutral"}, bearer(tok)); err != nil || love.JSON200 == nil {
		t.Fatalf("live love: %v", err)
	}

	// The live change fans out as love.updated plus the Loved Tracks
	// playlist.changed; both are fine. What must never arrive is the
	// pre-connect history (the "loved" event or its playlist echo).
	frames := readFrames(t, reader, 2, 5*time.Second)
	if len(frames) == 0 {
		t.Fatal("no live frames arrived")
	}
	sawLive := false
	for _, frame := range frames {
		if frame.Type != "love.updated" {
			continue
		}
		var payload struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(frame.Data, &payload)
		if payload.State == "loved" {
			t.Fatalf("received replayed history instead of live-only: %+v", frame)
		}
		if payload.State == "neutral" {
			sawLive = true
		}
	}
	if !sawLive && len(frames) < 2 {
		t.Fatalf("live events missing: %+v", frames)
	}
}
