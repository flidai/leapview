-- +goose Up

-- Failed candidate gates are retained as non-secret audit evidence. The row
-- is never consulted by candidate publication or runtime activation.
CREATE TABLE delivery_failed_gate_evidence (
  attempt_id TEXT PRIMARY KEY REFERENCES delivery_build_attempts(id) ON DELETE CASCADE,
  evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),
  evidence_digest TEXT NOT NULL CHECK (length(evidence_digest) = 71
    AND substr(evidence_digest, 1, 7) = 'sha256:'
    AND substr(evidence_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  created_at TEXT NOT NULL
);

-- +goose Down

DROP TABLE delivery_failed_gate_evidence;
