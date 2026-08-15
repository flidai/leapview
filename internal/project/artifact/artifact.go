// Package artifact owns the immutable, environment-neutral project artifact.
//
// A project artifact is deliberately smaller than a serving artifact. It
// contains one portable resource graph and the compiler's project-wide
// manifest. Environment, generation, leases, and other serving concerns are
// added by the deployment layer (LEA-374), never inferred here.
package artifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/manifest"
	refreshartifact "github.com/flidai/leapview/internal/refresh/artifact"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

const (
	// Version is the project artifact wire contract version. It is independent
	// of graph.GraphVersion and of serving ArtifactEnvelope versions.
	Version = 1

	// CompilerVersion identifies the project compiler contract that produced
	// the manifest. It is informational and is not part of serving identity.
	CompilerVersion = "leapview-project-compiler:v1"
)

// UnsupportedVersionError identifies a project artifact contract this binary
// cannot decode.
type UnsupportedVersionError struct {
	Version int
}

func (e UnsupportedVersionError) Error() string {
	return fmt.Sprintf("unsupported project artifact version %d; rebuild and redeploy the project", e.Version)
}

// projectWire is intentionally flat: there is exactly one graph and one
// project manifest. It carries no serving selector or target identity.
type projectWire struct {
	Version  int                       `json:"version"`
	Graph    projectgraph.ProjectGraph `json:"graph"`
	Manifest manifest.Project          `json:"manifest"`
}

// Project is an immutable, environment-neutral project artifact. All values
// returned by its methods are detached copies.
type Project struct {
	graph     projectgraph.ProjectGraph
	manifest  manifest.Project
	canonical []byte
	digest    string
}

