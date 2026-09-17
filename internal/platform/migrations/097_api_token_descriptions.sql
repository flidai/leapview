-- +goose Up
ALTER TABLE api_tokens
ADD COLUMN description TEXT NOT NULL DEFAULT ''
CHECK(length(description) <= 1024);

-- +goose Down
ALTER TABLE api_tokens DROP COLUMN description;
