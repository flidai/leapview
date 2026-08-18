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
	"sort"
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
	if err := validateGraphManifest(graph, project); err != nil {
		return Project{}, fmt.Errorf("project artifact: %w", err)
	}

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
	if err := validateGraphManifest(wire.Graph, wire.Manifest); err != nil {
		return Project{}, fmt.Errorf("decode project artifact: %w", err)
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

// validateGraphManifest is the single graph/manifest consistency boundary for
// project artifacts. Resource IDs are exact map keys: names, paths, and
// authoring metadata never provide a compatibility lookup. Access and
// publication declarations remain manifest snapshots, not graph nodes.
func validateGraphManifest(graph projectgraph.ProjectGraph, project manifest.Project) error {
	if err := graph.Validate(); err != nil {
		return fmt.Errorf("project graph: %w", err)
	}
	if strings.TrimSpace(project.ID) == "" {
		return errors.New("project manifest id is required")
	}
	if project.ID != graph.ProjectID().String() {
		return fmt.Errorf("%w: manifest %q, graph %q", projectgraph.ErrProjectIdentityMismatch, project.ID, graph.ProjectID())
	}

	resources := make(map[projectgraph.ResourceID]projectgraph.Resource, len(graph.Resources()))
	for _, resource := range graph.Resources() {
		resources[resource.ID] = resource
	}
	edges := make(map[projectgraph.ResourceID]map[projectgraph.ResourceID]struct{})
	for _, edge := range graph.Edges() {
		if edges[edge.From] == nil {
			edges[edge.From] = make(map[projectgraph.ResourceID]struct{})
		}
		edges[edge.From][edge.To] = struct{}{}
	}
	type projection struct {
		name string
		kind projectgraph.Kind
		ids  []string
	}
	projections := []projection{
		{name: "connections", kind: projectgraph.KindConnection, ids: sortedManifestKeys(project.Connections)},
		{name: "sources", kind: projectgraph.KindSource, ids: sortedManifestKeys(project.Sources)},
		{name: "models", kind: projectgraph.KindModel, ids: sortedManifestKeys(project.Models)},
		{name: "semanticModels", kind: projectgraph.KindSemanticModel, ids: sortedManifestKeys(project.SemanticModels)},
		{name: "refreshPipelines", kind: projectgraph.KindPipeline, ids: sortedManifestKeys(project.RefreshPipelines)},
		{name: "dashboardDefinitions", kind: projectgraph.KindDashboard, ids: sortedManifestKeys(project.DashboardDefinitions)},
		{name: "dashboardSources", kind: projectgraph.KindDashboard, ids: sortedManifestKeys(project.DashboardSources)},
	}
	for _, item := range projections {
		for _, id := range item.ids {
			resource, ok := resources[projectgraph.ResourceID(id)]
			if !ok {
				return fmt.Errorf("manifest %s key %q is missing from graph", item.name, id)
			}
			if resource.Kind != item.kind {
				return fmt.Errorf("manifest %s key %q resolves to graph kind %q, want %q", item.name, id, resource.Kind, item.kind)
			}
		}
	}
	for _, resource := range graph.Resources() {
		if resource.Kind == projectgraph.KindProject {
			continue
		}
		projectionName := manifestProjectionName(resource.Kind)
		if projectionName == "" {
			continue
		}
		if !manifestProjectionContains(project, resource.Kind, resource.ID) {
			return fmt.Errorf("graph resource %q (%s) is absent from manifest %s", resource.ID, resource.Kind, projectionName)
		}
	}
	for id, source := range project.Sources {
		if err := requireManifestReference(resources, edges, "source", id, "connection", source.Connection, projectgraph.KindConnection); err != nil {
			return err
		}
	}
	for id, model := range project.Models {
		for _, reference := range uniqueManifestReferences(model.Source, model.Sources, model.SourceDependencies) {
			if err := requireManifestReference(resources, edges, "model", id, "source", reference, projectgraph.KindSource); err != nil {
				return err
			}
		}
		for _, reference := range uniqueManifestReferences("", model.ModelDependencies, nil) {
			if err := requireManifestReference(resources, edges, "model", id, "model dependency", reference, projectgraph.KindModel); err != nil {
				return err
			}
		}
	}
	for id, pipeline := range project.RefreshPipelines {
		if pipeline.ID.String() != id {
			return fmt.Errorf("manifest refreshPipelines key %q does not match definition id %q", id, pipeline.ID)
		}
		if err := requireManifestReference(resources, edges, "pipeline", id, "semantic model", pipeline.SemanticModelID.String(), projectgraph.KindSemanticModel); err != nil {
			return err
		}
	}
	for id, dashboard := range project.DashboardDefinitions {
		if dashboard.ID != id {
			return fmt.Errorf("manifest dashboardDefinitions key %q does not match definition id %q", id, dashboard.ID)
		}
		if err := requireManifestReference(resources, edges, "dashboard", id, "semantic model", dashboard.SemanticModel, projectgraph.KindSemanticModel); err != nil {
			return err
		}
	}
	for id, source := range project.DashboardSources {
		if source.Document.Metadata.ID != id {
			return fmt.Errorf("manifest dashboardSources key %q does not match document id %q", id, source.Document.Metadata.ID)
		}
		if err := requireManifestReference(resources, edges, "dashboard", id, "semantic model", source.Document.Spec.SemanticModel, projectgraph.KindSemanticModel); err != nil {
			return err
		}
	}
	return nil
}

func requireManifestReference(
	resources map[projectgraph.ResourceID]projectgraph.Resource,
	edges map[projectgraph.ResourceID]map[projectgraph.ResourceID]struct{},
	ownerKind, ownerID, field, reference string,
	wantKind projectgraph.Kind,
) error {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return fmt.Errorf("manifest %s %q requires %s reference", ownerKind, ownerID, field)
	}
	resource, ok := resources[projectgraph.ResourceID(reference)]
	if !ok {
		return fmt.Errorf("manifest %s %q %s reference %q is missing from graph", ownerKind, ownerID, field, reference)
	}
	if resource.Kind != wantKind {
		return fmt.Errorf("manifest %s %q %s reference %q resolves to graph kind %q, want %q", ownerKind, ownerID, field, reference, resource.Kind, wantKind)
	}
	if _, ok := edges[projectgraph.ResourceID(ownerID)][projectgraph.ResourceID(reference)]; !ok {
		return fmt.Errorf("manifest %s %q %s reference %q is missing its graph edge", ownerKind, ownerID, field, reference)
	}
	return nil
}

