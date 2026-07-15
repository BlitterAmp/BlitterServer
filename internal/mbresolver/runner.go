package mbresolver

import (
	"context"
	"fmt"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/musicbrainz"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

type Resolver struct {
	st     *store.Store
	client *musicbrainz.Client
	now    func() time.Time
}

func New(st *store.Store, client *musicbrainz.Client) *Resolver {
	return &Resolver{st: st, client: client, now: time.Now}
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
	for {
		due, err := r.st.CountDueMusicBrainzAlbums(ctx, r.now())
		if err != nil {
			return changed, err
		}
		if due == 0 {
			return changed, batchErr
		}
		albums, err := r.st.DueMusicBrainzAlbums(ctx, r.now(), int(due))
		if err != nil {
			return changed, err
		}
		// Identity rank changes as this pass applies matches. Snapshot the ordered
		// due set first so those mutations cannot make keyset pagination skip rows.
		passApplied := 0
		for _, album := range albums {
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
				batchErr = fmt.Errorf("resolve album %s: %w", album.AlbumID, err)
				continue
			}
			changed = changed || applied
			if applied {
				stats.Applied++
				passApplied++
			}
			if progress != nil {
				progress(stats)
			}
		}
		remaining, err := r.st.CountDueMusicBrainzAlbums(ctx, r.now())
		if err != nil {
			return changed, err
		}
		if remaining == 0 {
			return changed, batchErr
		}
		if remaining >= due && passApplied == 0 {
			return changed, batchErr
		}
	}
}
