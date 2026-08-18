-- +goose Up

-- LEA-416: a migrated row with no serving-state identity must never become
-- queryable.  Migration 082 intentionally retained empty values so an
-- operator can inspect and repair old control rows; all transitioned
-- delivery rows fail closed at the database boundary.
-- A verified seal is the only seal that can be selected by a candidate.  Keep
-- the guard on every state transition so a pre-082 row cannot be promoted.
-- +goose StatementBegin
CREATE TRIGGER delivery_catalog_seals_serving_state_identity_guard_update
BEFORE UPDATE OF status, serving_state_id ON delivery_catalog_seals
WHEN NEW.status IN ('uploaded', 'verified') AND NEW.serving_state_id = ''
BEGIN
  SELECT RAISE(ABORT, 'catalog seal requires serving state identity');
END;
-- +goose StatementEnd

-- Candidate rows are allowed to exist while being prepared, but readiness and
-- retirement both require the immutable serving-state identity.
-- +goose StatementBegin
CREATE TRIGGER delivery_candidates_serving_state_identity_guard_update
BEFORE UPDATE OF status, serving_state_id ON delivery_candidates
WHEN NEW.status IN ('ready', 'retired') AND NEW.serving_state_id = ''
BEGIN
  SELECT RAISE(ABORT, 'ready candidate requires serving state identity');
END;
-- +goose StatementEnd

-- The ready/retired invariant must also hold when a row is created directly
-- through the database boundary; application validation is not sufficient for
-- repair scripts or alternate control-store adapters.
-- +goose StatementBegin
CREATE TRIGGER delivery_candidates_serving_state_identity_guard_insert
BEFORE INSERT ON delivery_candidates
WHEN NEW.status IN ('ready', 'retired') AND NEW.serving_state_id = ''
BEGIN
  SELECT RAISE(ABORT, 'ready candidate requires serving state identity');
END;
-- +goose StatementEnd

-- Prepared, active, and retired generations are serving roots.  A generation
-- without an identity is legacy control state and must be repaired before it
-- can be selected or published.
-- +goose StatementBegin
CREATE TRIGGER delivery_generations_serving_state_identity_guard_update
BEFORE UPDATE OF status, serving_state_id ON delivery_generations
WHEN NEW.status IN ('prepared', 'active', 'retired') AND NEW.serving_state_id = ''
BEGIN
  SELECT RAISE(ABORT, 'generation requires serving state identity');
END;
-- +goose StatementEnd

-- One canonical serving-state identity may belong to only one physical
-- generation globally. Empty values remain repairable legacy rows; all
-- non-empty identities are unique at the database boundary as well as in
-- application resolvers.
CREATE UNIQUE INDEX delivery_generations_serving_state_identity_uq
  ON delivery_generations(serving_state_id)
  WHERE serving_state_id <> '';

-- A serving root cannot be introduced without the immutable serving-state
-- identity, regardless of whether it starts prepared, active, or retired.
-- +goose StatementBegin
CREATE TRIGGER delivery_generations_serving_state_identity_guard_insert
BEFORE INSERT ON delivery_generations
WHEN NEW.status IN ('prepared', 'active', 'retired') AND NEW.serving_state_id = ''
BEGIN
  SELECT RAISE(ABORT, 'generation requires serving state identity');
END;
-- +goose StatementEnd

-- +goose Down

DROP INDEX delivery_generations_serving_state_identity_uq;
DROP TRIGGER delivery_generations_serving_state_identity_guard_insert;
DROP TRIGGER delivery_generations_serving_state_identity_guard_update;
DROP TRIGGER delivery_candidates_serving_state_identity_guard_insert;
DROP TRIGGER delivery_candidates_serving_state_identity_guard_update;
DROP TRIGGER delivery_catalog_seals_serving_state_identity_guard_update;
