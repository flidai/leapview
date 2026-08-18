-- +goose Up

-- Bind serving-state/artifact rows to the durable build attempt before any
-- private DuckLake construction. This closes the crash window between
-- release materialization and catalog construction.
CREATE TABLE delivery_build_artifact_bindings (
  attempt_id TEXT PRIMARY KEY REFERENCES delivery_build_attempts(id) ON DELETE CASCADE,
  serving_artifact_id TEXT NOT NULL,
  serving_artifact_digest TEXT NOT NULL,
  serving_state_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  CHECK (length(serving_artifact_id) BETWEEN 1 AND 128
    AND serving_artifact_id = trim(serving_artifact_id)
    AND serving_artifact_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  CHECK (length(serving_artifact_digest) = 71
    AND substr(serving_artifact_digest, 1, 7) = 'sha256:'
    AND substr(serving_artifact_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  CHECK (length(serving_state_id) BETWEEN 1 AND 128
    AND serving_state_id = trim(serving_state_id)
    AND serving_state_id NOT GLOB '*[^A-Za-z0-9._:/-]*')
);

-- +goose Down
DROP TABLE delivery_build_artifact_bindings;
