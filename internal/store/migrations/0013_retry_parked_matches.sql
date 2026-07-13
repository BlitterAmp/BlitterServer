-- +goose Up
-- The resolver gained edition-consensus identity extraction and search-title
-- normalization; previously parked albums deserve one immediate re-evaluation
-- (cached MusicBrainz responses make it cheap).
UPDATE album_musicbrainz_matches SET next_attempt_at = 0 WHERE state IN ('ambiguous', 'unmatched');

-- +goose Down
SELECT 1;
