-- +goose Up
CREATE TABLE playlists (
    playlist_id      TEXT PRIMARY KEY,
    owner_profile_id TEXT REFERENCES profiles(profile_id) ON DELETE CASCADE,
    title            TEXT NOT NULL,
    visibility       TEXT NOT NULL DEFAULT 'private',
    origin           TEXT NOT NULL DEFAULT 'blitterserver',
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);
CREATE TABLE playlist_items (
    item_id     TEXT PRIMARY KEY,
    playlist_id TEXT NOT NULL REFERENCES playlists(playlist_id) ON DELETE CASCADE,
    track_id    TEXT NOT NULL REFERENCES tracks(track_id),
    position    INTEGER NOT NULL,
    added_at    INTEGER NOT NULL
);
CREATE INDEX playlist_items_pl ON playlist_items(playlist_id, position);
CREATE TABLE loves (
    love_id    TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL REFERENCES profiles(profile_id) ON DELETE CASCADE,
    ref        TEXT NOT NULL,
    kind       TEXT NOT NULL,
    state      TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(profile_id, ref)
);
CREATE TABLE ratings (
    profile_id TEXT NOT NULL REFERENCES profiles(profile_id) ON DELETE CASCADE,
    item_id    TEXT NOT NULL,
    item_type  TEXT NOT NULL,
    rating10   INTEGER NOT NULL,
    PRIMARY KEY (profile_id, item_id)
);
CREATE TABLE playback_events (
    event_id     TEXT PRIMARY KEY,
    profile_id   TEXT NOT NULL REFERENCES profiles(profile_id) ON DELETE CASCADE,
    device_id    TEXT,
    type         TEXT NOT NULL,
    track_id     TEXT NOT NULL,
    position_sec REAL,
    at           TEXT NOT NULL,
    received_at  TEXT NOT NULL
);
CREATE INDEX playback_events_profile ON playback_events(profile_id, at);
CREATE TABLE recommendations (
    recommendation_id TEXT PRIMARY KEY,
    from_profile_id   TEXT NOT NULL REFERENCES profiles(profile_id) ON DELETE CASCADE,
    to_profile_id     TEXT NOT NULL REFERENCES profiles(profile_id) ON DELETE CASCADE,
    ref               TEXT NOT NULL,
    kind              TEXT NOT NULL,
    note              TEXT,
    seen              INTEGER NOT NULL DEFAULT 0,
    created_at        TEXT NOT NULL
);
CREATE INDEX recommendations_to ON recommendations(to_profile_id, created_at);
CREATE TABLE events (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    type       TEXT NOT NULL,
    profile_id TEXT,
    at         TEXT NOT NULL,
    data       TEXT NOT NULL
);
CREATE TABLE presence (
    profile_id TEXT PRIMARY KEY REFERENCES profiles(profile_id) ON DELETE CASCADE,
    track_id   TEXT NOT NULL,
    playing    INTEGER NOT NULL,
    at         TEXT NOT NULL
);

-- +goose Down
DROP TABLE presence;
DROP TABLE events;
DROP TABLE recommendations;
DROP TABLE playback_events;
DROP TABLE ratings;
DROP TABLE loves;
DROP TABLE playlist_items;
DROP TABLE playlists;
