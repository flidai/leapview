-- +goose Up

-- Durable handoff for security audit events emitted by capability-owned
-- mutations. Source repositories insert an intent through the transaction
-- they already own; the Access dispatcher materializes it into audit_events.
CREATE TABLE audit_outbox (
  event_id TEXT PRIMARY KEY
    CHECK (length(event_id) BETWEEN 1 AND 128
      AND event_id = trim(event_id)
      AND event_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  source TEXT NOT NULL
    CHECK (length(source) BETWEEN 1 AND 128 AND source = trim(source)),
  operation TEXT NOT NULL
    CHECK (length(operation) BETWEEN 1 AND 128 AND operation = trim(operation)),
  principal_id TEXT NOT NULL DEFAULT ''
    CHECK (length(principal_id) <= 256 AND principal_id = trim(principal_id)),
  action TEXT NOT NULL
    CHECK (length(action) BETWEEN 1 AND 256 AND action = trim(action)),
  resource_kind TEXT NOT NULL DEFAULT ''
    CHECK (length(resource_kind) <= 128 AND resource_kind = trim(resource_kind)),
  resource_id TEXT NOT NULL DEFAULT ''
    CHECK (length(resource_id) <= 256 AND resource_id = trim(resource_id)),
  capability TEXT NOT NULL DEFAULT ''
    CHECK (length(capability) <= 128 AND capability = trim(capability)),
  outcome TEXT NOT NULL
    CHECK (length(outcome) BETWEEN 1 AND 64 AND outcome = trim(outcome)),
  request_id TEXT NOT NULL DEFAULT '' CHECK (length(request_id) <= 256),
  correlation_id TEXT NOT NULL DEFAULT '' CHECK (length(correlation_id) <= 256),
  aggregate_key TEXT NOT NULL
    CHECK (length(aggregate_key) BETWEEN 1 AND 512 AND aggregate_key = trim(aggregate_key)),
  aggregate_sequence INTEGER NOT NULL CHECK (aggregate_sequence >= 0),
  metadata_json TEXT NOT NULL DEFAULT '{}'
    CHECK (length(metadata_json) <= 65536
      AND json_valid(metadata_json) AND json_type(metadata_json) = 'object'),
  payload_digest TEXT NOT NULL
    CHECK (length(payload_digest) = 71 AND substr(payload_digest, 1, 7) = 'sha256:'
      AND substr(payload_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  state TEXT NOT NULL DEFAULT 'pending'
    CHECK (state IN ('pending', 'retry', 'leased', 'delivered', 'poison', 'quarantined')),
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  next_attempt_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_generation INTEGER NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
  lease_expires_at TEXT,
  last_error_code TEXT NOT NULL DEFAULT '' CHECK (length(last_error_code) <= 128),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  delivered_at TEXT,
  CHECK ((state = 'leased' AND lease_owner <> '' AND lease_expires_at IS NOT NULL)
      OR (state <> 'leased' AND lease_owner = '' AND lease_expires_at IS NULL)),
  UNIQUE(aggregate_key, aggregate_sequence)
);

CREATE INDEX audit_outbox_delivery_idx
  ON audit_outbox(state, next_attempt_at, created_at, event_id);
CREATE INDEX audit_outbox_aggregate_idx
  ON audit_outbox(aggregate_key, aggregate_sequence, state);
CREATE INDEX audit_outbox_terminal_idx
  ON audit_outbox(state, created_at, event_id)
  WHERE state IN ('poison', 'quarantined');

-- +goose Down

DROP INDEX audit_outbox_terminal_idx;
DROP INDEX audit_outbox_aggregate_idx;
DROP INDEX audit_outbox_delivery_idx;
DROP TABLE audit_outbox;
