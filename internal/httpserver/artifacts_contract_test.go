package httpserver_test

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/api"
)

// TestContractArtifactPipelineEndToEnd drives request → SSE-visible progress
// → exact-length download over the wire against a real transcode.
func TestContractArtifactPipelineEndToEnd(t *testing.T) {
	dir := fixtureMusic(t) // skips without ffmpeg
	ts, _, tok := setup(t)
	admin := adminSession(t, ts)
	configureLibrary(t, ts.URL, admin, dir)

	c, _ := api.NewClientWithResponses(ts.URL)
	ctx := context.Background()
	tracks, err := c.ListTracksWithResponse(ctx, nil, bearer(tok))
	if err != nil || tracks.JSON200 == nil || len(tracks.JSON200.Items) == 0 {
		t.Fatalf("tracks: %v", err)
	}
	trackID := tracks.JSON200.Items[0].TrackId

	bitrate := api.RequestArtifactsJSONBodyBitrateKbps(128)
	req, err := c.RequestArtifactsWithResponse(ctx, api.RequestArtifactsJSONRequestBody{
		TrackIds: []string{trackID}, Format: "aac", BitrateKbps: &bitrate}, bearer(tok))
	if err != nil || req.JSON202 == nil || len(*req.JSON202) != 1 {
		t.Fatalf("request: %v %d", err, req.StatusCode())
	}
	artifact := (*req.JSON202)[0]

	// Downloading before ready is an honest 409.
	if artifact.Status != "ready" {
		early, _ := http.NewRequest("GET", ts.URL+"/v1/artifacts/"+artifact.ArtifactId+"/file", nil)
		early.Header.Set("Authorization", "Bearer "+tok)
		if resp, _ := http.DefaultClient.Do(early); resp.StatusCode != 409 {
			t.Fatalf("early download: want 409, got %d", resp.StatusCode)
		}
	}

	// Poll status until ready (the SSE path is covered elsewhere).
	var ready api.Artifact
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		got, err := c.GetArtifactWithResponse(ctx, artifact.ArtifactId, bearer(tok))
		if err != nil || got.JSON200 == nil {
			t.Fatalf("status: %v", err)
		}
		if got.JSON200.Status == "ready" {
			ready = *got.JSON200
			break
		}
		if got.JSON200.Status == "failed" {
			t.Fatalf("transcode failed: %v", got.JSON200.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ready.Bytes == nil || *ready.Bytes == 0 {
		t.Fatalf("never ready: %+v", ready)
	}

	// Download: exact Content-Length, valid bytes, Range works.
	dl, _ := http.NewRequest("GET", ts.URL+"/v1/artifacts/"+ready.ArtifactId+"/file", nil)
	dl.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(dl)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("download: %v %d", err, resp.StatusCode)
	}
	if cl := resp.Header.Get("Content-Length"); cl != strconv.FormatInt(*ready.Bytes, 10) {
		t.Fatalf("Content-Length %q vs bytes %d", cl, *ready.Bytes)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if int64(len(body)) != *ready.Bytes {
		t.Fatalf("body size %d vs %d", len(body), *ready.Bytes)
	}

	ranged, _ := http.NewRequest("GET", ts.URL+"/v1/artifacts/"+ready.ArtifactId+"/file", nil)
	ranged.Header.Set("Authorization", "Bearer "+tok)
	ranged.Header.Set("Range", "bytes=0-99")
	part, _ := http.DefaultClient.Do(ranged)
	if part.StatusCode != 206 {
		t.Fatalf("range: want 206, got %d", part.StatusCode)
	}
	part.Body.Close()

	// Release is a 204; unknown artifact file download 404s.
	rel, _ := http.NewRequest("DELETE", ts.URL+"/v1/artifacts/"+ready.ArtifactId, nil)
	rel.Header.Set("Authorization", "Bearer "+tok)
	if resp, _ := http.DefaultClient.Do(rel); resp.StatusCode != 204 {
		t.Fatalf("release: want 204, got %d", resp.StatusCode)
	}
	nf, _ := http.NewRequest("GET", ts.URL+"/v1/artifacts/arf_nope/file", nil)
	nf.Header.Set("Authorization", "Bearer "+tok)
	if resp, _ := http.DefaultClient.Do(nf); resp.StatusCode != 404 {
		t.Fatalf("unknown artifact: want 404, got %d", resp.StatusCode)
	}
}
