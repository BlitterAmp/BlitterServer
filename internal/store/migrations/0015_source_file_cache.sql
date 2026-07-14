-- +goose Up
CREATE TABLE source_file_cache (
    source_instance_id TEXT NOT NULL,
    source_kind        TEXT NOT NULL,
    native_id          TEXT NOT NULL,
    size_bytes         INTEGER NOT NULL,
    mtime_ns           INTEGER NOT NULL,
    parser_version     INTEGER NOT NULL,
    parsed_meta_json   TEXT NOT NULL,
    art_pending        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (source_instance_id, source_kind, native_id)
);

INSERT OR IGNORE INTO settings(key, value) VALUES ('filesystem_source_generation', '0');

-- +goose Down
DELETE FROM settings WHERE key IN ('filesystem_source_id', 'filesystem_source_generation');
DROP TABLE source_file_cache;
