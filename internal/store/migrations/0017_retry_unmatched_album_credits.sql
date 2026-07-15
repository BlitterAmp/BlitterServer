-- +goose Up
-- Retry prior misses once now that exact release structure can canonicalize an
-- unusable raw album-artist display string without parsing artist names.
UPDATE album_musicbrainz_matches
SET next_attempt_at = 0
WHERE state = 'unmatched';

-- +goose Down
SELECT 1;
