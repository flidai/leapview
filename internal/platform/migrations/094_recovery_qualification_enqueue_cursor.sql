-- +goose Up

CREATE TABLE recovery_qualification_enqueue_cursor (
  singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
  last_schedule_id TEXT NOT NULL,
  last_schedule_revision_id TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

INSERT INTO recovery_qualification_enqueue_cursor (
  singleton_id, last_schedule_id, last_schedule_revision_id, updated_at
) VALUES (1, '', '', '1970-01-01T00:00:00.000000000Z');

-- +goose Down
DROP TABLE recovery_qualification_enqueue_cursor;
