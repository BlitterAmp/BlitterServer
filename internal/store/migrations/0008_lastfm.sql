-- +goose Up
-- The encrypted payload contains the PII username and secret session key.
-- The server-local key protects against casual database-only disclosure, not
-- theft of a backup containing both the database and adjacent key file.
CREATE TABLE lastfm_profiles (
    profile_id        TEXT PRIMARY KEY REFERENCES profiles(profile_id) ON DELETE CASCADE,
    encrypted_payload BLOB NOT NULL,
    connected_at      TEXT NOT NULL
);
CREATE TABLE lastfm_auth_attempts (
    state       TEXT PRIMARY KEY,
    profile_id  TEXT NOT NULL REFERENCES profiles(profile_id) ON DELETE CASCADE,
    expires_at  TEXT NOT NULL,
    claim_id    TEXT,
    claimed_at  TEXT
);
CREATE INDEX lastfm_auth_attempts_expiry ON lastfm_auth_attempts(expires_at);
ALTER TABLE playback_events ADD COLUMN play_session_id TEXT;
CREATE TABLE lastfm_play_sessions (
    play_session_id TEXT PRIMARY KEY,
    profile_id      TEXT NOT NULL REFERENCES profiles(profile_id) ON DELETE CASCADE,
    device_id       TEXT,
    track_id        TEXT NOT NULL REFERENCES tracks(track_id),
    started_at      TEXT NOT NULL,
    position_sec    REAL NOT NULL DEFAULT 0,
    relay_state     TEXT NOT NULL DEFAULT 'pending',
    now_playing_state TEXT NOT NULL DEFAULT 'pending',
    now_playing_outcome TEXT,
    claim_id        TEXT,
    claimed_at      TEXT,
    terminal_reason TEXT,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT,
    updated_at TEXT NOT NULL
);
CREATE INDEX lastfm_play_sessions_lookup ON lastfm_play_sessions(profile_id, device_id, track_id, started_at DESC);
CREATE TABLE external_artists (artist_id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE, created_at TEXT NOT NULL);
ALTER TABLE albums ADD COLUMN art_tried_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE artists ADD COLUMN art_tried_at INTEGER NOT NULL DEFAULT 0;
UPDATE albums SET art_tried_at = 0 WHERE art_tried = 1;
UPDATE artists SET art_tried_at = 0 WHERE art_tried = 1;

-- +goose Down
DROP TABLE lastfm_play_sessions;
ALTER TABLE playback_events DROP COLUMN play_session_id;
DROP TABLE external_artists;
ALTER TABLE artists DROP COLUMN art_tried_at;
ALTER TABLE albums DROP COLUMN art_tried_at;
DROP TABLE lastfm_auth_attempts;
DROP TABLE lastfm_profiles;
