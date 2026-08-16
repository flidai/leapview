-- +goose Up

CREATE TABLE dashboard_view_days (
  project_id TEXT NOT NULL,
  dashboard_id TEXT NOT NULL,
  principal_id TEXT NOT NULL,
  viewed_on TEXT NOT NULL,
  page_id TEXT NOT NULL,
  first_viewed_at TEXT NOT NULL,
  last_viewed_at TEXT NOT NULL,
  PRIMARY KEY (project_id, dashboard_id, principal_id, viewed_on)
);

CREATE INDEX dashboard_view_days_recent_idx
  ON dashboard_view_days(last_viewed_at DESC, project_id, dashboard_id);

-- +goose Down

DROP INDEX dashboard_view_days_recent_idx;
DROP TABLE dashboard_view_days;
