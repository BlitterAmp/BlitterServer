// Package server implements the contract's strict interface. Anything not
// overridden here inherits api.Unimplemented's 501.
package server

import (
	"context"

	"github.com/BlitterAmp/Blittarr/internal/api"
	"github.com/BlitterAmp/Blittarr/internal/store"
	"github.com/BlitterAmp/Blittarr/internal/transcode"
)

type Server struct {
	api.Unimplemented
	st      *store.Store
	version string
}

func New(st *store.Store, version string) *Server {
	return &Server{st: st, version: version}
}

func (s *Server) GetPing(ctx context.Context, _ api.GetPingRequestObject) (api.GetPingResponseObject, error) {
	done, err := s.st.SetupComplete(ctx)
	if err != nil {
		return nil, err
	}
	return api.GetPing200JSONResponse{Name: "Blittarr", Version: s.version, SetupComplete: &done}, nil
}

func (s *Server) GetStatus(ctx context.Context, _ api.GetStatusRequestObject) (api.GetStatusResponseObject, error) {
	done, err := s.st.SetupComplete(ctx)
	if err != nil {
		return nil, err
	}
	resp := api.GetStatus200JSONResponse{Version: s.version, SetupComplete: done}
	resp.Source.Kind = api.ServerStatusSourceKind("plex")
	resp.Source.Connected = false
	return resp, nil
}

func (s *Server) GetCapabilities(ctx context.Context, _ api.GetCapabilitiesRequestObject) (api.GetCapabilitiesResponseObject, error) {
	formats := []api.CapabilitiesTranscodeFormats{"original"}
	if transcode.FFmpegAvailable() {
		formats = append(formats, "aac")
	}
	return api.GetCapabilities200JSONResponse{
		Acquisition: false, Lastfm: false,
		TranscodeFormats: formats,
	}, nil
}
