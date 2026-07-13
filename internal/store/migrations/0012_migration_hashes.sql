-- +goose Up
CREATE TABLE migration_hashes (
    version INTEGER PRIMARY KEY,
    sha256  TEXT NOT NULL
);

-- +goose Down
DROP TABLE migration_hashes;
