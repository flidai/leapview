-- Native PostgreSQL dashboard publication and public stream authority.
CREATE SCHEMA IF NOT EXISTS dashboard;

CREATE TABLE IF NOT EXISTS dashboard.publications (
 id uuid PRIMARY KEY,
 project_id text NOT NULL,
 name text NOT NULL,
 public_id text NOT NULL UNIQUE,
 dashboard text NOT NULL,
 default_page text NOT NULL,
 configuration_digest text NOT NULL CHECK (configuration_digest ~ '^sha256:[0-9a-f]{64}$'),
 allowed_origins_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(allowed_origins_json) = 'array' AND octet_length(allowed_origins_json::text) <= 32768),
 dependency_asset_ids_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(dependency_asset_ids_json) = 'array' AND octet_length(dependency_asset_ids_json::text) <= 32768),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
 configured boolean NOT NULL DEFAULT true,
 active_serving_state_id text,
 suspended_at timestamptz,
 suspended_by text NOT NULL DEFAULT '',
 configured_at timestamptz,
 disabled_at timestamptz,
 rotated_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(project_id,name),
 CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
 CHECK (name = btrim(name) AND octet_length(name) BETWEEN 1 AND 255),
 CHECK (public_id = btrim(public_id) AND octet_length(public_id) BETWEEN 1 AND 255),
 CHECK (dashboard = btrim(dashboard) AND octet_length(dashboard) BETWEEN 1 AND 512),
 CHECK (default_page = btrim(default_page) AND octet_length(default_page) BETWEEN 1 AND 255),
 CHECK (suspended_by = btrim(suspended_by) AND octet_length(suspended_by) <= 255),
 CHECK (active_serving_state_id IS NULL OR (active_serving_state_id = btrim(active_serving_state_id) AND octet_length(active_serving_state_id) BETWEEN 1 AND 255)),
 CHECK (updated_at >= created_at),
 CHECK ((configured AND active_serving_state_id IS NOT NULL AND configured_at IS NOT NULL AND disabled_at IS NULL)
     OR (NOT configured AND active_serving_state_id IS NULL AND disabled_at IS NOT NULL)),
 CHECK ((suspended_at IS NULL AND suspended_by = '') OR (suspended_at IS NOT NULL AND suspended_by <> ''))
);

CREATE TABLE IF NOT EXISTS dashboard.publication_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 publication_id uuid NOT NULL REFERENCES dashboard.publications(id) ON DELETE RESTRICT,
 domain_event_id uuid NOT NULL,
 aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
 revision bigint NOT NULL CHECK (revision > 0),
 event_type text NOT NULL CHECK (event_type IN ('dashboard_publication.configured','dashboard_publication.configuration_changed','dashboard_publication.serving_state_changed','dashboard_publication.disabled','dashboard_publication.suspended','dashboard_publication.resumed','dashboard_publication.rotated')),
 actor_id text NOT NULL DEFAULT '' CHECK (actor_id = btrim(actor_id) AND octet_length(actor_id) <= 255),
 correlation_id text NOT NULL DEFAULT '' CHECK (correlation_id = btrim(correlation_id) AND octet_length(correlation_id) <= 255),
 serving_state_id text CHECK (serving_state_id IS NULL OR (serving_state_id = btrim(serving_state_id) AND octet_length(serving_state_id) BETWEEN 1 AND 255)),
 payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object' AND octet_length(payload_json::text) <= 65536),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE (domain_event_id),
 UNIQUE (publication_id, aggregate_version),
 UNIQUE (publication_id, revision)
);
CREATE INDEX IF NOT EXISTS publication_events_publication_idx ON dashboard.publication_events(publication_id,id DESC);
CREATE INDEX IF NOT EXISTS publication_events_publication_revision_idx ON dashboard.publication_events(publication_id,revision DESC);

CREATE TABLE IF NOT EXISTS dashboard.publication_streams (
 publication_id uuid NOT NULL,
 stream_id text NOT NULL,
 public_id text NOT NULL,
 serving_state_id text NOT NULL,
 registration_id uuid NOT NULL,
 filters_json jsonb NOT NULL CHECK (jsonb_typeof(filters_json) = 'object' AND octet_length(filters_json::text) <= 32768),
 generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
 expires_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(publication_id,stream_id),
 FOREIGN KEY (publication_id) REFERENCES dashboard.publications(id) ON DELETE RESTRICT,
 CHECK (stream_id = btrim(stream_id) AND octet_length(stream_id) BETWEEN 1 AND 255),
 CHECK (public_id = btrim(public_id) AND octet_length(public_id) BETWEEN 1 AND 255),
 CHECK (serving_state_id = btrim(serving_state_id) AND octet_length(serving_state_id) BETWEEN 1 AND 255)
);

