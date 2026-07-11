-- +goose Up
CREATE TABLE artifacts (
    artifact_id    TEXT PRIMARY KEY,
    track_id       TEXT NOT NULL REFERENCES tracks(track_id),
    format         TEXT NOT NULL,
    bitrate_kbps   INTEGER NOT NULL DEFAULT 0,
    source_version INTEGER NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT 'queued',
    bytes          INTEGER,
    path           TEXT,
    error          TEXT,
    released       INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL,
    last_access    INTEGER NOT NULL,
    UNIQUE(track_id, format, bitrate_kbps, source_version)
);
CREATE INDEX artifacts_status ON artifacts(status, created_at);

-- +goose Down
DROP TABLE artifacts;
