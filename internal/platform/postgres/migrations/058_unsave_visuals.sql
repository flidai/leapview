-- +goose Up
SET LOCAL ROLE leapview_control_owner;
GRANT DELETE ON dashboard.saved_visuals TO leapview_control_runtime;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
REVOKE DELETE ON dashboard.saved_visuals FROM leapview_control_runtime;
RESET ROLE;
