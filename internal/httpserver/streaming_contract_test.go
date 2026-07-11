package httpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/api"
)

// fixtureMusic writes a one-track library (flac with embedded art) and
// returns its directory. Skips without ffmpeg.
func fixtureMusic(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH; fixture tests skipped")
	}
	root := t.TempDir()
	cover := filepath.Join(root, "cover.png")
	mustRun(t, "ffmpeg", "-y", "-f", "lavfi", "-i", "color=c=blue:size=128x128:duration=1", "-frames:v", "1", cover)
	dir := filepath.Join(root, "Fixture Artist", "Fixture Album")
	os.MkdirAll(dir, 0o755)
	mustRun(t, "ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-i", cover,
		"-map", "0:a", "-map", "1:v", "-disposition:v", "attached_pic",
		"-metadata", "title=Fixture Song", "-metadata", "artist=Fixture Artist",
		"-metadata", "album=Fixture Album", "-metadata", "genre=Test", "-metadata", "track=1",
		filepath.Join(dir, "01 - Fixture Song.flac"))
	os.Remove(cover)
	return root
}

func mustRun(t *testing.T, name string, args ...string) {
	t.Helper()
	if b, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, b)
	}
}

// configureLibrary points the server at dir and waits for the scan.
func configureLibrary(t *testing.T, tsURL string, admin *http.Client, dir string) {
	t.Helper()
	body := strings.NewReader(fmt.Sprintf(`{"path":%q}`, dir))
	req, _ := http.NewRequest("PUT", tsURL+"/admin/api/source/filesystem", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := admin.Do(req)
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("set source: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := admin.Get(tsURL + "/admin/api/source/filesystem")
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Scanning      bool    `json:"scanning"`
			LastScanAt    *string `json:"lastScanAt"`
			LastScanError *string `json:"lastScanError"`
		}
		json.NewDecoder(resp.Body).Decode(&cfg)
		resp.Body.Close()
		if !cfg.Scanning && cfg.LastScanAt != nil {
			if cfg.LastScanError != nil && *cfg.LastScanError != "" {
				t.Fatalf("scan error: %s", *cfg.LastScanError)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("scan never completed")
}

func TestContractLibraryStreamingEndToEnd(t *testing.T) {
	dir := fixtureMusic(t)
	ts, _, tok := setup(t)
	admin := adminSession(t, ts)
	configureLibrary(t, ts.URL, admin, dir)

	c, _ := api.NewClientWithResponses(ts.URL)
	ctx := context.Background()

	// Library summary reflects the scan.
	lib, err := c.GetLibraryWithResponse(ctx, bearer(tok))
	if err != nil || lib.JSON200 == nil || *lib.JSON200.Counts.Tracks != 1 {
		t.Fatalf("library: %v %+v", err, lib.JSON200)
	}

	// Browse down to the track.
	artists, err := c.ListArtistsWithResponse(ctx, nil, bearer(tok))
	if err != nil || artists.JSON200 == nil || len(artists.JSON200.Items) != 1 {
		t.Fatalf("artists: %v %+v", err, artists.JSON200)
	}
	artist := artists.JSON200.Items[0]
	if artist.Name != "Fixture Artist" || artist.ArtId == nil {
		t.Fatalf("artist (art must have propagated): %+v", artist)
	}
	albums, err := c.ListArtistAlbumsWithResponse(ctx, artist.ArtistId, bearer(tok))
	if err != nil || albums.JSON200 == nil || len(*albums.JSON200) != 1 {
		t.Fatalf("albums: %v", err)
	}
	tracks, err := c.ListAlbumTracksWithResponse(ctx, (*albums.JSON200)[0].AlbumId, bearer(tok))
	if err != nil || tracks.JSON200 == nil || len(*tracks.JSON200) != 1 {
		t.Fatalf("tracks: %v", err)
	}
	track := (*tracks.JSON200)[0]
	if track.Title != "Fixture Song" || track.Media.AudioCodec != "flac" || track.DurationMs < 1500 {
		t.Fatalf("track: %+v", track)
	}

	// Status now reports a connected filesystem source.
	status, _ := c.GetStatusWithResponse(ctx, bearer(tok))
	if status.JSON200 == nil || status.JSON200.Source.Kind != "filesystem" || !status.JSON200.Source.Connected {
		t.Fatalf("status: %+v", status.JSON200)
	}

	// Stream: full then ranged.
	streamReq, _ := http.NewRequest("GET", ts.URL+"/v1/stream/"+track.TrackId, nil)
	streamReq.Header.Set("Authorization", "Bearer "+tok)
	full, err := http.DefaultClient.Do(streamReq)
	if err != nil || full.StatusCode != 200 {
		t.Fatalf("stream: %v %d", err, full.StatusCode)
	}
	head := make([]byte, 4)
	io.ReadFull(full.Body, head)
	full.Body.Close()
	if string(head) != "fLaC" {
		t.Fatalf("stream bytes: %q", head)
	}
	ranged, _ := http.NewRequest("GET", ts.URL+"/v1/stream/"+track.TrackId, nil)
	ranged.Header.Set("Authorization", "Bearer "+tok)
	ranged.Header.Set("Range", "bytes=0-3")
	part, err := http.DefaultClient.Do(ranged)
	if err != nil || part.StatusCode != 206 || part.Header.Get("Content-Range") == "" {
		t.Fatalf("range: %v %d %q", err, part.StatusCode, part.Header.Get("Content-Range"))
	}
	part.Body.Close()

	// Unknown track 404s; missing bearer 401s.
	nf, _ := http.NewRequest("GET", ts.URL+"/v1/stream/trk_nope", nil)
	nf.Header.Set("Authorization", "Bearer "+tok)
	if resp, _ := http.DefaultClient.Do(nf); resp.StatusCode != 404 {
		t.Fatalf("unknown track: want 404, got %d", resp.StatusCode)
	}
	if resp, _ := http.Get(ts.URL + "/v1/stream/" + track.TrackId); resp.StatusCode != 401 {
		t.Fatalf("bare stream: want 401, got %d", resp.StatusCode)
	}

	// Art: original and resized (resized is jpeg per the contract).
	artReq, _ := http.NewRequest("GET", ts.URL+"/v1/art/"+*track.ArtId+"?w=32&h=32", nil)
	artReq.Header.Set("Authorization", "Bearer "+tok)
	artResp, err := http.DefaultClient.Do(artReq)
	if err != nil || artResp.StatusCode != 200 {
		t.Fatalf("art: %v %d", err, artResp.StatusCode)
	}
	img, format, err := image.Decode(artResp.Body)
	artResp.Body.Close()
	if err != nil || format != "jpeg" {
		t.Fatalf("resized art must decode as jpeg: %v %q", err, format)
	}
	if img.Bounds().Dx() > 32 || img.Bounds().Dy() > 32 {
		t.Fatalf("resize bounds: %v", img.Bounds())
	}

	// Stream grant: mint with the profile token, fetch with no auth at all.
	grant, err := c.CreateStreamGrantWithResponse(ctx,
		api.CreateStreamGrantJSONRequestBody{TrackId: track.TrackId}, bearer(tok))
	if err != nil || grant.JSON201 == nil {
		t.Fatalf("grant: %v %d", err, grant.StatusCode())
	}
	grantResp, err := http.Get(grant.JSON201.Url)
	if err != nil || grantResp.StatusCode != 200 {
		t.Fatalf("grant fetch: %v %d", err, grantResp.StatusCode)
	}
	grantResp.Body.Close()
	if !strings.Contains(grant.JSON201.Url, "grant=") {
		t.Fatalf("grant url: %q", grant.JSON201.Url)
	}

	// Tampered grants die.
	bad := strings.Replace(grant.JSON201.Url, "grant=", "grant=00", 1)
	if resp, _ := http.Get(bad); resp.StatusCode != 401 {
		t.Fatalf("tampered grant: want 401, got %d", resp.StatusCode)
	}
}
