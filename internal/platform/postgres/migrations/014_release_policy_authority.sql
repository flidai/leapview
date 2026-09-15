-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- FAI-926 release transition policy authority.  The policy is scoped to one
-- exact immutable predecessor/candidate artifact pair.  Release policy
-- semantics and the domain-separated digest remain validated by the release
-- capability; these checks protect the durable shape and denormalized fields.
CREATE TABLE IF NOT EXISTS release.release_transition_policy (
    predecessor_artifact_digest text NOT NULL,
    candidate_artifact_digest text NOT NULL,
    policy_version text NOT NULL,
    policy_digest text NOT NULL,
    policy_json jsonb NOT NULL,
    published_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (predecessor_artifact_digest, candidate_artifact_digest),
    CHECK (predecessor_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (candidate_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (predecessor_artifact_digest <> candidate_artifact_digest),
    CHECK (policy_version = 'release-policy/v1'),
    CHECK (policy_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(policy_json) = 'object'),
    CHECK (octet_length(policy_json::text) BETWEEN 1 AND 262144),
    CHECK ((policy_json ->> 'version' = policy_version) IS TRUE),
    CHECK ((policy_json ->> 'digest' = policy_digest) IS TRUE),
    CHECK ((jsonb_typeof(policy_json -> 'rules') = 'array') IS TRUE),
    CHECK ((jsonb_array_length(policy_json -> 'rules') BETWEEN 1 AND 128) IS TRUE)
);

-- Policy rows are final evidence.  The trigger also blocks privileged SQL
-- UPDATE/DELETE and the explicit statement trigger closes TRUNCATE.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION release.reject_transition_policy_mutation()
RETURNS trigger LANGUAGE plpgsql
SET search_path = pg_catalog, release
AS $$
BEGIN
    RAISE EXCEPTION 'release transition policy is immutable';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS release_transition_policy_immutable ON release.release_transition_policy;
CREATE TRIGGER release_transition_policy_immutable
    BEFORE UPDATE OR DELETE ON release.release_transition_policy
    FOR EACH ROW EXECUTE FUNCTION release.reject_transition_policy_mutation();
DROP TRIGGER IF EXISTS release_transition_policy_no_truncate ON release.release_transition_policy;
CREATE TRIGGER release_transition_policy_no_truncate
    BEFORE TRUNCATE ON release.release_transition_policy
    FOR EACH STATEMENT EXECUTE FUNCTION release.reject_transition_policy_mutation();

REVOKE ALL ON TABLE release.release_transition_policy FROM PUBLIC;
REVOKE ALL ON FUNCTION release.reject_transition_policy_mutation() FROM PUBLIC;

-- Publication is a maintenance operation.  Runtime resolution is strictly
-- read-only, including for callers that can otherwise write release rows.
-- Readonly and backup roles retain SELECT through their evidence snapshots.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_owner;
        GRANT ALL ON release.release_transition_policy TO leapview_control_owner;
        GRANT EXECUTE ON FUNCTION release.reject_transition_policy_mutation() TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_migrator;
        GRANT ALL ON release.release_transition_policy TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION release.reject_transition_policy_mutation() TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_runtime;
        GRANT SELECT ON release.release_transition_policy TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.release_transition_policy FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_maintenance;
        GRANT SELECT, INSERT ON release.release_transition_policy TO leapview_control_maintenance;
        REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.release_transition_policy FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_readonly;
        GRANT SELECT ON release.release_transition_policy TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.release_transition_policy FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_backup;
        GRANT SELECT ON release.release_transition_policy TO leapview_control_backup;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.release_transition_policy FROM leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'release policy authority migration is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
