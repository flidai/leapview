-- +goose Up
PRAGMA foreign_keys = OFF;

CREATE TABLE principal_preferences_new (
  principal_id TEXT PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  theme TEXT NOT NULL DEFAULT 'system' CHECK(theme IN (
    'system',
    'light',
    'dark',
    'dark_dimmed',
    'light_colorblind',
    'dark_colorblind',
    'light_tritanopia',
    'dark_tritanopia'
  )),
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO principal_preferences_new (principal_id, theme, updated_at)
SELECT principal_id, theme, updated_at
FROM principal_preferences;

DROP TABLE principal_preferences;
ALTER TABLE principal_preferences_new RENAME TO principal_preferences;

PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;

CREATE TABLE principal_preferences_old (
  principal_id TEXT PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  theme TEXT NOT NULL DEFAULT 'system' CHECK(theme IN ('system', 'light', 'dark')),
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO principal_preferences_old (principal_id, theme, updated_at)
SELECT principal_id,
  CASE
    WHEN theme IN ('light_colorblind', 'light_tritanopia') THEN 'light'
    WHEN theme IN ('dark_dimmed', 'dark_colorblind', 'dark_tritanopia') THEN 'dark'
    ELSE theme
  END,
  updated_at
FROM principal_preferences;

DROP TABLE principal_preferences;
ALTER TABLE principal_preferences_old RENAME TO principal_preferences;

PRAGMA foreign_keys = ON;