func uniqueManifestReferences(primary string, values, dependencies []string) []string {
	seen := make(map[string]struct{}, 1+len(values)+len(dependencies))
	for _, reference := range append(append([]string{primary}, values...), dependencies...) {
		reference = strings.TrimSpace(reference)
		if reference != "" {
			seen[reference] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for reference := range seen {
		out = append(out, reference)
	}
	sort.Strings(out)
	return out
}

func sortedManifestKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func manifestProjectionName(kind projectgraph.Kind) string {
	switch kind {
	case projectgraph.KindConnection:
		return "connections"
	case projectgraph.KindSource:
		return "sources"
	case projectgraph.KindModel:
		return "models"
	case projectgraph.KindSemanticModel:
		return "semanticModels"
	case projectgraph.KindPipeline:
		return "refreshPipelines"
	case projectgraph.KindDashboard:
		return "dashboardDefinitions and dashboardSources"
	default:
		return ""
	}
}

func manifestProjectionContains(project manifest.Project, kind projectgraph.Kind, id projectgraph.ResourceID) bool {
	switch kind {
	case projectgraph.KindConnection:
		_, ok := project.Connections[id.String()]
		return ok
	case projectgraph.KindSource:
		_, ok := project.Sources[id.String()]
		return ok
	case projectgraph.KindModel:
		_, ok := project.Models[id.String()]
		return ok
	case projectgraph.KindSemanticModel:
		_, ok := project.SemanticModels[id.String()]
		return ok
	case projectgraph.KindPipeline:
		_, ok := project.RefreshPipelines[id.String()]
		return ok
	case projectgraph.KindDashboard:
		_, definitions := project.DashboardDefinitions[id.String()]
		_, sources := project.DashboardSources[id.String()]
		return definitions && sources
	default:
		return true
	}
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

// RelationExecutionDigests returns per-model-table identities for physical
// reuse. Each digest includes the table descriptor and transitive model-table
// dependency descriptors; callers supply target-scoped pinned-input context
// separately so a changed source revision cannot retain stale files.
func (p Project) RelationExecutionDigests(context string) (map[string]string, error) {
	contexts := make(map[string]string)
	for id := range p.manifest.Models {
		contexts[id] = context
	}
	return p.RelationExecutionDigestsByContext(contexts)
}

// RelationExecutionDigestsByContext is the relation-scoped form of
// RelationExecutionDigests. Each model-table digest receives only its own
// transitive source/binding/pin context; callers can therefore change an
// unrelated source without invalidating untouched physical references.
func (p Project) RelationExecutionDigestsByContext(contexts map[string]string) (map[string]string, error) {
	tables := p.ModelTables()
	resources := make(map[string]string)
	resourceIDsByName := make(map[string]string)
	for _, resource := range p.graph.Resources() {
		if resource.Kind == projectgraph.KindModel {
			resources[resource.ID.String()] = resource.Name
			resourceIDsByName[resource.Name] = resource.ID.String()
		}
	}
	byName := make(map[string]semanticmodel.Table, len(tables))
	for id, table := range tables {
		if name := resources[id]; name != "" {
			byName[name] = table
		}
	}
	var digestTable func(string, map[string]bool) (any, error)
	digestTable = func(name string, visiting map[string]bool) (any, error) {
		table, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("relation dependency %q is missing", name)
		}
		if visiting[name] {
			return nil, fmt.Errorf("relation dependency cycle at %q", name)
		}
		visiting[name] = true
		dependencies := make(map[string]any, len(table.ModelDependencies))
		for _, dependency := range table.ModelDependencies {
			dependencyName := dependency
			if graphName := resources[dependency]; graphName != "" {
				dependencyName = graphName
			}
			value, err := digestTable(dependencyName, visiting)
			if err != nil {
				return nil, err
			}
			dependencies[dependency] = value
		}
		delete(visiting, name)
		return struct {
			Table        semanticmodel.Table `json:"table"`
			Dependencies map[string]any      `json:"dependencies,omitempty"`
			Context      string              `json:"context"`
		}{Table: table, Dependencies: dependencies, Context: contexts[resourceIDsByName[name]]}, nil
	}
	result := make(map[string]string, len(tables))
	for id, name := range resources {
		if _, ok := tables[id]; !ok {
			continue
		}
		value, err := digestTable(name, map[string]bool{})
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode relation %q: %w", id, err)
		}
		sum := sha256.Sum256(encoded)
		result[id] = "sha256:" + hex.EncodeToString(sum[:])
	}
	return result, nil
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
	return &refreshartifact.Definition{Models: cloneValue(value.SemanticModels), Pipelines: cloneValue(value.RefreshPipelines), ConnectionIDs: cloneValue(value.NameIndex.Connections)}
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
