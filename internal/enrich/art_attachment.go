package enrich

import (
	"context"
)

func (e *Enricher) attachAlbumArt(ctx context.Context, albumID, artID string) (bool, error) {
	return e.st.SetAlbumArtAtNextSequence(ctx, albumID, artID)
}

func (e *Enricher) attachArtistArt(ctx context.Context, artistID, expectedArtID, artID string) (bool, error) {
	return e.st.SetArtistArtAtNextSequence(ctx, artistID, expectedArtID, artID)
}
