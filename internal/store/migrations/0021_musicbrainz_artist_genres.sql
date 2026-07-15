-- +goose Up
CREATE TABLE artist_genres (
    artist_id TEXT NOT NULL REFERENCES artists(artist_id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    name TEXT NOT NULL COLLATE NOCASE,
    PRIMARY KEY (artist_id, position),
    UNIQUE (artist_id, name)
);

ALTER TABLE artists ADD COLUMN musicbrainz_genres_fetched_at INTEGER NOT NULL DEFAULT 0;

-- A terminal artist lookup is terminal for every field returned by that lookup.
UPDATE artists SET musicbrainz_genres_fetched_at = -1
WHERE musicbrainz_aliases_fetched_at = -1;

-- Existing mirrors may contain track-tag-derived artist genres. Re-emit every
-- artist so those values are cleared while the MusicBrainz backfill runs.
INSERT INTO settings(key, value)
SELECT 'library_scan_seq', '1' WHERE EXISTS (SELECT 1 FROM artists)
ON CONFLICT(key) DO UPDATE SET value = CAST(value AS INTEGER) + 1;

UPDATE artists SET change_seq = (
    SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'library_scan_seq'
);

-- +goose Down
ALTER TABLE artists DROP COLUMN musicbrainz_genres_fetched_at;
DROP TABLE artist_genres;
