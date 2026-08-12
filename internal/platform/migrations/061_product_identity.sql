-- +goose Up
CREATE TABLE IF NOT EXISTS product_identity (
  singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
  display_name TEXT NOT NULL CHECK(length(trim(display_name)) BETWEEN 1 AND 120),
  logo_sha256 TEXT,
  logo_media_type TEXT,
  logo_size_bytes INTEGER,
  logo_width INTEGER,
  logo_height INTEGER,
  revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK(
    (logo_sha256 IS NULL AND logo_media_type IS NULL AND logo_size_bytes IS NULL AND logo_width IS NULL AND logo_height IS NULL)
    OR
    (length(logo_sha256) = 64 AND logo_sha256 = lower(logo_sha256)
      AND logo_media_type IN ('image/jpeg', 'image/png', 'image/webp')
      AND logo_size_bytes > 0 AND logo_width > 0 AND logo_height > 0)
  )
);

INSERT INTO product_identity (singleton, display_name)
VALUES (1, 'LeapView')
ON CONFLICT(singleton) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS product_identity;
