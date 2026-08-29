-- Clean-slate admin product identity authority (ADR-0016).
-- Product identity is mutable through revision-guarded writes; logo bytes are
-- intentionally external to this capability and only their validated metadata
-- is persisted here.

CREATE SCHEMA IF NOT EXISTS admin;

CREATE TABLE IF NOT EXISTS admin.product_identity (
    singleton_id     smallint PRIMARY KEY CHECK (singleton_id = 1),
    display_name     text NOT NULL CHECK (display_name = btrim(display_name)
                                           AND char_length(display_name) BETWEEN 1 AND 120),
    logo_sha256      text,
    logo_media_type  text,
    logo_size_bytes  bigint,
    logo_width       integer,
    logo_height      integer,
    revision         bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (logo_sha256 IS NULL AND logo_media_type IS NULL AND logo_size_bytes IS NULL
           AND logo_width IS NULL AND logo_height IS NULL
        OR logo_sha256 ~ '^[0-9a-f]{64}$'
           AND logo_media_type IN ('image/jpeg', 'image/png', 'image/webp')
           AND logo_size_bytes BETWEEN 1 AND 5242880
           AND logo_width BETWEEN 1 AND 2147483647
           AND logo_height BETWEEN 1 AND 2147483647),
    CHECK ((logo_sha256 IS NULL) = (logo_media_type IS NULL)
           AND (logo_sha256 IS NULL) = (logo_size_bytes IS NULL)
           AND (logo_sha256 IS NULL) = (logo_width IS NULL)
           AND (logo_sha256 IS NULL) = (logo_height IS NULL))
);

INSERT INTO admin.product_identity(singleton_id, display_name)
VALUES (1, 'LeapView')
ON CONFLICT (singleton_id) DO NOTHING;

-- Every product mutation must advance the revision exactly once. This keeps
-- direct SQL writes subject to the same optimistic-concurrency contract as
-- the repository and prevents silent edits that clients cannot observe.
CREATE OR REPLACE FUNCTION admin.guard_product_identity_revision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.singleton_id IS DISTINCT FROM OLD.singleton_id
       OR NEW.revision <> OLD.revision + 1
       OR NEW.updated_at <= OLD.updated_at THEN
        RAISE EXCEPTION 'product identity revision or singleton is invalid';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS product_identity_revision_guard ON admin.product_identity;
CREATE TRIGGER product_identity_revision_guard
    BEFORE UPDATE ON admin.product_identity
    FOR EACH ROW EXECUTE FUNCTION admin.guard_product_identity_revision();

REVOKE ALL ON SCHEMA admin FROM PUBLIC;
REVOKE ALL ON TABLE admin.product_identity FROM PUBLIC;
REVOKE ALL ON FUNCTION admin.guard_product_identity_revision() FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA admin TO leapview_control_runtime;
        GRANT SELECT, UPDATE ON admin.product_identity TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA admin TO leapview_control_readonly;
        GRANT SELECT ON admin.product_identity TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA admin TO leapview_control_backup;
        GRANT SELECT ON admin.product_identity TO leapview_control_backup;
    END IF;
END
$$;
