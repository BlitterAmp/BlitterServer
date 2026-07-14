package server

import (
	"context"

	"github.com/BlitterAmp/BlitterServer/internal/api"
)

// ListChanges returns the catalog delta since a client's known version. Missing
// rows become removed ids; changed rows are hydrated to the same shapes the list
// endpoints return. Bootstrap is via the paged lists; this is the incremental path.
func (s *Server) ListChanges(ctx context.Context, req api.ListChangesRequestObject) (api.ListChangesResponseObject, error) {
	var since int64
	if req.Params.Since != nil {
		since = *req.Params.Since
	}
	cur := ""
	if req.Params.Cursor != nil {
		cur = *req.Params.Cursor
	}
	limit := 200
	if l := req.Params.Limit; l != nil && int(*l) > 0 && int(*l) <= 1000 {
		limit = int(*l)
	}
	scanning := s.lib != nil && s.lib.Status(ctx).Scanning
	changes, next, version, err := s.st.ChangesSnapshot(ctx, since, cur, limit)
	if err != nil {
		return nil, err
	}
	scanning = scanning || s.lib != nil && s.lib.Status(ctx).Scanning
	resp := api.ListChanges200JSONResponse{
		Version:          stableChangesVersion(since, version, scanning),
		Artists:          []api.Artist{},
		Albums:           []api.Album{},
		Tracks:           []api.Track{},
		RemovedArtistIds: []string{},
		RemovedAlbumIds:  []string{},
		RemovedTrackIds:  []string{},
	}
	if next != "" {
		resp.NextCursor = &next
	}
	for _, change := range changes {
		if change.Missing {
			switch change.Kind {
			case "artist":
				resp.RemovedArtistIds = append(resp.RemovedArtistIds, change.ID)
			case "album":
				resp.RemovedAlbumIds = append(resp.RemovedAlbumIds, change.ID)
			case "track":
				resp.RemovedTrackIds = append(resp.RemovedTrackIds, change.ID)
			}
			continue
		}
		switch change.Kind {
		case "artist":
			if artist, ok, err := s.st.GetArtist(ctx, change.ID); err != nil {
				return nil, err
			} else if ok {
				resp.Artists = append(resp.Artists, apiArtist(artist))
			}
		case "album":
			if album, ok, err := s.st.GetAlbum(ctx, change.ID); err != nil {
				return nil, err
			} else if ok {
				resp.Albums = append(resp.Albums, apiAlbum(album))
			}
		case "track":
			if track, ok, err := s.st.GetTrack(ctx, change.ID); err != nil {
				return nil, err
			} else if ok {
				resp.Tracks = append(resp.Tracks, apiTrack(track))
			}
		}
	}
	return resp, nil
}

func stableChangesVersion(since, current int64, scanning bool) int64 {
	if scanning {
		return since
	}
	return current
}
