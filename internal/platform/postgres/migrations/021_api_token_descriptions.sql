-- +goose Up
SET LOCAL ROLE leapview_control_owner;

ALTER TABLE access.api_token
ADD COLUMN description text NOT NULL DEFAULT ''
CHECK (description = btrim(description) AND length(description) <= 1024);

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;

ALTER TABLE access.api_token DROP COLUMN description;

RESET ROLE;
