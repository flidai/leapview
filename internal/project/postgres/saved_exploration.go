package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	project "github.com/flidai/leapview/internal/project"
	projectdb "github.com/flidai/leapview/internal/project/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var _ project.SavedExplorationStore = (*Repository)(nil)

func (r *Repository) CreateSavedExploration(ctx context.Context, scope project.SavedExplorationScope, id, title, commandJSON string) (project.SavedExplorationRecord, error) {
	if r == nil || r.db == nil || validateSavedExplorationScope(scope) != nil || uuid.Validate(id) != nil || !validSavedExplorationTitle(title) || !validSavedExplorationCommand(commandJSON) {
		return project.SavedExplorationRecord{}, project.ErrSavedExplorationInvalid
	}
	parsedID, _ := uuid.Parse(id)
	row, err := projectdb.New(r.db).CreateSavedExploration(ctx, projectdb.CreateSavedExplorationParams{
		ID: pgtype.UUID{Bytes: parsedID, Valid: true}, ProjectID: scope.ProjectID.String(), Environment: strings.TrimSpace(scope.Environment),
		PrincipalID: strings.TrimSpace(scope.PrincipalID), Title: title, CommandJson: []byte(commandJSON),
	})
	if err != nil {
		return project.SavedExplorationRecord{}, err
	}
	return project.SavedExplorationRecord{ID: row.ID, Title: row.Title, CommandJSON: row.CommandJson, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}, nil
}

func (r *Repository) ListSavedExplorations(ctx context.Context, scope project.SavedExplorationScope) ([]project.SavedExplorationRecord, error) {
	if r == nil || r.db == nil || validateSavedExplorationScope(scope) != nil {
		return nil, project.ErrSavedExplorationInvalid
	}
	rows, err := projectdb.New(r.db).ListSavedExplorations(ctx, projectdb.ListSavedExplorationsParams{
		ProjectID: scope.ProjectID.String(), Environment: strings.TrimSpace(scope.Environment), PrincipalID: strings.TrimSpace(scope.PrincipalID),
	})
	if err != nil {
		return nil, err
	}
	records := make([]project.SavedExplorationRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, project.SavedExplorationRecord{ID: row.ID, Title: row.Title, CommandJSON: row.CommandJson, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time})
	}
	return records, nil
}

func (r *Repository) GetSavedExploration(ctx context.Context, scope project.SavedExplorationScope, id string) (project.SavedExplorationRecord, error) {
	if r == nil || r.db == nil || validateSavedExplorationScope(scope) != nil || uuid.Validate(id) != nil {
		return project.SavedExplorationRecord{}, project.ErrSavedExplorationInvalid
	}
	parsedID, _ := uuid.Parse(id)
	row, err := projectdb.New(r.db).GetSavedExploration(ctx, projectdb.GetSavedExplorationParams{
		ID: pgtype.UUID{Bytes: parsedID, Valid: true}, ProjectID: scope.ProjectID.String(), Environment: strings.TrimSpace(scope.Environment), PrincipalID: strings.TrimSpace(scope.PrincipalID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return project.SavedExplorationRecord{}, project.ErrSavedExplorationNotFound
	}
	if err != nil {
		return project.SavedExplorationRecord{}, err
	}
	return project.SavedExplorationRecord{ID: row.ID, Title: row.Title, CommandJSON: row.CommandJson, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}, nil
}

func validateSavedExplorationScope(scope project.SavedExplorationScope) error {
	if scope.ProjectID.Validate() != nil || strings.TrimSpace(scope.Environment) == "" || len(scope.Environment) > 255 || strings.TrimSpace(scope.PrincipalID) == "" || len(scope.PrincipalID) > 255 {
		return project.ErrSavedExplorationInvalid
	}
	return nil
}

func validSavedExplorationTitle(title string) bool {
	return title == strings.TrimSpace(title) && len(title) >= 1 && len(title) <= 255
}

func validSavedExplorationCommand(value string) bool {
	if len(value) == 0 || len(value) > 65536 {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal([]byte(value), &object) == nil && object != nil
}
