-- +goose Up
-- Per-entity change tracking for client delta-sync. change_seq is stamped with
-- the scan seq (a persistent monotonic counter) whenever a row's client-visible
-- content actually changes, so `GET /v1/changes?since=N` can return only rows
-- with change_seq > N. Soft-deletes keep the row (missing=1) with a bumped
-- change_seq so clients learn to drop it.
ALTER TABLE artists ADD COLUMN change_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE albums ADD COLUMN change_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tracks ADD COLUMN change_seq INTEGER NOT NULL DEFAULT 0;

-- Backfill existing rows to the current scan seq: a client bootstrapping via
-- changes?since=0 receives them, while a client already at that version does not.
UPDATE artists SET change_seq = COALESCE((SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'library_scan_seq'), 0);
UPDATE albums SET change_seq = COALESCE((SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'library_scan_seq'), 0);
UPDATE tracks SET change_seq = COALESCE((SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'library_scan_seq'), 0);

CREATE INDEX artists_change ON artists(change_seq);
CREATE INDEX albums_change ON albums(change_seq);
CREATE INDEX tracks_change ON tracks(change_seq);

-- +goose Down
DROP INDEX tracks_change;
DROP INDEX albums_change;
DROP INDEX artists_change;
ALTER TABLE tracks DROP COLUMN change_seq;
ALTER TABLE albums DROP COLUMN change_seq;
ALTER TABLE artists DROP COLUMN change_seq;
