-- +goose Up
-- Artist photos are never legitimately assigned by the filesystem scan.
-- Shared, untried art is album fallback/provider-placeholder contamination;
-- clear it and publish one change sequence so enrichment and mirrors retry.
INSERT INTO settings(key, value)
SELECT 'library_scan_seq', '1'
WHERE EXISTS (
  SELECT 1 FROM artists
  WHERE missing=0 AND art_tried=0 AND art_id IS NOT NULL
  GROUP BY art_id HAVING count(*) > 1
)
ON CONFLICT(key) DO UPDATE SET value = CAST(value AS INTEGER) + 1;

UPDATE artists
SET art_id = NULL,
    art_tried = 0,
    art_tried_at = 0,
    art_next_attempt_at = 0,
    art_miss_count = 0,
    change_seq = (SELECT CAST(value AS INTEGER) FROM settings WHERE key='library_scan_seq')
WHERE missing=0
  AND art_tried=0
  AND art_id IS NOT NULL
  AND art_id IN (
    SELECT art_id FROM artists
    WHERE missing=0 AND art_tried=0 AND art_id IS NOT NULL
    GROUP BY art_id HAVING count(*) > 1
  );

-- +goose Down
SELECT 1;
