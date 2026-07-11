-- +goose Up
CREATE TABLE admin_sessions (
    token_hash TEXT PRIMARY KEY,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL
);
CREATE TABLE pairings (
    pairing_id   TEXT PRIMARY KEY,
    code         TEXT NOT NULL,
    device_name  TEXT NOT NULL,
    device_type  TEXT NOT NULL,
    app_version  TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'pending',
    device_id    TEXT REFERENCES devices(device_id) ON DELETE SET NULL,
    requested_at TEXT NOT NULL,
    expires_at   TEXT NOT NULL
);
CREATE TABLE pair_codes (
    code       TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at    TEXT
);
ALTER TABLE profiles ADD COLUMN share_listening INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE profiles DROP COLUMN share_listening;
DROP TABLE pair_codes;
DROP TABLE pairings;
DROP TABLE admin_sessions;
