package httpserver

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/artifacts"
	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/store"
	"golang.org/x/image/draw"
)

// The streaming endpoints live outside the strict handler: they serve raw
// bytes with Range support (http.ServeContent) and mint grant URLs from
// request context — neither fits generated response objects.

var containerMIME = map[string]string{
	"flac": "audio/flac",
	"mp3":  "audio/mpeg",
	"m4a":  "audio/mp4",
	"ogg":  "audio/ogg",
	"opus": "audio/ogg",
}

const streamGrantTTL = 5 * time.Minute

// grantSecret returns (creating on first use) the instance's HMAC key for
// stream grants.
func grantSecret(r *http.Request, st *store.Store) ([]byte, error) {
	v, ok, err := st.GetSetting(r.Context(), "stream_grant_secret")
	if err != nil {
		return nil, err
	}
	if !ok || v == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		v = hex.EncodeToString(b)
		if err := st.SetSetting(r.Context(), "stream_grant_secret", v); err != nil {
			return nil, err
		}
	}
	return hex.DecodeString(v)
}

func grantMAC(secret []byte, trackID string, exp int64) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%s|%d", trackID, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

// validStreamGrant reports whether the request carries a live, untampered
// grant for the track in its path. Used by Auth as an alternative credential
// for stream GETs only.
func validStreamGrant(r *http.Request, st *store.Store) bool {
	trackID := strings.TrimPrefix(r.URL.Path, "/v1/stream/")
	q := r.URL.Query()
	macHex, expStr := q.Get("grant"), q.Get("exp")
	if trackID == "" || macHex == "" || expStr == "" {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	secret, err := grantSecret(r, st)
	if err != nil {
		return false
	}
	want := grantMAC(secret, trackID, exp)
	return hmac.Equal([]byte(want), []byte(macHex))
}

// handleCreateStreamGrant mints the short-lived signed URL. Bearer auth has
// already run; the absolute URL prefers the canonical URL setting and falls
// back to the request host.
func handleCreateStreamGrant(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			TrackId string `json:"trackId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TrackId == "" {
			WriteProblem(w, http.StatusBadRequest, "Bad Request", "bad_request")
			return
		}
		if _, found, err := st.GetTrack(r.Context(), body.TrackId); err != nil {
			logging.From(r.Context()).Error("grant track lookup", "err", err)
			WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
			return
		} else if !found {
			WriteProblem(w, http.StatusNotFound, "Not Found", "not_found")
			return
		}
		secret, err := grantSecret(r, st)
		if err != nil {
			logging.From(r.Context()).Error("grant secret", "err", err)
			WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
			return
		}
		base, _, _ := st.GetSetting(r.Context(), "canonical_url")
		if base == "" {
			scheme := "http"
			if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
				scheme = "https"
			}
			base = scheme + "://" + r.Host
		}
		expiresAt := time.Now().Add(streamGrantTTL)
		exp := expiresAt.Unix()
		grantURL := fmt.Sprintf("%s/v1/stream/%s?grant=%s&exp=%d",
			strings.TrimSuffix(base, "/"), url.PathEscape(body.TrackId), grantMAC(secret, body.TrackId, exp), exp)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"url": grantURL, "expiresAt": expiresAt.UTC().Format(time.RFC3339),
		})
	}
}

// handleStreamTrack direct-plays the original file with Range support.
func handleStreamTrack(st *store.Store, mgr *library.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		trackID := strings.TrimPrefix(r.URL.Path, "/v1/stream/")
		tr, found, err := st.GetTrack(r.Context(), trackID)
		if err != nil {
			logging.From(r.Context()).Error("stream track lookup", "err", err)
			WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
			return
		}
		if !found {
			WriteProblem(w, http.StatusNotFound, "Not Found", "not_found")
			return
		}
		rc, found, err := mgr.Open(r.Context(), trackID)
		if errors.Is(err, library.ErrNotConfigured) {
			WriteProblem(w, http.StatusServiceUnavailable, "Source Unavailable", "source_unavailable")
			return
		}
		if err != nil || !found {
			logging.From(r.Context()).Error("stream open", "err", err)
			WriteProblem(w, http.StatusServiceUnavailable, "Source Unavailable", "source_unavailable")
			return
		}
		defer rc.Close()
		if mime, ok := containerMIME[tr.Container]; ok {
			w.Header().Set("Content-Type", mime)
		}
		http.ServeContent(w, r, "", time.Time{}, rc)
	}
}

// handleDownloadArtifact serves a ready artifact with its exact size (iOS
// background URLSession needs Content-Length). Unready artifacts are 409.
func handleDownloadArtifact(st *store.Store, artMgr *artifacts.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		artifactID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/artifacts/"), "/file")
		a, found, err := st.GetArtifact(r.Context(), artifactID)
		if err != nil {
			logging.From(r.Context()).Error("artifact lookup", "err", err)
			WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
			return
		}
		if !found {
			WriteProblem(w, http.StatusNotFound, "Not Found", "not_found")
			return
		}
		if a.Status != "ready" {
			code := "artifact_not_ready"
			if a.Status == "failed" {
				code = "artifact_failed"
			}
			WriteProblem(w, http.StatusConflict, "Conflict", code)
			return
		}
		rc, _, err := artMgr.Open(r.Context(), artifactID)
		if err != nil {
			logging.From(r.Context()).Error("artifact open", "err", err)
			WriteProblem(w, http.StatusServiceUnavailable, "Source Unavailable", "source_unavailable")
			return
		}
		defer rc.Close()
		if a.Format == "original" {
			tr, found, _ := st.GetTrack(r.Context(), a.TrackID)
			if found {
				if mime, ok := containerMIME[tr.Container]; ok {
					w.Header().Set("Content-Type", mime)
				}
			}
		} else {
			w.Header().Set("Content-Type", "audio/mp4")
		}
		http.ServeContent(w, r, "", time.Time{}, rc)
	}
}

// handleGetArt serves artwork, resizing (and caching) when w/h are given.
// Resized output is always JPEG per the contract.
func handleGetArt(st *store.Store, dataDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		artID := strings.TrimPrefix(r.URL.Path, "/v1/art/")
		path, mime, found, err := st.GetArt(r.Context(), artID)
		if err != nil {
			logging.From(r.Context()).Error("art lookup", "err", err)
			WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
			return
		}
		if !found {
			WriteProblem(w, http.StatusNotFound, "Not Found", "not_found")
			return
		}
		wQ, _ := strconv.Atoi(r.URL.Query().Get("w"))
		hQ, _ := strconv.Atoi(r.URL.Query().Get("h"))
		if wQ > 2048 || hQ > 2048 || wQ < 0 || hQ < 0 {
			WriteProblem(w, http.StatusBadRequest, "Bad Request", "bad_dimensions")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		if wQ == 0 && hQ == 0 {
			w.Header().Set("Content-Type", mime)
			http.ServeFile(w, r, path)
			return
		}

		cachePath := filepath.Join(dataDir, "art-cache", fmt.Sprintf("%s_%dx%d.jpg", artID, wQ, hQ))
		if _, err := os.Stat(cachePath); err != nil {
			if err := resizeToCache(path, cachePath, wQ, hQ); err != nil {
				logging.From(r.Context()).Error("art resize", "err", err)
				WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
				return
			}
		}
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeFile(w, r, cachePath)
	}
}

func resizeToCache(srcPath, dstPath string, maxW, maxH int) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return err
	}
	b := img.Bounds()
	scale := 1.0
	if maxW > 0 {
		scale = min(scale, float64(maxW)/float64(b.Dx()))
	}
	if maxH > 0 {
		scale = min(scale, float64(maxH)/float64(b.Dy()))
	}
	dw, dh := max(1, int(float64(b.Dx())*scale)), max(1, int(float64(b.Dy())*scale))
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	tmp := dstPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(out, dst, &jpeg.Options{Quality: 85}); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dstPath)
}
