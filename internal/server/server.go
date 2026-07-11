// Package server implements the contract's strict interface. Anything not
// overridden here inherits api.Unimplemented's 501.
package server

import (
	"context"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/store"
	"github.com/BlitterAmp/BlitterServer/internal/transcode"
)

type Server struct {
	api.Unimplemented
	st      *store.Store
	lib     *library.Manager
	version string
}

// New builds a server with a settings-restored library manager rooted at the
// store's own view of the world; use NewWithLibrary to share a manager with
// the HTTP layer.
func New(st *store.Store, version string) *Server {
	return NewWithLibrary(st, library.NewManager(st, ""), version)
}

func NewWithLibrary(st *store.Store, lib *library.Manager, version string) *Server {
	return &Server{st: st, lib: lib, version: version}
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
	resp.Source.Kind = api.ServerStatusSourceKindNone
	if kind := s.lib.SourceKind(ctx); kind != "" {
		resp.Source.Kind = api.ServerStatusSourceKind(kind)
		resp.Source.Connected = s.lib.Connected(ctx)
	}
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
