-- +goose Up

-- A delivery candidate/generation is only queryable when it names the exact
-- compiled serving artifact that produced its sealed catalog. Empty defaults
-- keep this additive migration compatible with pre-existing control rows;
-- deployment domain validation rejects empty identities for ready/prepared
-- states before they can be published or served.
ALTER TABLE delivery_candidates
  ADD COLUMN serving_artifact_id TEXT NOT NULL DEFAULT ''
    CHECK (serving_artifact_id = trim(serving_artifact_id)
      AND (serving_artifact_id = '' OR (length(serving_artifact_id) BETWEEN 1 AND 128
        AND serving_artifact_id NOT GLOB '*[^A-Za-z0-9._:/-]*')));

ALTER TABLE delivery_candidates
  ADD COLUMN serving_artifact_digest TEXT NOT NULL DEFAULT ''
    CHECK (serving_artifact_digest = '' OR (length(serving_artifact_digest) = 71
      AND substr(serving_artifact_digest, 1, 7) = 'sha256:'
      AND substr(serving_artifact_digest, 8) NOT GLOB '*[^0-9a-f]*'));

ALTER TABLE delivery_catalog_seals
  ADD COLUMN serving_artifact_id TEXT NOT NULL DEFAULT ''
    CHECK (serving_artifact_id = trim(serving_artifact_id)
      AND (serving_artifact_id = '' OR (length(serving_artifact_id) BETWEEN 1 AND 128
        AND serving_artifact_id NOT GLOB '*[^A-Za-z0-9._:/-]*')));

ALTER TABLE delivery_catalog_seals
  ADD COLUMN serving_artifact_digest TEXT NOT NULL DEFAULT ''
    CHECK (serving_artifact_digest = '' OR (length(serving_artifact_digest) = 71
      AND substr(serving_artifact_digest, 1, 7) = 'sha256:'
      AND substr(serving_artifact_digest, 8) NOT GLOB '*[^0-9a-f]*'));

ALTER TABLE delivery_generations
  ADD COLUMN serving_artifact_id TEXT NOT NULL DEFAULT ''
    CHECK (serving_artifact_id = trim(serving_artifact_id)
      AND (serving_artifact_id = '' OR (length(serving_artifact_id) BETWEEN 1 AND 128
        AND serving_artifact_id NOT GLOB '*[^A-Za-z0-9._:/-]*')));

ALTER TABLE delivery_generations
  ADD COLUMN serving_artifact_digest TEXT NOT NULL DEFAULT ''
    CHECK (serving_artifact_digest = '' OR (length(serving_artifact_digest) = 71
      AND substr(serving_artifact_digest, 1, 7) = 'sha256:'
      AND substr(serving_artifact_digest, 8) NOT GLOB '*[^0-9a-f]*'));

-- Existing pre-078 rows remain explicitly unbound (the empty defaults above)
-- and therefore fail closed in the deployment read models. Every newly
-- created or transitioned serving row must carry the exact immutable binding.
-- +goose StatementBegin
CREATE TRIGGER delivery_catalog_seals_serving_identity_guard
BEFORE INSERT ON delivery_catalog_seals
WHEN NEW.serving_artifact_id = '' OR NEW.serving_artifact_digest = ''
BEGIN
  SELECT RAISE(ABORT, 'catalog seal requires serving artifact identity');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER delivery_catalog_seals_serving_identity_guard_update
BEFORE UPDATE OF serving_artifact_id, serving_artifact_digest ON delivery_catalog_seals
WHEN NEW.serving_artifact_id = '' OR NEW.serving_artifact_digest = ''
BEGIN
  SELECT RAISE(ABORT, 'catalog seal requires serving artifact identity');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER delivery_candidates_serving_identity_guard
BEFORE INSERT ON delivery_candidates
WHEN NEW.status IN ('ready', 'retired')
  AND (NEW.serving_artifact_id = '' OR NEW.serving_artifact_digest = '')
BEGIN
  SELECT RAISE(ABORT, 'ready candidate requires serving artifact identity');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER delivery_candidates_serving_identity_guard_update
BEFORE UPDATE OF status, serving_artifact_id, serving_artifact_digest ON delivery_candidates
WHEN NEW.status IN ('ready', 'retired')
  AND (NEW.serving_artifact_id = '' OR NEW.serving_artifact_digest = '')
BEGIN
  SELECT RAISE(ABORT, 'ready candidate requires serving artifact identity');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER delivery_generations_serving_identity_guard
BEFORE INSERT ON delivery_generations
WHEN NEW.serving_artifact_id = '' OR NEW.serving_artifact_digest = ''
BEGIN
  SELECT RAISE(ABORT, 'generation requires serving artifact identity');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER delivery_generations_serving_identity_guard_update
BEFORE UPDATE OF status, serving_artifact_id, serving_artifact_digest ON delivery_generations
WHEN NEW.serving_artifact_id = '' OR NEW.serving_artifact_digest = ''
BEGIN
  SELECT RAISE(ABORT, 'generation requires serving artifact identity');
END;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER delivery_generations_serving_identity_guard_update;
DROP TRIGGER delivery_generations_serving_identity_guard;
DROP TRIGGER delivery_candidates_serving_identity_guard_update;
DROP TRIGGER delivery_candidates_serving_identity_guard;
DROP TRIGGER delivery_catalog_seals_serving_identity_guard_update;
DROP TRIGGER delivery_catalog_seals_serving_identity_guard;
ALTER TABLE delivery_generations DROP COLUMN serving_artifact_digest;
ALTER TABLE delivery_generations DROP COLUMN serving_artifact_id;
ALTER TABLE delivery_catalog_seals DROP COLUMN serving_artifact_digest;
ALTER TABLE delivery_catalog_seals DROP COLUMN serving_artifact_id;
ALTER TABLE delivery_candidates DROP COLUMN serving_artifact_digest;
ALTER TABLE delivery_candidates DROP COLUMN serving_artifact_id;
