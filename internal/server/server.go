// Package server implements the contract's strict interface. Anything not
// overridden here inherits api.Unimplemented's 501.
package server

import (
	"context"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/artifacts"
	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/lastfm"
	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/party"
	"github.com/BlitterAmp/BlitterServer/internal/store"
	"github.com/BlitterAmp/BlitterServer/internal/transcode"
)

// lastfmProvider is consumer-owned: domain handlers depend only on operations
// they consume, while internal/lastfm supplies the outbound adapter.
type lastfmProvider interface {
	Exchange(context.Context, string) (lastfm.Session, error)
	NowPlaying(context.Context, string, lastfm.Track) error
	Scrobble(context.Context, string, lastfm.Track) (lastfm.ScrobbleResult, error)
	Love(context.Context, string, lastfm.Track, bool) error
	Similar(context.Context, string, int) ([]lastfm.Artist, error)
	ArtistInfo(context.Context, string) (lastfm.Artist, error)
	TopArtists(context.Context, string, int) ([]lastfm.Artist, error)
}

type Server struct {
	api.Unimplemented
	st            *store.Store
	lib           *library.Manager
	bus           *events.Bus
	art           *artifacts.Manager
	pty           *party.Manager
	version       string
	lastfmFactory func(string, string) lastfmProvider
	lastfmWorker  *lastfmWorker
}

// New builds a server with a settings-restored library manager rooted at the
// store's own view of the world; use NewWithLibrary to share a manager with
// the HTTP layer.
func New(st *store.Store, version string) *Server {
	return NewWithLibrary(st, library.NewManager(st, ""), version)
}

func NewWithLibrary(st *store.Store, lib *library.Manager, version string) *Server {
	bus := events.NewBus(st)
	return NewFull(st, lib, bus, artifacts.NewManager(st, lib, bus, ""), version)
}

// NewFull wires every dependency explicitly (the HTTP layer shares the bus
// with the SSE handler and owns the artifact worker's lifecycle).
func NewFull(st *store.Store, lib *library.Manager, bus *events.Bus, art *artifacts.Manager, version string) *Server {
	s := &Server{st: st, lib: lib, bus: bus, art: art, pty: party.NewManager(st, bus), version: version, lastfmFactory: func(key, secret string) lastfmProvider { return lastfm.New(key, secret) }}
	s.lastfmWorker = newLastfmWorker(s)
	s.lastfmWorker.start()
	return s
}

func (s *Server) Close() {
	if s.art != nil {
		s.art.Stop()
	}
	if s.lastfmWorker != nil {
		s.lastfmWorker.stop()
	}
	if s.lib != nil {
		s.lib.Close()
	}
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
	lastfm, err := s.lastfmConfigured(ctx)
	if err != nil {
		return nil, err
	}
	return api.GetCapabilities200JSONResponse{
		// Acquisition stays false until an acquirer adapter actually acts on
		// loves; stored Lidarr config alone doesn't make that promise true.
		Acquisition: false, Lastfm: lastfm, Discovery: &lastfm,
		TranscodeFormats: formats,
	}, nil
}
