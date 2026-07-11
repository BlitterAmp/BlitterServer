-- +goose Up
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE profiles (
    profile_id   TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    pin_hash     TEXT,
    avatar_color TEXT,
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE devices (
    device_id    TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    type         TEXT NOT NULL,
    paired_at    TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen_at TEXT
);
CREATE TABLE device_tokens (
    token_hash TEXT PRIMARY KEY,
    device_id  TEXT NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE profile_tokens (
    token_hash TEXT PRIMARY KEY,
    device_id  TEXT NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL REFERENCES profiles(profile_id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- +goose Down
DROP TABLE profile_tokens;
DROP TABLE device_tokens;
DROP TABLE devices;
DROP TABLE profiles;
DROP TABLE settings;