-- Publication identity is stable for the lifetime of a row.  Every
-- projection mutation advances its optimistic revision exactly once; public
-- identifiers may only rotate together with their rotation timestamp.
CREATE OR REPLACE FUNCTION dashboard.guard_publication_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.id IS DISTINCT FROM NEW.id
       OR OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.name IS DISTINCT FROM NEW.name
       OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'publication identity is immutable';
    END IF;
    IF NEW.revision <> OLD.revision + 1 THEN
        RAISE EXCEPTION 'publication revision must advance exactly one';
    END IF;
    IF OLD.public_id IS DISTINCT FROM NEW.public_id
       AND OLD.rotated_at IS NOT DISTINCT FROM NEW.rotated_at THEN
        RAISE EXCEPTION 'publication public_id changes require rotated_at change';
    END IF;
    IF OLD.rotated_at IS DISTINCT FROM NEW.rotated_at
       AND OLD.public_id IS NOT DISTINCT FROM NEW.public_id THEN
        RAISE EXCEPTION 'publication rotated_at changes require public_id change';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'publication updated_at is monotonic';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION dashboard.guard_publication_stream_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.publication_id IS DISTINCT FROM NEW.publication_id
       OR OLD.stream_id IS DISTINCT FROM NEW.stream_id THEN
        RAISE EXCEPTION 'publication stream primary key is immutable';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'publication stream updated_at is monotonic';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS publications_mutation ON dashboard.publications;
CREATE TRIGGER publications_mutation
    BEFORE UPDATE ON dashboard.publications
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_publication_update();
DROP TRIGGER IF EXISTS publication_streams_mutation ON dashboard.publication_streams;
CREATE TRIGGER publication_streams_mutation
    BEFORE UPDATE ON dashboard.publication_streams
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_publication_stream_update();


DO $$
BEGIN
    IF to_regclass('project.project_identity') IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'publications_project_fk') THEN
        ALTER TABLE dashboard.publications
            ADD CONSTRAINT publications_project_fk FOREIGN KEY (project_id)
            REFERENCES project.project_identity(project_id) ON DELETE RESTRICT;
    END IF;
END $$;

REVOKE ALL ON SCHEMA dashboard FROM PUBLIC;
REVOKE ALL ON TABLE dashboard.publications,dashboard.publication_events,dashboard.publication_streams FROM PUBLIC;
REVOKE ALL ON SEQUENCE dashboard.publication_events_id_seq FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.guard_publication_update() FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.guard_publication_stream_update() FROM PUBLIC;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_owner;
  GRANT ALL ON TABLE dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_owner;
  GRANT ALL ON SEQUENCE dashboard.publication_events_id_seq TO leapview_control_owner;
  GRANT ALL ON FUNCTION dashboard.guard_publication_update(), dashboard.guard_publication_stream_update() TO leapview_control_owner;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_migrator') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_migrator;
  GRANT ALL ON TABLE dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_migrator;
  GRANT ALL ON SEQUENCE dashboard.publication_events_id_seq TO leapview_control_migrator;
  GRANT ALL ON FUNCTION dashboard.guard_publication_update(), dashboard.guard_publication_stream_update() TO leapview_control_migrator;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_runtime;
  GRANT SELECT ON dashboard.publications,dashboard.publication_streams TO leapview_control_runtime;
  GRANT INSERT (id,project_id,name,public_id,dashboard,default_page,configuration_digest,allowed_origins_json,dependency_asset_ids_json,active_serving_state_id,configured_at)
      ON dashboard.publications TO leapview_control_runtime;
  GRANT UPDATE (public_id,dashboard,default_page,configuration_digest,allowed_origins_json,dependency_asset_ids_json,revision,configured,active_serving_state_id,suspended_at,suspended_by,configured_at,disabled_at,rotated_at,updated_at)
      ON dashboard.publications TO leapview_control_runtime;
  GRANT INSERT (publication_id,stream_id,public_id,serving_state_id,registration_id,filters_json,expires_at)
      ON dashboard.publication_streams TO leapview_control_runtime;
  GRANT UPDATE (public_id,serving_state_id,registration_id,filters_json,generation,expires_at,updated_at)
      ON dashboard.publication_streams TO leapview_control_runtime;
  GRANT SELECT,INSERT ON dashboard.publication_events TO leapview_control_runtime;
  GRANT USAGE ON SEQUENCE dashboard.publication_events_id_seq TO leapview_control_runtime;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_maintenance') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_maintenance;
  GRANT SELECT,DELETE ON dashboard.publication_streams TO leapview_control_maintenance;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_readonly;
  GRANT SELECT ON dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_readonly;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_backup;
  GRANT SELECT ON dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_backup;
 END IF;
END $$;
