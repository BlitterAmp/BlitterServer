package enrich

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/musicbrainz"
)

func (e *Enricher) runArtistIdentityStage(ctx context.Context, markDirty func(), publish func(bool)) {
	if e.mbClient == nil {
		return
	}
	log := logging.From(ctx).With("component", "enrich")
	cursor := ""
	for {
		if ctx.Err() != nil {
			return
		}
		artists, next, err := e.st.PendingMusicBrainzArtists(ctx, cursor, perRun)
		if err != nil {
			log.Error("list artists pending MusicBrainz metadata", "err", err)
			return
		}
		for _, artist := range artists {
			if ctx.Err() != nil {
				return
			}
			var body struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Aliases []struct {
					Name string `json:"name"`
				} `json:"aliases"`
			}
			path := "/artist/" + url.PathEscape(artist.MusicBrainzID) + "?inc=aliases&fmt=json"
			if err := e.mbClient.GetJSON(ctx, path, &body); err != nil {
				if ctx.Err() != nil {
					return
				}
				if terminalMusicBrainzArtistError(err) {
					if markErr := e.st.MarkMusicBrainzArtistMetadataTerminal(ctx, artist.ArtistID, artist.MusicBrainzID); markErr != nil {
						log.Warn("mark MusicBrainz artist metadata terminal", "artist_id", artist.ArtistID, "err", markErr)
					}
					continue
				}
				log.Warn("fetch MusicBrainz artist metadata", "artist_id", artist.ArtistID, "err", err)
				continue
			}
			if body.ID != artist.MusicBrainzID {
				log.Warn("MusicBrainz artist metadata id mismatch", "artist_id", artist.ArtistID, "expected_mbid", artist.MusicBrainzID, "received_mbid", body.ID)
				if err := e.st.MarkMusicBrainzArtistMetadataTerminal(ctx, artist.ArtistID, artist.MusicBrainzID); err != nil {
					log.Warn("mark mismatched MusicBrainz artist metadata terminal", "artist_id", artist.ArtistID, "err", err)
				}
				continue
			}
			aliases := make([]string, 0, len(body.Aliases))
			for _, alias := range body.Aliases {
				aliases = append(aliases, alias.Name)
			}
			changed, err := e.st.PersistMusicBrainzArtistMetadataAtNextSequence(ctx, artist.ArtistID, artist.MusicBrainzID, body.Name, aliases)
			if err != nil {
				log.Warn("apply MusicBrainz artist metadata", "artist_id", artist.ArtistID, "err", err)
				continue
			}
			if changed {
				markDirty()
				publish(false)
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	changed, err := e.st.ConsolidateMusicBrainzArtistsAtNextSequence(ctx)
	if err != nil {
		log.Warn("consolidate MusicBrainz artists", "err", err)
		return
	}
	if changed {
		markDirty()
		publish(false)
	}
}

func terminalMusicBrainzArtistError(err error) bool {
	var statusErr *musicbrainz.HTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.StatusCode >= 400 && statusErr.StatusCode < 500 &&
		statusErr.StatusCode != http.StatusRequestTimeout && statusErr.StatusCode != http.StatusTooManyRequests
}
