-- +goose Up
-- Marks albums/artists whose art we've already tried to fetch externally, so
-- enrichment doesn't re-query MusicBrainz/CAA/last.fm/fanart.tv every scan.
ALTER TABLE albums ADD COLUMN art_tried INTEGER NOT NULL DEFAULT 0;
ALTER TABLE artists ADD COLUMN art_tried INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE artists DROP COLUMN art_tried;
ALTER TABLE albums DROP COLUMN art_tried;
