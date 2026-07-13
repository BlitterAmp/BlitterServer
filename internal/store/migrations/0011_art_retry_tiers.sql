-- +goose Up
ALTER TABLE albums ADD COLUMN art_next_attempt_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE albums ADD COLUMN art_miss_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE artists ADD COLUMN art_next_attempt_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE artists ADD COLUMN art_miss_count INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE artists DROP COLUMN art_miss_count;
ALTER TABLE artists DROP COLUMN art_next_attempt_at;
ALTER TABLE albums DROP COLUMN art_miss_count;
ALTER TABLE albums DROP COLUMN art_next_attempt_at;
