-- +goose Up

-- FAI-596: remove the legacy SQLite dashboard publication relay. Stream
-- registration, heartbeat, and command compare-and-swap state remain in
-- dashboard_publication_streams.
DROP INDEX dashboard_publication_stream_events_stream_idx;
DROP TABLE dashboard_publication_stream_events;

-- +goose Down

CREATE TABLE dashboard_publication_stream_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  stream_id TEXT NOT NULL,
  envelope_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE INDEX dashboard_publication_stream_events_stream_idx
  ON dashboard_publication_stream_events(stream_id, id);
