-- +goose Up
-- Retry parked misses once now that a consistent filesystem album directory
-- and disc subdirectory can provide resolver evidence when tags are malformed.
UPDATE album_musicbrainz_matches
SET next_attempt_at = 0
WHERE state IN ('unmatched', 'ambiguous');

-- +goose Down
SELECT 1;
