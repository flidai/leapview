package publication

import (
	"encoding/json"
	"fmt"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// Projection is the backend-neutral publication row shape. PostgreSQL and
// SQLite adapters normalize nullable timestamps and IDs into strings before
// calling MapProjection; SQL query and transaction details stay backend-owned.
type Projection struct {
	ID                     string
	ProjectID              string
	Name                   string
	PublicID               string
	Dashboard              string
	DefaultPage            string
	ConfigurationDigest    string
	AllowedOriginsJSON     string
	DependencyAssetIDsJSON string
	Revision               int64
	Configured             bool
	ServingStateID         string
	SuspendedAt            string
	SuspendedBy            string
	ConfiguredAt           string
	DisabledAt             string
	RotatedAt              string
	CreatedAt              string
	UpdatedAt              string
}

// MapProjection converts either persistence backend's normalized row into the
// canonical publication model and preserves identical validation/error rules.
func MapProjection(row Projection) (Publication, error) {
	projectID, err := projectgraph.NewResourceID(strings.TrimSpace(row.ProjectID))
	if err != nil {
		return Publication{}, fmt.Errorf("decode publication project ID: %w", err)
	}
	out := Publication{
		ID: row.ID, ProjectID: projectID, Name: row.Name,
		PublicID: row.PublicID, Dashboard: row.Dashboard, DefaultPage: row.DefaultPage,
		ConfigurationDigest: row.ConfigurationDigest, Configured: row.Configured,
		Revision: row.Revision, ServingStateID: strings.TrimSpace(row.ServingStateID),
		SuspendedAt: row.SuspendedAt, SuspendedBy: row.SuspendedBy,
		ConfiguredAt: row.ConfiguredAt, DisabledAt: row.DisabledAt, RotatedAt: row.RotatedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if err := json.Unmarshal([]byte(row.AllowedOriginsJSON), &out.AllowedOrigins); err != nil {
		return Publication{}, fmt.Errorf("decode publication origins: %w", err)
	}
	if err := json.Unmarshal([]byte(row.DependencyAssetIDsJSON), &out.DependencyAssetIDs); err != nil {
		return Publication{}, fmt.Errorf("decode publication dependencies: %w", err)
	}
	return out, nil
}
