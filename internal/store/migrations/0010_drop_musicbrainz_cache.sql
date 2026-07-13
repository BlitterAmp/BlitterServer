-- +goose Up
DROP TABLE musicbrainz_cache;

-- +goose Down
CREATE TABLE musicbrainz_cache (
    cache_key TEXT PRIMARY KEY,
    status INTEGER NOT NULL,
    body BLOB,
    fetched_at INTEGER NOT NULL,
    fresh_until INTEGER NOT NULL,
    etag TEXT,
    last_modified TEXT,
    retry_at INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT ''
);
