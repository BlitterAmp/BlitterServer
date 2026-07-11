// Package server implements the contract's strict interface. Anything not
// overridden here inherits api.Unimplemented's 501.
package server

import (
	"context"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/store"
	"github.com/BlitterAmp/BlitterServer/internal/transcode"
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
	return api.GetPing200JSONResponse{Name: "BlitterServer", Version: s.version, SetupComplete: &done}, nil
}

func (s *Server) GetStatus(ctx context.Context, _ api.GetStatusRequestObject) (api.GetStatusResponseObject, error) {
	done, err := s.st.SetupComplete(ctx)
	if err != nil {
		return nil, err
	}
	resp := api.GetStatus200JSONResponse{Version: s.version, SetupComplete: done}
	// Literal until source config lands in the store (spec 2) — then this must come from settings.
	resp.Source.Kind = api.Plex
	resp.Source.Connected = false
	return resp, nil
}

func (s *Server) GetCapabilities(ctx context.Context, _ api.GetCapabilitiesRequestObject) (api.GetCapabilitiesResponseObject, error) {
	formats := []api.CapabilitiesTranscodeFormats{api.CapabilitiesTranscodeFormatsOriginal}
	if transcode.FFmpegAvailable() {
		formats = append(formats, api.CapabilitiesTranscodeFormatsAac)
	}
	return api.GetCapabilities200JSONResponse{
		Acquisition: false, Lastfm: false,
		TranscodeFormats: formats,
	}, nil
}
