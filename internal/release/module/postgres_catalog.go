package module

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// ProjectIdentityRecord is the capability-neutral project projection needed
// by the release catalog. It keeps the release module independent of the
// project PostgreSQL package; application-owned adapters provide concrete
// implementations for production composition.
type ProjectIdentityRecord struct {
	ID, Title, Description string
	CreatedAt, UpdatedAt   string
}

type ProjectIdentityAuthority interface {
	GetProject(context.Context, string) (ProjectIdentityRecord, error)
}

type ConnectionBindingRecord struct {
	ID, ConnectionID, Title, Description, ActiveRevisionID string
}

type ConnectionBindingAuthority interface {
	ListConnections(context.Context, string, string, string) ([]ConnectionBindingRecord, error)
	GetConnection(context.Context, string, string, string, string) (ConnectionBindingRecord, error)
}

type postgresCatalogAuthority interface {
	PostgreSQLAuthority()
	Configured() bool
}

var postgresCatalogTargetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$`)

// PostgresCatalogConfig wires the canonical project identity and
// target-scoped connection-binding authorities. TargetID is the process-bound
// delivery target used to resolve one exact binding scope; it is never taken
// from a browser route.
type PostgresCatalogConfig struct {
	Projects           ProjectIdentityAuthority
	Bindings           ConnectionBindingAuthority
	TargetID           string
	LatestReleaseID    func(context.Context, string) (string, error)
	ActiveDeploymentID func(context.Context, string) (string, error)
}

// PostgresCatalog adapts capability-neutral project and binding authorities
// to release.CatalogRepository. It contains no database handle and imports no
// SQLite, file, or sibling storage package; concrete PostgreSQL wrappers own
// those details in the application-owned releasecatalog adapter package.
type PostgresCatalog struct {
	projects           ProjectIdentityAuthority
	bindings           ConnectionBindingAuthority
	targetID           string
	latestReleaseID    func(context.Context, string) (string, error)
	activeDeploymentID func(context.Context, string) (string, error)
}

func NewPostgresCatalog(config PostgresCatalogConfig) (*PostgresCatalog, error) {
	if config.Projects == nil || config.Bindings == nil {
		return nil, errors.New("PostgreSQL release catalog project and binding authorities are required")
	}
	if authority, ok := config.Projects.(postgresCatalogAuthority); !ok || !authority.Configured() {
		return nil, errors.New("PostgreSQL release catalog project authority is not configured")
	}
	if authority, ok := config.Bindings.(postgresCatalogAuthority); !ok || !authority.Configured() {
		return nil, errors.New("PostgreSQL release catalog binding authority is not configured")
	}
	if config.TargetID == "" || config.TargetID != strings.TrimSpace(config.TargetID) {
		return nil, errors.New("PostgreSQL release catalog target id is required")
	}
	if config.LatestReleaseID == nil || config.ActiveDeploymentID == nil {
		return nil, errors.New("PostgreSQL release catalog latest-release and active-deployment readers are required")
	}
	if !postgresCatalogTargetPattern.MatchString(config.TargetID) {
		return nil, errors.New("PostgreSQL release catalog target id must be canonical")
	}
	return &PostgresCatalog{projects: config.Projects, bindings: config.Bindings, targetID: config.TargetID, latestReleaseID: config.LatestReleaseID, activeDeploymentID: config.ActiveDeploymentID}, nil
}

// NewNativeCatalog is an expressive alias for composition code that already
// distinguishes native capability construction.
func NewNativeCatalog(config PostgresCatalogConfig) (*PostgresCatalog, error) {
	return NewPostgresCatalog(config)
}

func (*PostgresCatalog) PostgreSQLAuthority() {}

func (c *PostgresCatalog) Configured() bool {
	if c == nil || c.projects == nil || c.bindings == nil || c.targetID == "" || c.latestReleaseID == nil || c.activeDeploymentID == nil {
		return false
	}
	projects, projectsOK := c.projects.(postgresCatalogAuthority)
	bindings, bindingsOK := c.bindings.(postgresCatalogAuthority)
	return projectsOK && bindingsOK && projects.Configured() && bindings.Configured()
}

func (c *PostgresCatalog) GetProject(ctx context.Context, projectID string) (release.ProjectRecord, error) {
	if c == nil || !c.Configured() || !validCatalogProject(projectID) {
		return release.ProjectRecord{}, release.ErrInvalid
	}
	row, err := c.projects.GetProject(ctx, projectID)
	if err != nil {
		return release.ProjectRecord{}, err
	}
	if row.ID != projectID {
		return release.ProjectRecord{}, fmt.Errorf("%w: project authority returned mismatched identity", release.ErrConflict)
	}
	result := release.ProjectRecord{ID: row.ID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	result.LatestReleaseID, err = c.latestReleaseID(ctx, projectID)
	if err != nil {
		return release.ProjectRecord{}, err
	}
	result.ActiveDeploymentID, err = c.activeDeploymentID(ctx, projectID)
	if err != nil {
		return release.ProjectRecord{}, err
	}
	return result, nil
}

func (c *PostgresCatalog) ListConnections(ctx context.Context, projectID, environment string) ([]release.ConnectionRecord, error) {
	if c == nil || !c.Configured() || !validCatalogScope(projectID, environment) {
		return nil, release.ErrInvalid
	}
	rows, err := c.bindings.ListConnections(ctx, projectID, environment, c.targetID)
	if err != nil {
		return nil, err
	}
	result := make([]release.ConnectionRecord, 0, len(rows))
	for _, row := range rows {
		mapped, mapErr := mapConnection(row)
		if mapErr != nil {
			return nil, mapErr
		}
		result = append(result, mapped)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (c *PostgresCatalog) GetConnection(ctx context.Context, projectID, connectionID, environment string) (release.ConnectionRecord, error) {
	if c == nil || !c.Configured() || !validCatalogScope(projectID, environment) || !validCatalogProject(connectionID) {
		return release.ConnectionRecord{}, release.ErrInvalid
	}
	row, err := c.bindings.GetConnection(ctx, projectID, connectionID, environment, c.targetID)
	if err != nil {
		return release.ConnectionRecord{}, err
	}
	return mapConnection(row)
}

func mapConnection(row ConnectionBindingRecord) (release.ConnectionRecord, error) {
	id := row.ConnectionID
	if id == "" {
		id = row.ID
	}
	if !validCatalogProject(id) {
		return release.ConnectionRecord{}, fmt.Errorf("%w: connection binding returned invalid identity", release.ErrConflict)
	}
	return release.ConnectionRecord{ID: id, Title: row.Title, Description: row.Description, ActiveRevisionID: row.ActiveRevisionID}, nil
}

func validCatalogProject(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && projectgraph.ResourceID(value).Validate() == nil
}

func validCatalogScope(projectID, environment string) bool {
	return validCatalogProject(projectID) && environment != "" && environment == strings.TrimSpace(environment) && servingstate.ValidateEnvironment(servingstate.Environment(environment)) == nil
}

var _ release.CatalogRepository = (*PostgresCatalog)(nil)
