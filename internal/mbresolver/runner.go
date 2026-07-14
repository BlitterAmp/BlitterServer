package mbresolver

import (
	"context"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/musicbrainz"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

type Resolver struct {
	st        *store.Store
	client    *musicbrainz.Client
	now       func() time.Time
	batchSize int
}

func New(st *store.Store, client *musicbrainz.Client) *Resolver {
	return &Resolver{st: st, client: client, now: time.Now, batchSize: 5}
}

type ResolutionProgress struct {
	Processed int
	Applied   int
	Failed    int
}

func (r *Resolver) Run(ctx context.Context) (bool, error) {
	return r.RunWithProgress(ctx, nil)
}

// RunWithProgress invokes progress after every due album, regardless of outcome.
func (r *Resolver) RunWithProgress(ctx context.Context, progress func(ResolutionProgress)) (bool, error) {
	changed := false
	stats := ResolutionProgress{}
	var batchErr error
	var afterDueAt int64 = -1
	var afterID string
	for {
		albums, err := r.st.DueMusicBrainzAlbumsPage(ctx, r.now(), afterDueAt, afterID, r.batchSize)
		if err != nil {
			return changed, err
		}
		if len(albums) == 0 {
			return changed, batchErr
		}
		for _, album := range albums {
			afterDueAt, afterID = album.DueAt, album.AlbumID
			if err := ctx.Err(); err != nil {
				return changed, err
			}
			applied, err := r.resolve(ctx, album)
			if ctx.Err() != nil {
				return changed, ctx.Err()
			}
			stats.Processed++
			if err != nil {
				stats.Failed++
				if progress != nil {
					progress(stats)
				}
				batchErr = err
				continue
			}
			changed = changed || applied
			if applied {
				stats.Applied++
			}
			if progress != nil {
				progress(stats)
			}
		}
	}
}
