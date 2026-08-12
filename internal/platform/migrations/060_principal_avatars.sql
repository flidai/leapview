-- +goose Up
CREATE TABLE IF NOT EXISTS principal_avatars (
  principal_id TEXT PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  sha256 TEXT NOT NULL,
  media_type TEXT NOT NULL CHECK(media_type = 'image/png'),
  size_bytes INTEGER NOT NULL CHECK(size_bytes > 0),
  width INTEGER NOT NULL CHECK(width = 256),
  height INTEGER NOT NULL CHECK(height = 256),
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK(length(sha256) = 64 AND sha256 = lower(sha256))
);

-- +goose Down
DROP TABLE IF EXISTS principal_avatars;
