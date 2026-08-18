-- +goose Up

-- Structured, non-secret plan explanation is durable evidence.  The digest
-- binds it into the immutable plan identity; component target revisions remain
-- the sole publication CAS authority.
ALTER TABLE delivery_plans
  ADD COLUMN evidence_json TEXT NOT NULL DEFAULT '{}'
    CHECK (json_valid(evidence_json) AND json_type(evidence_json) = 'object');
ALTER TABLE delivery_plans
  ADD COLUMN evidence_digest TEXT NOT NULL DEFAULT 'sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a'
    CHECK (length(evidence_digest) = 71 AND substr(evidence_digest, 1, 7) = 'sha256:'
      AND substr(evidence_digest, 8) NOT GLOB '*[^0-9a-f]*');

-- Build-time resolved input evidence is attached to the immutable candidate;
-- an empty object is retained for legacy candidates that had no live inputs.
ALTER TABLE delivery_candidates
  ADD COLUMN resolved_inputs_json TEXT NOT NULL DEFAULT '{}'
    CHECK (json_valid(resolved_inputs_json) AND json_type(resolved_inputs_json) = 'object');
ALTER TABLE delivery_candidates
  ADD COLUMN resolved_inputs_digest TEXT NOT NULL DEFAULT 'sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a'
    CHECK (length(resolved_inputs_digest) = 71 AND substr(resolved_inputs_digest, 1, 7) = 'sha256:'
      AND substr(resolved_inputs_digest, 8) NOT GLOB '*[^0-9a-f]*');

-- +goose Down
ALTER TABLE delivery_candidates DROP COLUMN resolved_inputs_digest;
ALTER TABLE delivery_candidates DROP COLUMN resolved_inputs_json;
ALTER TABLE delivery_plans DROP COLUMN evidence_digest;
ALTER TABLE delivery_plans DROP COLUMN evidence_json;

