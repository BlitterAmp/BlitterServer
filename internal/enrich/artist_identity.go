package enrich

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/BlitterAmp/BlitterServer/internal/activity"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/musicbrainz"
	"github.com/BlitterAmp/BlitterServer/internal/progress"
)

type artistIdentitySummary struct {
	activity.Counts
	Consolidated, Cancelled bool
}

func (e *Enricher) runArtistIdentityStage(ctx context.Context, markDirty func(), publish func(bool)) (summary artistIdentitySummary, token activity.Token) {
	token = e.activity.Start(activity.StageMusicBrainzArtistMetadata, summary.Counts)
	if e.mbClient == nil {
		return summary, token
	}
	log := logging.From(ctx).With("component", "enrich")
	due, err := e.st.CountPendingMusicBrainzArtists(ctx)
	if err != nil {
		log.Error("musicbrainz artist metadata failed", "err", err)
		summary.Failed++
		e.activity.Fail(token, summary.Counts)
		return summary, token
	}
	summary.Total, summary.Remaining = due, due
	e.activity.Update(token, summary.Counts)
	reporter := progress.New(e.Now, e.LogProgressInterval)
	log.Info("musicbrainz artist metadata started", "due", due)
	defer func() {
		remaining, _ := e.st.CountPendingMusicBrainzArtists(context.WithoutCancel(ctx))
		summary.Remaining, summary.Cancelled = remaining, ctx.Err() != nil
		message := "musicbrainz artist metadata completed"
		if ctx.Err() != nil {
			message = "musicbrainz artist metadata cancelled"
		} else if summary.Failed > 0 {
			message = "musicbrainz artist metadata failed"
		}
		args := []any{"due", due, "processed", summary.Processed, "changed", summary.Changed, "terminal", summary.Terminal, "failed", summary.Failed, "remaining", remaining, "consolidated", summary.Consolidated, "duration_ms", reporter.DurationMS()}
		if summary.Failed > 0 && ctx.Err() == nil {
			e.activity.Fail(token, summary.Counts)
			log.Error(message, args...)
		} else {
			e.activity.Update(token, summary.Counts)
			log.Info(message, args...)
		}
	}()
	report := func() {
		summary.Processed++
		if reporter.Due(summary.Processed) {
			remaining, _ := e.st.CountPendingMusicBrainzArtists(ctx)
			summary.Remaining = remaining
			e.activity.Update(token, summary.Counts)
			log.Info("musicbrainz artist metadata progress", "due", due, "processed", summary.Processed, "changed", summary.Changed, "terminal", summary.Terminal, "failed", summary.Failed, "remaining", remaining, "duration_ms", reporter.DurationMS())
		}
	}
	cursor := ""
	for {
		if ctx.Err() != nil {
			return summary, token
		}
		artists, next, err := e.st.PendingMusicBrainzArtists(ctx, cursor, perRun)
		if err != nil {
			log.Error("list artists pending MusicBrainz metadata", "err", err)
			summary.Failed++
			return summary, token
		}
		for _, artist := range artists {
			if ctx.Err() != nil {
				return summary, token
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
					if markErr := e.markArtistMetadataTerminal(ctx, artist.ArtistID, artist.MusicBrainzID); markErr != nil {
						log.Warn("mark MusicBrainz artist metadata terminal", "artist_id", artist.ArtistID, "err", markErr)
						summary.Failed++
					} else {
						summary.Terminal++
					}
					report()
					continue
				}
				log.Warn("fetch MusicBrainz artist metadata", "artist_id", artist.ArtistID, "err", err)
				summary.Failed++
				report()
				continue
			}
			if body.ID != artist.MusicBrainzID {
				log.Warn("MusicBrainz artist metadata id mismatch", "artist_id", artist.ArtistID)
				if err := e.markArtistMetadataTerminal(ctx, artist.ArtistID, artist.MusicBrainzID); err != nil {
					log.Warn("mark mismatched MusicBrainz artist metadata terminal", "artist_id", artist.ArtistID, "err", err)
					summary.Failed++
				} else {
					summary.Terminal++
				}
				report()
				continue
			}
			aliases := make([]string, 0, len(body.Aliases))
			for _, alias := range body.Aliases {
				aliases = append(aliases, alias.Name)
			}
			changed, err := e.st.PersistMusicBrainzArtistMetadataAtNextSequence(ctx, artist.ArtistID, artist.MusicBrainzID, body.Name, aliases)
			if err != nil {
				log.Warn("apply MusicBrainz artist metadata", "artist_id", artist.ArtistID, "err", err)
				summary.Failed++
				report()
				continue
			}
			if changed {
				summary.Changed++
				markDirty()
				publish(false)
			}
			report()
		}
		if next == "" {
			break
		}
		cursor = next
	}
	changed, err := e.consolidateArtists(ctx)
	if err != nil {
		log.Warn("consolidate MusicBrainz artists", "err", err)
		summary.Failed++
		return summary, token
	}
	if changed {
		summary.Consolidated = true
		markDirty()
		publish(false)
	}
	return summary, token
}

func terminalMusicBrainzArtistError(err error) bool {
	var statusErr *musicbrainz.HTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.StatusCode >= 400 && statusErr.StatusCode < 500 &&
		statusErr.StatusCode != http.StatusRequestTimeout && statusErr.StatusCode != http.StatusTooManyRequests
}
