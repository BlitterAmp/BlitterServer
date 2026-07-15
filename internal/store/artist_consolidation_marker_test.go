package store

import (
	"context"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/source"
)

func TestConsolidationNoOpMarkerSkipsPlanningUntilLibraryAdvances(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	canonical := source.TrackMeta{NativeID: "canonical.flac", Title: "One", Album: "Collision", PrimaryArtist: source.ArtistReference{Name: "Canonical", MBID: "canonical-mbid"}, AlbumCredits: []source.ArtistCredit{{Name: "Canonical", MBID: "canonical-mbid"}}, TrackCredits: []source.ArtistCredit{{Name: "Canonical", MBID: "canonical-mbid"}}, Container: "flac", Codec: "flac", Version: 1}
	local := source.TrackMeta{NativeID: "local.flac", Title: "Two", Album: "Collision", PrimaryArtist: source.ArtistReference{Name: "Local Alias"}, AlbumCredits: []source.ArtistCredit{{Name: "Local Alias"}}, TrackCredits: []source.ArtistCredit{{Name: "Local Alias"}}, Container: "flac", Codec: "flac", Version: 1}
	for _, meta := range []source.TrackMeta{canonical, local} {
		if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	var canonicalID string
	_ = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id='canonical-mbid'`).Scan(&canonicalID)
	metadataSeq, _ := s.NextScanSeq(ctx)
	if _, err := s.PersistMusicBrainzArtistMetadata(ctx, canonicalID, "canonical-mbid", "Canonical", []string{"Local Alias"}, nil, metadataSeq); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.ConsolidateMusicBrainzArtistsAtNextSequence(ctx); err != nil || changed {
		t.Fatalf("collision evaluation changed=%v err=%v", changed, err)
	}
	var marker string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='artist_consolidation_evaluated_seq'`).Scan(&marker); err != nil || marker != "2" {
		t.Fatalf("evaluation marker=%q err=%v", marker, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE albums SET title='No Longer Colliding' WHERE artist_id<>? AND title='Collision'`, canonicalID); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.ConsolidateMusicBrainzArtistsAtNextSequence(ctx); err != nil || changed {
		t.Fatalf("same-sequence replan changed=%v err=%v", changed, err)
	}
	if _, err := s.NextScanSeq(ctx); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.ConsolidateMusicBrainzArtistsAtNextSequence(ctx); err != nil || !changed {
		t.Fatalf("advanced-sequence replan changed=%v err=%v", changed, err)
	}
}
