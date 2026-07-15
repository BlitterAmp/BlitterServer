-- +goose Up
ALTER TABLE albums ADD COLUMN release_date TEXT;
CREATE INDEX albums_release_date ON albums(release_date) WHERE release_date IS NOT NULL AND missing = 0;

-- Revisit matched albums once because prior resolver responses discarded dates.
UPDATE album_musicbrainz_matches SET state='ambiguous', next_attempt_at=0
WHERE state='matched' AND album_id IN (SELECT album_id FROM albums WHERE release_date IS NULL);

-- +goose Down
DROP INDEX albums_release_date;
ALTER TABLE albums DROP COLUMN release_date;
