package server

import (
	"context"
	"errors"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/store"
	"github.com/BlitterAmp/BlitterServer/internal/transcode"
)

func apiArtifact(a store.ArtifactRow) api.Artifact {
	out := api.Artifact{
		ArtifactId: a.ArtifactID,
		TrackId:    a.TrackID,
		Format:     api.ArtifactFormat(a.Format),
		Status:     api.ArtifactStatus(a.Status),
	}
	out.AlbumId = optStr(a.AlbumID)
	out.ArtId = optStr(a.ArtID)
	out.BitrateKbps = optInt(a.BitrateKbps)
	if a.Bytes > 0 {
		out.Bytes = &a.Bytes
	}
	out.Error = optStr(a.Error)
	return out
}

func (s *Server) RequestArtifacts(ctx context.Context, req api.RequestArtifactsRequestObject) (api.RequestArtifactsResponseObject, error) {
	if _, err := profileID(ctx); err != nil {
		return nil, err
	}
	if len(req.Body.TrackIds) == 0 || len(req.Body.TrackIds) > 500 {
		return nil, badRequest("track_batch_size")
	}
	bitrate := 0
	switch req.Body.Format {
	case "original":
	case "aac":
		if req.Body.BitrateKbps == nil {
			return nil, badRequest("bitrate_required_for_aac")
		}
		bitrate = int(*req.Body.BitrateKbps)
		switch bitrate {
		case 128, 192, 256, 320:
		default:
			return nil, badRequest("bitrate_must_be_128_192_256_or_320")
		}
		if !transcode.FFmpegAvailable() {
			return nil, badRequest("transcode_unavailable")
		}
	default:
		return nil, badRequest("format_must_be_original_or_aac")
	}

	rows, err := s.art.Request(ctx, req.Body.TrackIds, string(req.Body.Format), bitrate)
	if errors.Is(err, store.ErrNotFound) {
		return nil, badRequest("unknown_track")
	}
	if err != nil {
		return nil, err
	}
	out := make(api.RequestArtifacts202JSONResponse, 0, len(rows))
	for _, a := range rows {
		out = append(out, apiArtifact(a))
	}
	return out, nil
}

func (s *Server) GetArtifact(ctx context.Context, req api.GetArtifactRequestObject) (api.GetArtifactResponseObject, error) {
	if _, err := profileID(ctx); err != nil {
		return nil, err
	}
	a, found, err := s.st.GetArtifact(ctx, req.ArtifactId)
	if err != nil {
		return nil, err
	}
	if !found {
		return api.GetArtifact404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	return api.GetArtifact200JSONResponse(apiArtifact(a)), nil
}

func (s *Server) ReleaseArtifact(ctx context.Context, req api.ReleaseArtifactRequestObject) (api.ReleaseArtifactResponseObject, error) {
	if _, err := profileID(ctx); err != nil {
		return nil, err
	}
	err := s.st.ReleaseArtifact(ctx, req.ArtifactId)
	if errors.Is(err, store.ErrNotFound) {
		// Releasing something already evicted is an honest no-op.
		return api.ReleaseArtifact204Response{}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.ReleaseArtifact204Response{}, nil
}
