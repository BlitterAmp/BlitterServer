-- +goose Up
CREATE TABLE artists (
    artist_id  TEXT PRIMARY KEY,
    name       TEXT NOT NULL COLLATE NOCASE UNIQUE,
    art_id     TEXT,
    missing    INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
CREATE TABLE albums (
    album_id   TEXT PRIMARY KEY,
    artist_id  TEXT NOT NULL REFERENCES artists(artist_id),
    title      TEXT NOT NULL COLLATE NOCASE,
    year       INTEGER,
    art_id     TEXT,
    missing    INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    UNIQUE(artist_id, title)
);
CREATE TABLE tracks (
    track_id     TEXT PRIMARY KEY,
    album_id     TEXT NOT NULL REFERENCES albums(album_id),
    artist_id    TEXT NOT NULL REFERENCES artists(artist_id),
    artist_name  TEXT NOT NULL,
    title        TEXT NOT NULL COLLATE NOCASE,
    idx          INTEGER,
    disc         INTEGER,
    genre        TEXT NOT NULL DEFAULT '' COLLATE NOCASE,
    duration_ms  INTEGER NOT NULL DEFAULT 0,
    container    TEXT NOT NULL,
    codec        TEXT NOT NULL,
    bitrate_kbps INTEGER,
    size_bytes   INTEGER,
    source_kind  TEXT NOT NULL,
    native_id    TEXT NOT NULL,
    version      INTEGER NOT NULL DEFAULT 0,
    art_id       TEXT,
    seen_seq     INTEGER NOT NULL DEFAULT 0,
    missing      INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    UNIQUE(source_kind, native_id)
);
CREATE INDEX tracks_album ON tracks(album_id);
CREATE INDEX tracks_artist ON tracks(artist_id);
CREATE INDEX tracks_genre ON tracks(genre);
CREATE TABLE art (
    art_id TEXT PRIMARY KEY,
    hash   TEXT NOT NULL UNIQUE,
    mime   TEXT NOT NULL,
    path   TEXT NOT NULL
);

-- +goose Down
DROP TABLE art;
DROP TABLE tracks;
DROP TABLE albums;
DROP TABLE artists;
