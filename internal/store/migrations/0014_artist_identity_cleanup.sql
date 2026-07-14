-- +goose Up
ALTER TABLE artists ADD COLUMN musicbrainz_aliases_fetched_at INTEGER NOT NULL DEFAULT 0;

-- Re-emit removals at one fresh version so existing mirrors drop artists that
-- were already missing before album ownership became the resource boundary.
INSERT INTO settings(key, value)
SELECT 'library_scan_seq', '1' WHERE EXISTS (SELECT 1 FROM artists)
ON CONFLICT(key) DO UPDATE SET value = CAST(value AS INTEGER) + 1;

UPDATE artists SET
    missing = 1,
    change_seq = (SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'library_scan_seq')
WHERE missing = 1
   OR NOT EXISTS (
       SELECT 1 FROM albums
       WHERE albums.artist_id = artists.artist_id AND albums.missing = 0
   );

-- +goose Down
ALTER TABLE artists DROP COLUMN musicbrainz_aliases_fetched_at;
