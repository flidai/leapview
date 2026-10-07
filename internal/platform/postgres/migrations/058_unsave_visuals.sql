-- +goose Up
SET LOCAL ROLE leapview_control_owner;
GRANT DELETE ON dashboard.saved_visuals TO leapview_control_runtime;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'saved visual permissions are immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
