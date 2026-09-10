-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Additive FAI-520 handoff for exact provider versions returned by successful
-- managed-data writes. These rows are inputs to a future signed capture; they
-- are not manifests, receipts, recovery sets, or admission results.
CREATE TABLE IF NOT EXISTS managed_data.provider_observation_profile (
    profile_id text PRIMARY KEY,
    implementation text NOT NULL CHECK (implementation = 's3'),
    account_identity text NOT NULL,
    endpoint text NOT NULL,
    region text NOT NULL,
    bucket text NOT NULL,
    namespace text NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (profile_id = btrim(profile_id) AND octet_length(profile_id) BETWEEN 1 AND 4096),
    CHECK (account_identity = btrim(account_identity) AND octet_length(account_identity) BETWEEN 1 AND 4096),
    CHECK (endpoint = btrim(endpoint) AND octet_length(endpoint) BETWEEN 1 AND 4096),
    CHECK (region = btrim(region) AND octet_length(region) BETWEEN 1 AND 4096),
    CHECK (bucket = btrim(bucket) AND octet_length(bucket) BETWEEN 1 AND 4096),
    CHECK (namespace = btrim(namespace) AND octet_length(namespace) <= 4096)
);

CREATE TABLE IF NOT EXISTS managed_data.provider_version_observation (
    profile_id text NOT NULL REFERENCES managed_data.provider_observation_profile(profile_id) ON DELETE RESTRICT,
    object_key text NOT NULL,
    version_id text NOT NULL,
    sha256 text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    captured_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (profile_id, object_key),
    CHECK (object_key = btrim(object_key) AND octet_length(object_key) BETWEEN 1 AND 4096),
    CHECK (version_id = btrim(version_id) AND octet_length(version_id) BETWEEN 1 AND 4096 AND lower(version_id) NOT IN ('latest', 'null')),
    CHECK (sha256 ~ '^[0-9a-f]{64}$')
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION managed_data.reject_provider_observation_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, managed_data AS $$
BEGIN RAISE EXCEPTION 'provider-version observations are immutable'; END $$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS provider_observation_profile_immutable ON managed_data.provider_observation_profile;
CREATE TRIGGER provider_observation_profile_immutable BEFORE UPDATE OR DELETE ON managed_data.provider_observation_profile FOR EACH ROW EXECUTE FUNCTION managed_data.reject_provider_observation_mutation();
DROP TRIGGER IF EXISTS provider_version_observation_immutable ON managed_data.provider_version_observation;
CREATE TRIGGER provider_version_observation_immutable BEFORE UPDATE OR DELETE ON managed_data.provider_version_observation FOR EACH ROW EXECUTE FUNCTION managed_data.reject_provider_observation_mutation();

REVOKE ALL ON TABLE managed_data.provider_observation_profile, managed_data.provider_version_observation FROM PUBLIC;
REVOKE ALL ON FUNCTION managed_data.reject_provider_observation_mutation() FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
    GRANT SELECT, INSERT ON managed_data.provider_observation_profile, managed_data.provider_version_observation TO leapview_control_runtime;
    REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON managed_data.provider_observation_profile, managed_data.provider_version_observation FROM leapview_control_runtime;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
    GRANT SELECT ON managed_data.provider_observation_profile, managed_data.provider_version_observation TO leapview_control_maintenance;
    REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON managed_data.provider_observation_profile, managed_data.provider_version_observation FROM leapview_control_maintenance;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
    GRANT SELECT ON managed_data.provider_observation_profile, managed_data.provider_version_observation TO leapview_control_readonly;
    REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON managed_data.provider_observation_profile, managed_data.provider_version_observation FROM leapview_control_readonly;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
    GRANT SELECT ON managed_data.provider_observation_profile, managed_data.provider_version_observation TO leapview_control_backup;
    REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON managed_data.provider_observation_profile, managed_data.provider_version_observation FROM leapview_control_backup;
  END IF;
END $$;
-- +goose StatementEnd
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$ BEGIN
  RAISE EXCEPTION 'managed provider-version observations are immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
