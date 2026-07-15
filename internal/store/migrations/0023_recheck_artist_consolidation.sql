-- +goose Up
-- Artist identity evidence now gives canonical names precedence over aliases.
-- Reevaluate rows that may previously have been blocked by alias collisions.
DELETE FROM settings WHERE key = 'artist_consolidation_evaluated_seq';

-- +goose Down