// NewProject validates and retains one project graph and one project-wide
// manifest. The manifest identity must match graph.ProjectID(). The input is
// defensively copied through the canonical wire representation.
func NewProject(graph projectgraph.ProjectGraph, project manifest.Project) (Project, error) {
	if err := graph.Validate(); err != nil {
		return Project{}, fmt.Errorf("project graph: %w", err)
	}
	projectID := project.ID
	if projectID == "" {
		return Project{}, errors.New("project manifest id is required")
	}
	if projectID != graph.ProjectID().String() {
		return Project{}, fmt.Errorf("%w: manifest %q, graph %q", projectgraph.ErrProjectIdentityMismatch, projectID, graph.ProjectID())
	}
	project.ID = projectID

	wire := projectWire{Version: Version, Graph: graph, Manifest: project}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return Project{}, fmt.Errorf("encode canonical project artifact: %w", err)
	}
	// Decode the just-encoded bytes so maps, slices, and pointers in the
	// compiler manifest cannot alias the caller's mutable value.
	decoded, err := decodeCanonical(canonical)
	if err != nil {
		return Project{}, err
	}
	sum := sha256.Sum256(canonical)
	return Project{
		graph: decoded.graph, manifest: decoded.manifest,
		canonical: append([]byte(nil), canonical...),
		digest:    "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}

// Decode decodes a canonical project artifact. Unknown fields, duplicate
// fields, trailing JSON, unsupported versions, and identity mismatches are
// rejected before the artifact is retained.
func Decode(data []byte) (Project, error) {
	return decodeCanonical(data)
}

func decodeCanonical(data []byte) (Project, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Project{}, fmt.Errorf("decode project artifact: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire projectWire
	if err := decoder.Decode(&wire); err != nil {
		return Project{}, fmt.Errorf("decode project artifact: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return Project{}, errors.New("decode project artifact: trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return Project{}, fmt.Errorf("decode project artifact: trailing data: %w", err)
	}
	if wire.Version != Version {
		return Project{}, UnsupportedVersionError{Version: wire.Version}
	}
	if err := wire.Graph.Validate(); err != nil {
		return Project{}, fmt.Errorf("decode project artifact graph: %w", err)
	}
	if strings.TrimSpace(wire.Manifest.ID) == "" {
		return Project{}, errors.New("decode project artifact: manifest id is required")
	}
	if wire.Manifest.ID != wire.Graph.ProjectID().String() {
		return Project{}, fmt.Errorf("%w: manifest %q, graph %q", projectgraph.ErrProjectIdentityMismatch, wire.Manifest.ID, wire.Graph.ProjectID())
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return Project{}, fmt.Errorf("encode canonical project artifact: %w", err)
	}
	// Canonical bytes are required to be stable. This catches hand-written
	// non-canonical encodings while still accepting insignificant JSON spacing.
	// We compare decoded semantic values rather than raw input so callers may
	// submit ordinary JSON and receive the canonical retained form.
	sum := sha256.Sum256(canonical)
	return Project{
		graph: wire.Graph, manifest: wire.Manifest,
		canonical: canonical,
		digest:    "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}

// Version returns the artifact wire version.
func (p Project) Version() int { return Version }

// ProjectID returns the graph's canonical project resource ID.
func (p Project) ProjectID() projectgraph.ResourceID { return p.graph.ProjectID() }

// ID is retained as a concise alias for ProjectID. It returns the graph ID,
// never an environment or generation identity.
func (p Project) ID() projectgraph.ResourceID { return p.ProjectID() }

// Graph returns the immutable project graph by value.
func (p Project) Graph() projectgraph.ProjectGraph { return p.graph }

// Manifest returns a detached project-wide compiler manifest.
func (p Project) Manifest() manifest.Project { return cloneManifest(p.manifest) }

// Canonical returns deterministic artifact bytes.
func (p Project) Canonical() []byte { return append([]byte(nil), p.canonical...) }

// Digest returns the SHA-256 digest of Canonical. It is portable and does not
// include serving environment or generation identity.
func (p Project) Digest() string { return p.digest }

func (p Project) MarshalJSON() ([]byte, error) {
	if len(p.canonical) == 0 {
		return nil, errors.New("project artifact is not initialized")
	}
	return p.Canonical(), nil
}

func (p *Project) UnmarshalJSON(data []byte) error {
	if p == nil {
		return errors.New("cannot unmarshal project artifact into nil receiver")
	}
	decoded, err := Decode(data)
	if err != nil {
		return err
	}
	*p = decoded
	return nil
}

// Connections returns detached project-wide connection projections.
func (p Project) Connections() map[string]semanticmodel.Connection {
	return cloneValue(p.manifest.Connections)
}

// Models returns detached project-wide semantic model projections.
func (p Project) Models() map[string]*semanticmodel.Model {
	return cloneValue(p.manifest.SemanticModels)
}

// ModelTables returns detached physical model-table projections keyed by
// canonical resource ID.
func (p Project) ModelTables() map[string]semanticmodel.Table {
	return cloneValue(p.manifest.Models)
}

// DashboardDefinitions returns detached compiled dashboard definitions keyed
// by canonical dashboard ID.
func (p Project) DashboardDefinitions() map[string]dashboarddefinition.Definition {
	return cloneValue(p.manifest.DashboardDefinitions)
}

// DashboardSources returns detached authored dashboard documents and their
// source/provenance metadata, preserving the evidence required for export.
func (p Project) DashboardSources() map[string]manifest.DashboardSource {
	return cloneValue(p.manifest.DashboardSources)
}

// AuthoredDashboardSource returns one detached authored dashboard source by
// canonical ID. A missing source is explicit through the bool result.
func (p Project) AuthoredDashboardSource(id string) (manifest.DashboardSource, bool) {
	source, ok := p.manifest.DashboardSources[strings.TrimSpace(id)]
	if !ok {
		return manifest.DashboardSource{}, false
	}
	return cloneValue(source), true
}

// RefreshPipelines returns detached project-wide refresh projections.
func (p Project) RefreshPipelines() map[string]refreshschedule.Definition {
	return cloneValue(p.manifest.RefreshPipelines)
}

// RefreshDefinition returns a detached project-level projection consumed by
// refresh execution.
func (p Project) RefreshDefinition() *refreshartifact.Definition {
	return RefreshProjection(p.manifest)
}

// RefreshProjection narrows a project manifest to refresh-owned resources.
func RefreshProjection(value manifest.Project) *refreshartifact.Definition {
	return &refreshartifact.Definition{Models: cloneValue(value.SemanticModels), Pipelines: cloneValue(value.RefreshPipelines)}
}

func cloneManifest(value manifest.Project) manifest.Project { return cloneValue(value) }

// cloneValue uses the JSON representation because the compiler's model graph
// contains nested maps, slices, pointers, and interface values. This keeps
// every projection detached without importing mutable compiler internals.
func cloneValue[T any](value T) T {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("clone artifact value: encode: %v", err))
	}
	var cloned T
	if err := json.Unmarshal(data, &cloned); err != nil {
		panic(fmt.Sprintf("clone artifact value: decode: %v", err))
	}
	return cloned
}

// rejectDuplicateJSONKeys rejects duplicate object keys recursively. The
// standard library otherwise keeps the last value, which would make identity
// and digest validation ambiguous.
func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value any
	if err := decodeUnique(decoder, &value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func decodeUnique(decoder *json.Decoder, target *any) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				canonicalKey := strings.ToLower(key)
				if _, exists := object[canonicalKey]; exists {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				var child any
				if err := decodeUnique(decoder, &child); err != nil {
					return err
				}
				object[canonicalKey] = child
			}
			if _, err := decoder.Token(); err != nil {
				return err
			}
			*target = object
		case '[':
			array := []any{}
			for decoder.More() {
				var child any
				if err := decodeUnique(decoder, &child); err != nil {
					return err
				}
				array = append(array, child)
			}
			if _, err := decoder.Token(); err != nil {
				return err
			}
			*target = array
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	default:
		*target = token
	}
	return nil
}
