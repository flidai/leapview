package sqlite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	platformdb "github.com/flidai/leapview/internal/access/internal/db"
)

func (r *Repository) PrincipalPreferences(ctx context.Context, principalID string) (access.PrincipalPreferences, error) {
	row, err := r.q.GetPrincipalPreferences(ctx, principalID)
	if err != nil {
		return access.PrincipalPreferences{}, err
	}
	return principalPreferences(row.PrincipalID, row.Theme, row.UpdatedAt), nil
}

func (r *Repository) SetPrincipalTheme(ctx context.Context, principalID string, theme access.ThemeMode) (access.PrincipalPreferences, error) {
	if parsed, ok := access.ParseThemeMode(string(theme)); !ok || parsed != theme {
		return access.PrincipalPreferences{}, fmt.Errorf("unsupported theme %q", theme)
	}
	row, err := r.q.UpsertPrincipalTheme(ctx, platformdb.UpsertPrincipalThemeParams{PrincipalID: principalID, Theme: string(theme)})
	if err != nil {
		return access.PrincipalPreferences{}, err
	}
	return principalPreferences(row.PrincipalID, row.Theme, row.UpdatedAt), nil
}

func (r *Repository) SetPrincipalThemeAudited(ctx context.Context, principalID string, theme access.ThemeMode) error {
	return r.RunAuditedMutation(ctx, func(repository access.Repository) (access.AuditEventInput, error) {
		writer, ok := any(repository).(access.PrincipalPreferencesWriter)
		if !ok {
			return access.AuditEventInput{}, fmt.Errorf("principal preference writer is unavailable")
		}
		preferences, err := writer.SetPrincipalTheme(ctx, principalID, theme)
		metadata, _ := json.Marshal(map[string]string{"theme": string(theme)})
		return access.AuditEventInput{
			PrincipalID: principalID, Action: "principal.theme.updated", TargetType: "principal",
			TargetID: preferences.PrincipalID, Status: "success", MetadataJSON: string(metadata),
		}, err
	})
}

func principalPreferences(principalID, theme, updatedAt string) access.PrincipalPreferences {
	parsed, ok := access.ParseThemeMode(theme)
	if !ok {
		parsed = access.ThemeSystem
	}
	return access.PrincipalPreferences{PrincipalID: principalID, Theme: parsed, UpdatedAt: updatedAt}
}
