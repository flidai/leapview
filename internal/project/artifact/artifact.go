// Package artifact owns immutable, versioned project compiler output.
package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/project/manifest"
	refreshartifact "github.com/flidai/leapview/internal/refresh/artifact"
	"github.com/flidai/leapview/internal/workspace"
)

const (
	Version         = 2
	CompilerVersion = "leapview-project-compiler:v2"
)

type UnsupportedVersionError struct {
	Version int
}

func (e UnsupportedVersionError) Error() string {
	return fmt.Sprintf("unsupported project artifact version %d; rebuild and redeploy the project", e.Version)
}

type WorkspaceInput struct {
	Metadata workspace.Workspace
	Manifest *manifest.Workspace
}

type workspaceWire struct {
	Metadata workspace.Workspace `json:"metadata"`
	Manifest manifest.Workspace  `json:"manifest"`
}

type projectWire struct {
	Version    int                      `json:"version"`
	ProjectID  string                   `json:"projectId"`
	Workspaces map[string]workspaceWire `json:"workspaces"`
}

// Project retains canonical bytes rather than mutable domain collections.
// Every projection is decoded into a fresh value before it is returned.
type Project struct {
	projectID    string
	canonical    []byte
	digest       string
	workspace    map[string][]byte
	workspaceIDs []string
}

type Workspace struct {
	canonical []byte
}

func NewProject(projectID string, inputs map[string]WorkspaceInput) (Project, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return Project{}, fmt.Errorf("project artifact id is required")
	}
	wire := projectWire{Version: Version, ProjectID: projectID, Workspaces: make(map[string]workspaceWire, len(inputs))}
	for id, input := range inputs {
		id = strings.TrimSpace(id)
		if id == "" {
			return Project{}, fmt.Errorf("project artifact workspace id is required")
		}
		if input.Manifest == nil {
			return Project{}, fmt.Errorf("project artifact workspace %q manifest is required", id)
		}
		if metadataID := strings.TrimSpace(string(input.Metadata.ID)); metadataID != id {
			return Project{}, fmt.Errorf("project artifact workspace key %q does not match metadata id %q", id, metadataID)
		}
		wire.Workspaces[id] = workspaceWire{Metadata: input.Metadata, Manifest: *input.Manifest}
	}
	return projectFromWire(wire)
}

func Decode(data []byte) (Project, error) {
	var wire projectWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return Project{}, fmt.Errorf("decode project artifact: %w", err)
	}
	if wire.Version != Version {
		return Project{}, UnsupportedVersionError{Version: wire.Version}
	}
	inputs := make(map[string]WorkspaceInput, len(wire.Workspaces))
	for id, item := range wire.Workspaces {
		workspaceManifest := item.Manifest
		inputs[id] = WorkspaceInput{Metadata: item.Metadata, Manifest: &workspaceManifest}
	}
	return NewProject(wire.ProjectID, inputs)
}

func projectFromWire(wire projectWire) (Project, error) {
	canonical, err := json.Marshal(wire)
	if err != nil {
		return Project{}, fmt.Errorf("encode canonical project artifact: %w", err)
	}
	ids := make([]string, 0, len(wire.Workspaces))
	workspaces := make(map[string][]byte, len(wire.Workspaces))
	for id, item := range wire.Workspaces {
		encoded, err := json.Marshal(item)
		if err != nil {
			return Project{}, fmt.Errorf("encode project artifact workspace %q: %w", id, err)
		}
		ids = append(ids, id)
		workspaces[id] = encoded
	}
	sort.Strings(ids)
	sum := sha256.Sum256(canonical)
	return Project{
		projectID: wire.ProjectID, canonical: canonical,
		digest: "sha256:" + hex.EncodeToString(sum[:]), workspace: workspaces, workspaceIDs: ids,
	}, nil
}

func (p Project) Version() int { return Version }

func (p Project) ID() string { return p.projectID }

func (p Project) Digest() string { return p.digest }

func (p Project) Canonical() []byte { return append([]byte(nil), p.canonical...) }

func (p Project) WorkspaceIDs() []string { return append([]string(nil), p.workspaceIDs...) }

