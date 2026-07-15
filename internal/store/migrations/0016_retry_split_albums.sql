-- +goose Up
-- Existing ambiguous anchors were parked before split-release reconciliation
-- existed. Wake only those sharing a visible title/year with another album so
-- their next full MusicBrainz match can evaluate the structural union.
UPDATE album_musicbrainz_matches
SET next_attempt_at = 0
WHERE state = 'ambiguous'
  AND EXISTS (
    SELECT 1
    FROM albums anchor
    JOIN albums fragment
      ON fragment.album_id <> anchor.album_id
     AND fragment.missing = 0
     AND fragment.title = anchor.title COLLATE NOCASE
     AND COALESCE(fragment.year, 0) = COALESCE(anchor.year, 0)
    WHERE anchor.album_id = album_musicbrainz_matches.album_id
      AND anchor.missing = 0
  );

-- +goose Down
SELECT 1;
