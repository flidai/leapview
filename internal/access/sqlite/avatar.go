package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/flidai/leapview/internal/access/avatar"
)

func (r *Repository) Avatar(ctx context.Context, principalID string) (avatar.Metadata, error) {
	return scanAvatar(r.db.QueryRowContext(ctx, `
SELECT principal_id, sha256, media_type, size_bytes, width, height, updated_at
FROM principal_avatars
WHERE principal_id = ?`, principalID))
}

func (r *Repository) UpsertAvatar(ctx context.Context, value avatar.Metadata) (avatar.Metadata, error) {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO principal_avatars (principal_id, sha256, media_type, size_bytes, width, height, updated_at)
VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(principal_id) DO UPDATE SET
  sha256 = excluded.sha256,
  media_type = excluded.media_type,
  size_bytes = excluded.size_bytes,
  width = excluded.width,
  height = excluded.height,
  updated_at = CURRENT_TIMESTAMP`,
		value.PrincipalID, value.SHA256, value.MediaType, value.SizeBytes, value.Width, value.Height)
	if err != nil {
		return avatar.Metadata{}, err
	}
	return r.Avatar(ctx, value.PrincipalID)
}

func (r *Repository) DeleteAvatar(ctx context.Context, principalID string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM principal_avatars WHERE principal_id = ?`, principalID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return avatar.ErrNotFound
	}
	return nil
}

type avatarScanner interface{ Scan(...any) error }

func scanAvatar(row avatarScanner) (avatar.Metadata, error) {
	var result avatar.Metadata
	err := row.Scan(&result.PrincipalID, &result.SHA256, &result.MediaType, &result.SizeBytes, &result.Width, &result.Height, &result.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return avatar.Metadata{}, avatar.ErrNotFound
	}
	return result, err
}
