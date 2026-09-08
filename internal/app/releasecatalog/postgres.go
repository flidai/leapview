// Package releasecatalog contains application-owned adapters that join
// capability authorities at the release module boundary. Keeping these
// wrappers here prevents release/module and release/postgres from importing
// sibling storage packages or selecting a SQL dialect.
package releasecatalog

import (
	"context"
	"strconv"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	connectionbindingpostgres "github.com/flidai/leapview/internal/analytics/connectionbinding/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
	"github.com/flidai/leapview/internal/release"
	releasemodule "github.com/flidai/leapview/internal/release/module"
)

// ProjectAuthority adapts the canonical project identity PostgreSQL
// repository to the release module's capability-neutral catalog contract.
type ProjectAuthority struct{ Repository *projectpostgres.Repository }

func NewProjectAuthority(repository *projectpostgres.Repository) (*ProjectAuthority, error) {
	if repository == nil || !repository.Configured() {
		return nil, ErrRepositoryRequired("project")
	}
	return &ProjectAuthority{Repository: repository}, nil
}

func (*ProjectAuthority) PostgreSQLAuthority() {}
func (a *ProjectAuthority) Configured() bool {
	return a != nil && a.Repository != nil && a.Repository.Configured()
}
func (a *ProjectAuthority) GetProject(ctx context.Context, projectID string) (releasemodule.ProjectIdentityRecord, error) {
	if a == nil || !a.Configured() {
		return releasemodule.ProjectIdentityRecord{}, release.ErrInvalid
	}
	id, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		return releasemodule.ProjectIdentityRecord{}, release.ErrInvalid
	}
	row, err := a.Repository.ByID(ctx, id)
	if err != nil {
		return releasemodule.ProjectIdentityRecord{}, err
	}
	return releasemodule.ProjectIdentityRecord{
		ID: row.ID.String(), Title: row.Title, Description: row.Description,
		CreatedAt: row.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		UpdatedAt: row.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}, nil
}

// ConnectionBindingAuthority adapts canonical target-scoped connection
// bindings. Binding metadata has no authored title/description authority yet;
// the stable connection identity is therefore used as its title.
type ConnectionBindingAuthority struct {
	Repository *connectionbindingpostgres.Repository
}

func NewConnectionBindingAuthority(repository *connectionbindingpostgres.Repository) (*ConnectionBindingAuthority, error) {
	if repository == nil || !repository.Configured() {
		return nil, ErrRepositoryRequired("connection binding")
	}
	return &ConnectionBindingAuthority{Repository: repository}, nil
}

func (*ConnectionBindingAuthority) PostgreSQLAuthority() {}
func (a *ConnectionBindingAuthority) Configured() bool {
	return a != nil && a.Repository != nil && a.Repository.Configured()
}

func (a *ConnectionBindingAuthority) ListConnections(ctx context.Context, projectID, environment, targetID string) ([]releasemodule.ConnectionBindingRecord, error) {
	if a == nil || !a.Configured() {
		return nil, release.ErrInvalid
	}
	id, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		return nil, release.ErrInvalid
	}
	target, err := connectionbinding.ParseTargetID(targetID)
	if err != nil {
		return nil, release.ErrInvalid
	}
	rows, err := a.Repository.List(ctx, connectionbinding.BindingScope{ProjectID: id, Environment: environment}, target)
	if err != nil {
		return nil, err
	}
	out := make([]releasemodule.ConnectionBindingRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, bindingRecord(row))
	}
	return out, nil
}

func (a *ConnectionBindingAuthority) GetConnection(ctx context.Context, projectID, connectionID, environment, targetID string) (releasemodule.ConnectionBindingRecord, error) {
	if a == nil || !a.Configured() {
		return releasemodule.ConnectionBindingRecord{}, release.ErrInvalid
	}
	projectIDValue, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		return releasemodule.ConnectionBindingRecord{}, release.ErrInvalid
	}
	connectionIDValue, err := projectgraph.NewResourceID(connectionID)
	if err != nil {
		return releasemodule.ConnectionBindingRecord{}, release.ErrInvalid
	}
	target, err := connectionbinding.ParseTargetID(targetID)
	if err != nil {
		return releasemodule.ConnectionBindingRecord{}, release.ErrInvalid
	}
	row, err := a.Repository.Binding(ctx, connectionbinding.BindingScope{ProjectID: projectIDValue, Environment: environment}, target, connectionIDValue)
	if err != nil {
		return releasemodule.ConnectionBindingRecord{}, err
	}
	return bindingRecord(row), nil
}

func bindingRecord(row connectionbinding.TargetBinding) releasemodule.ConnectionBindingRecord {
	id := row.ConnectionID.String()
	return releasemodule.ConnectionBindingRecord{ID: id, ConnectionID: id, Title: id, ActiveRevisionID: strconv.FormatInt(row.Revision, 10)}
}

type repositoryRequired string

func (e repositoryRequired) Error() string    { return string(e) + " PostgreSQL repository is required" }
func ErrRepositoryRequired(kind string) error { return repositoryRequired(kind) }

var (
	_ releasemodule.ProjectIdentityAuthority   = (*ProjectAuthority)(nil)
	_ releasemodule.ConnectionBindingAuthority = (*ConnectionBindingAuthority)(nil)
)
