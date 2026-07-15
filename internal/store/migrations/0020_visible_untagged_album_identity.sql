-- +goose Up
-- Retired split fragments retain their historical rows, but must not prevent a
-- visible canonical survivor from adopting the same artist and title.
DROP INDEX albums_untagged_identity;
CREATE UNIQUE INDEX albums_untagged_identity ON albums(artist_id, title)
WHERE musicbrainz_release_id IS NULL AND missing = 0;

-- +goose Down
DROP INDEX albums_untagged_identity;
CREATE UNIQUE INDEX albums_untagged_identity ON albums(artist_id, title)
WHERE musicbrainz_release_id IS NULL;
