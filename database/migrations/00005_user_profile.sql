-- +goose Up
ALTER TABLE users ADD COLUMN avatar_jpeg bytea CHECK (octet_length(avatar_jpeg) BETWEEN 1 AND 36864);
ALTER TABLE users ADD COLUMN profile_revision bigint NOT NULL DEFAULT 0 CHECK (profile_revision BETWEEN 0 AND 9007199254740991);

-- +goose Down
ALTER TABLE users DROP COLUMN profile_revision;
ALTER TABLE users DROP COLUMN avatar_jpeg;
