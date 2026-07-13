-- +goose Up
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

CREATE TABLE album_musicbrainz_matches (
    album_id TEXT PRIMARY KEY REFERENCES albums(album_id) ON DELETE CASCADE,
    state TEXT NOT NULL CHECK (state IN ('pending','matched','ambiguous','unmatched','error')) DEFAULT 'pending',
    release_id TEXT,
    release_group_id TEXT,
    confidence REAL NOT NULL DEFAULT 0,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE album_musicbrainz_candidates (
    album_id TEXT NOT NULL REFERENCES albums(album_id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    release_id TEXT NOT NULL,
    release_group_id TEXT,
    title TEXT NOT NULL,
    artist_credit TEXT NOT NULL,
    score REAL NOT NULL,
    evidence_json TEXT NOT NULL,
    PRIMARY KEY (album_id, position)
);

CREATE INDEX album_musicbrainz_matches_due ON album_musicbrainz_matches(next_attempt_at, state);

INSERT INTO album_musicbrainz_matches (album_id, state, next_attempt_at)
SELECT album_id, 'pending', 0 FROM albums;

-- +goose Down
DROP TABLE album_musicbrainz_candidates;
DROP TABLE album_musicbrainz_matches;
DROP TABLE musicbrainz_cache;