func (p Project) Workspace(id string) (Workspace, bool) {
	encoded, ok := p.workspace[strings.TrimSpace(id)]
	if !ok {
		return Workspace{}, false
	}
	return Workspace{canonical: append([]byte(nil), encoded...)}, true
}

func (p Project) MarshalJSON() ([]byte, error) {
	if len(p.canonical) == 0 {
		return nil, fmt.Errorf("project artifact is not initialized")
	}
	return p.Canonical(), nil
}

func (w Workspace) Metadata() workspace.Workspace {
	var decoded workspaceWire
	if err := json.Unmarshal(w.canonical, &decoded); err != nil {
		return workspace.Workspace{}
	}
	return decoded.Metadata
}

func (w Workspace) Manifest() *manifest.Workspace {
	var decoded workspaceWire
	if err := json.Unmarshal(w.canonical, &decoded); err != nil {
		return nil
	}
	return &decoded.Manifest
}

func (w Workspace) Canonical() []byte {
	return append([]byte(nil), w.canonical...)
}

func (w Workspace) Digest() string {
	if len(w.canonical) == 0 {
		return ""
	}
	sum := sha256.Sum256(w.canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DashboardDefinition returns a fresh capability-scoped projection. Mutating
// it cannot change the retained project artifact or any other projection.
func (w Workspace) DashboardDefinition() *dashboarddefinition.Workspace {
	workspaceManifest := w.Manifest()
	if workspaceManifest == nil {
		return nil
	}
	return DashboardProjection(workspaceManifest)
}

// DashboardProjection narrows a mutable project manifest to the data the
// dashboard capability is allowed to consume. The caller retains ownership of
// the supplied manifest; artifact-backed callers should use
// Workspace.DashboardDefinition so the projection starts from a fresh decode.
func DashboardProjection(definition *manifest.Workspace) *dashboarddefinition.Workspace {
	if definition == nil {
		return nil
	}
	catalogView := catalog.Catalog{
		Workspace: catalog.Workspace{
			ID: definition.Catalog.Workspace.ID, Title: definition.Catalog.Workspace.Title,
			Description: definition.Catalog.Workspace.Description,
		},
		Models:     make([]catalog.Model, 0, len(definition.Catalog.SemanticModels)),
		Dashboards: make([]catalog.Dashboard, 0, len(definition.Catalog.Dashboards)),
	}
	for _, model := range definition.Catalog.SemanticModels {
		catalogView.Models = append(catalogView.Models, catalog.Model{
			ID: model.ID, Title: model.Title, Description: model.Description,
		})
	}
	for _, item := range definition.Catalog.Dashboards {
		semanticModel := ""
		pageCount := 0
		if report, ok := definition.DashboardDefinitions[item.ID]; ok {
			semanticModel = report.SemanticModel
			pageCount = len(report.Pages)
		}
		appearanceValue := dashboardappearance.Value{}
		if item.Appearance.Icon != nil {
			appearanceValue.Icon = dashboardappearance.StoredValue(*item.Appearance.Icon)
		}
		if item.Appearance.Color != nil {
			appearanceValue.Color = dashboardappearance.StoredValue(*item.Appearance.Color)
		}
		catalogView.Dashboards = append(catalogView.Dashboards, catalog.Dashboard{
			ID: item.ID, Title: item.Title, Description: item.Description, Tags: append([]string(nil), item.Tags...),
			SemanticModel: semanticModel, PageCount: pageCount,
			Appearance: dashboardappearance.Resolve(appearanceValue),
		})
	}
	return &dashboarddefinition.Workspace{Catalog: catalogView, Models: definition.Models, Dashboards: definition.DashboardDefinitions}
}

// RefreshDefinition returns a fresh capability-scoped projection.
func (w Workspace) RefreshDefinition() *refreshartifact.Definition {
	workspaceManifest := w.Manifest()
	if workspaceManifest == nil {
		return nil
	}
	return RefreshProjection(workspaceManifest)
}

func RefreshProjection(workspaceManifest *manifest.Workspace) *refreshartifact.Definition {
	if workspaceManifest == nil {
		return nil
	}
	return &refreshartifact.Definition{
		Models:    workspaceManifest.Models,
		Pipelines: workspaceManifest.RefreshPipelines,
	}
}
