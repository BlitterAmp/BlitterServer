package server

import (
	"errors"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/api"
)

func TestRequestArtifactsOriginal(t *testing.T) {
	s, st, _, ctx1, _ := dataSrv(t)
	tr := firstTrack(t, st)

	resp, err := s.RequestArtifacts(ctx1, api.RequestArtifactsRequestObject{
		Body: &api.RequestArtifactsJSONRequestBody{TrackIds: []string{tr.TrackID}, Format: "original"}})
	if err != nil {
		t.Fatal(err)
	}
	batch := resp.(api.RequestArtifacts202JSONResponse)
	if len(batch) != 1 || batch[0].Status != "ready" || batch[0].Bytes == nil || *batch[0].Bytes != tr.SizeBytes {
		t.Fatalf("original artifact: %+v", batch)
	}
	if batch[0].AlbumId == nil || *batch[0].AlbumId != tr.AlbumID {
		t.Fatalf("albumId grouping key missing: %+v", batch[0])
	}

	// Status endpoint mirrors it; release 204s; unknown 404s.
	got, err := s.GetArtifact(ctx1, api.GetArtifactRequestObject{ArtifactId: batch[0].ArtifactId})
	if err != nil {
		t.Fatal(err)
	}
	if a := got.(api.GetArtifact200JSONResponse); a.ArtifactId != batch[0].ArtifactId {
		t.Fatalf("get: %+v", a)
	}
	rel, err := s.ReleaseArtifact(ctx1, api.ReleaseArtifactRequestObject{ArtifactId: batch[0].ArtifactId})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := rel.(api.ReleaseArtifact204Response); !is204 {
		t.Fatalf("release: want 204, got %T", rel)
	}
	nf, _ := s.GetArtifact(ctx1, api.GetArtifactRequestObject{ArtifactId: "arf_nope"})
	if _, is404 := nf.(api.GetArtifact404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("unknown: want 404, got %T", nf)
	}
}

func TestRequestArtifactsValidation(t *testing.T) {
	s, st, _, ctx1, _ := dataSrv(t)
	tr := firstTrack(t, st)

	// aac requires a bitrate.
	_, err := s.RequestArtifacts(ctx1, api.RequestArtifactsRequestObject{
		Body: &api.RequestArtifactsJSONRequestBody{TrackIds: []string{tr.TrackID}, Format: "aac"}})
	var se *api.StatusError
	if !errors.As(err, &se) || se.Status != 400 {
		t.Fatalf("aac without bitrate: want 400, got %v", err)
	}

	// Unknown track in the batch is a 400 (batch stays all-or-nothing).
	_, err = s.RequestArtifacts(ctx1, api.RequestArtifactsRequestObject{
		Body: &api.RequestArtifactsJSONRequestBody{TrackIds: []string{"trk_nope"}, Format: "original"}})
	if !errors.As(err, &se) || se.Status != 400 {
		t.Fatalf("unknown track: want 400, got %v", err)
	}

	// Empty batch is a 400.
	_, err = s.RequestArtifacts(ctx1, api.RequestArtifactsRequestObject{
		Body: &api.RequestArtifactsJSONRequestBody{TrackIds: nil, Format: "original"}})
	if !errors.As(err, &se) || se.Status != 400 {
		t.Fatalf("empty batch: want 400, got %v", err)
	}
}
