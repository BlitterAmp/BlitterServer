-- +goose Up
CREATE TABLE artists (
    artist_id  TEXT PRIMARY KEY,
    name       TEXT NOT NULL COLLATE NOCASE,
    musicbrainz_id TEXT UNIQUE,
    art_id     TEXT,
    missing    INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
CREATE INDEX artists_name ON artists(name COLLATE NOCASE);
CREATE TABLE artist_aliases (
    artist_id TEXT NOT NULL REFERENCES artists(artist_id) ON DELETE CASCADE,
    name TEXT NOT NULL COLLATE NOCASE,
    PRIMARY KEY (artist_id, name)
);
CREATE TABLE albums (
    album_id   TEXT PRIMARY KEY,
    artist_id  TEXT NOT NULL REFERENCES artists(artist_id),
    title      TEXT NOT NULL COLLATE NOCASE,
    musicbrainz_release_id TEXT UNIQUE,
    musicbrainz_release_group_id TEXT,
    year       INTEGER,
    art_id     TEXT,
    missing    INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX albums_untagged_identity ON albums(artist_id, title) WHERE musicbrainz_release_id IS NULL;
CREATE TABLE tracks (
    track_id     TEXT PRIMARY KEY,
    album_id     TEXT NOT NULL REFERENCES albums(album_id),
    artist_id    TEXT NOT NULL REFERENCES artists(artist_id),
    artist_name  TEXT NOT NULL,
    musicbrainz_recording_id TEXT,
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
CREATE TABLE album_artist_credits (
    album_id TEXT NOT NULL REFERENCES albums(album_id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    artist_id TEXT NOT NULL REFERENCES artists(artist_id),
    name TEXT NOT NULL,
    join_phrase TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (album_id, position)
);
CREATE INDEX album_credits_artist ON album_artist_credits(artist_id);
CREATE TABLE track_artist_credits (
    track_id TEXT NOT NULL REFERENCES tracks(track_id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    artist_id TEXT NOT NULL REFERENCES artists(artist_id),
    name TEXT NOT NULL,
    join_phrase TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (track_id, position)
);
CREATE INDEX track_credits_artist ON track_artist_credits(artist_id);
CREATE TABLE art (
    art_id TEXT PRIMARY KEY,
    hash   TEXT NOT NULL UNIQUE,
    mime   TEXT NOT NULL,
    path   TEXT NOT NULL
);

-- +goose Down
DROP TABLE art;
DROP TABLE track_artist_credits;
DROP TABLE album_artist_credits;
DROP TABLE tracks;
DROP TABLE albums;
DROP TABLE artist_aliases;
DROP TABLE artists;
