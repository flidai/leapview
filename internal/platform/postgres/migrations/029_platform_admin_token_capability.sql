-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- API token scopes include a distinct attenuation for durable platform
-- administration. The scope never grants the durable role by itself; the
-- request authorizer still requires access.platform_role_bindings.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.valid_capabilities(value jsonb) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE item text; seen_items jsonb := '[]'::jsonb;
BEGIN
    IF value IS NULL THEN RETURN TRUE; END IF;
    IF jsonb_typeof(value) <> 'array' THEN RETURN FALSE; END IF;
    FOR item IN SELECT jsonb_array_elements_text(value) LOOP
        IF item NOT IN ('PLATFORM_ADMIN','PROJECT_ADMIN','RESOURCE_USE','RESOURCE_READ','RESOURCE_EDIT','RESOURCE_MANAGE','RESOURCE_SHARE','RESOURCE_PUBLISH') THEN RETURN FALSE; END IF;
        IF seen_items ? item THEN RETURN FALSE; END IF;
        seen_items := seen_items || to_jsonb(item);
    END LOOP;
    RETURN TRUE;
END $$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SELECT 1;
