// Package graph defines the portable, project-wide resource graph contract.
//
// A graph is an immutable value after construction. Resource IDs and symbolic
// names are deliberately separate: IDs are explicit, opaque, and stable while
// names are a convenient project-local reference. Authoring metadata (including
// paths and provenance) is retained in the graph artifact, but is never used
// to derive an ID or to resolve an edge.
package graph

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const (
	// GraphVersion is the portable project graph format version.
	GraphVersion = 1
	// ArtifactVersion is the serving-scoped envelope format version.
	ArtifactVersion = 1

	digestPrefix = "sha256:"
)

// Resource IDs are opaque references. The grammar intentionally accepts
// generated UUID/ULID values and namespaced IDs such as semantic_model:orders,
// while excluding path separators and whitespace.
var resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

// Symbolic names are project-local readable references, deliberately stricter
// than opaque IDs so IDs and names cannot be confused at the API boundary.
var resourceNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

var (
	// ErrInvalidResourceID indicates an empty or malformed canonical ID.
	ErrInvalidResourceID = errors.New("invalid resource id")
	// ErrInvalidKind indicates a kind outside the project graph contract.
	ErrInvalidKind = errors.New("invalid resource kind")
	// ErrInvalidName indicates an empty or malformed symbolic project name.
	ErrInvalidName = errors.New("invalid resource name")
	// ErrDuplicateResourceID indicates that two resources have the same ID.
	ErrDuplicateResourceID = errors.New("duplicate resource id")
	// ErrDuplicateName indicates that two resources have the same symbolic name.
	ErrDuplicateName = errors.New("duplicate resource name")
	// ErrMissingEndpoint indicates an edge endpoint that is not in the graph.
	ErrMissingEndpoint = errors.New("missing edge endpoint")
	// ErrDuplicateEdge indicates that an edge is repeated.
	ErrDuplicateEdge = errors.New("duplicate edge")
	// ErrCycle indicates that the dependency graph is cyclic.
	ErrCycle = errors.New("resource graph contains a cycle")
	// ErrProjectRoot indicates that the graph does not have exactly one project
	// root resource.
	ErrProjectRoot = errors.New("project graph must contain exactly one project root")
	// ErrProjectIdentityMismatch indicates that an artifact identity does not
	// match the project root in its graph.
	ErrProjectIdentityMismatch = errors.New("project identity does not match graph root")
	// ErrInvalidServingIdentity indicates a malformed serving scope.
	ErrInvalidServingIdentity = errors.New("invalid serving identity")
)

// UnsupportedVersionError reports a graph or serving artifact envelope whose
// canonical format version is not understood by this package.
type UnsupportedVersionError struct {
	Contract string
	Version  int
}

func (e UnsupportedVersionError) Error() string {
	return fmt.Sprintf("unsupported %s version %d", e.Contract, e.Version)
}

// ResourceID is an explicit, stable project resource identity. It is not
// generated from a path, display name, domain, owner, or authoring origin.
// IDs are opaque and may be digit-first, UUID/ULID-shaped, or namespaced with
// a colon; callers must not infer symbolic meaning from their spelling.
type ResourceID string

// NewResourceID validates an explicit resource ID.
func NewResourceID(value string) (ResourceID, error) {
	if !resourceIDPattern.MatchString(value) {
		return "", fmt.Errorf("%w %q", ErrInvalidResourceID, value)
	}
	return ResourceID(value), nil
}

// String returns the textual form of the ID.
func (id ResourceID) String() string { return string(id) }

// Validate checks the canonical resource ID. It mirrors the validation
// methods exposed by domain-specific identity wrappers while keeping the
// graph ResourceID itself the single identity type.
func (id ResourceID) Validate() error {
	_, err := NewResourceID(id.String())
	return err
}

// Valid reports whether id is a valid canonical resource ID.
func (id ResourceID) Valid() bool { return resourceIDPattern.MatchString(string(id)) }

// Kind is a first-class project graph resource kind.
type Kind string

const (
	KindProject       Kind = "project"
	KindConnection    Kind = "connection"
	KindSource        Kind = "source"
	KindModel         Kind = "model"
	KindSemanticModel Kind = "semantic_model"
	KindPipeline      Kind = "pipeline"
	KindDashboard     Kind = "dashboard"
)

var validKinds = map[Kind]struct{}{
	KindProject: {}, KindConnection: {}, KindSource: {}, KindModel: {},
	KindSemanticModel: {}, KindPipeline: {}, KindDashboard: {},
}

// Valid reports whether kind belongs to the project graph contract.
func (kind Kind) Valid() bool {
	_, ok := validKinds[kind]
	return ok
}

// ParseKind validates a wire kind.
func ParseKind(value string) (Kind, error) {
	kind := Kind(value)
	if !kind.Valid() {
		return "", fmt.Errorf("%w %q", ErrInvalidKind, value)
	}
	return kind, nil
}

// Metadata is descriptive resource metadata. Domain is intentionally only
// metadata: it is not a namespace, access boundary, or identity component.
type Metadata struct {
	DisplayName   string   `json:"displayName,omitempty"`
	Description   string   `json:"description,omitempty"`
	Owner         string   `json:"owner,omitempty"`
	Domain        string   `json:"domain,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Documentation string   `json:"documentation,omitempty"`
}

// Provenance describes where a resource was authored or imported. It has no
// bearing on resource identity or graph topology.
type Provenance struct {
	Origin string `json:"origin,omitempty"`
	Path   string `json:"path,omitempty"`
	Source string `json:"source,omitempty"`
}

// Resource is a project-wide graph node. ID is explicit and immutable by
// convention; callers receive defensive copies from ProjectGraph methods.
type Resource struct {
	ID         ResourceID `json:"id"`
	Kind       Kind       `json:"kind"`
	Name       string     `json:"name"`
	Metadata   Metadata   `json:"metadata,omitempty"`
	Provenance Provenance `json:"provenance,omitempty"`
}

// Edge is a directed dependency from From to To. Relation is optional
// descriptive data; endpoint identity alone defines graph topology.
type Edge struct {
	From     ResourceID `json:"from"`
	To       ResourceID `json:"to"`
	Relation string     `json:"relation,omitempty"`
}

// ProjectGraph is an immutable, validated project resource graph. It contains
// exactly one project root resource; that root supplies ProjectID. The graph
// bytes are portable and do not carry serving environment or generation data.
type ProjectGraph struct {
	projectID ResourceID
	resources []Resource
	edges     []Edge
	canonical []byte
	digest    string
}

// NewProjectGraph validates resources and edges, then returns an immutable
// project graph.
func NewProjectGraph(resources []Resource, edges []Edge) (ProjectGraph, error) {
	resourcesCopy := cloneResources(resources)
	edgesCopy := cloneEdges(edges)
	if edgesCopy == nil {
		edgesCopy = []Edge{}
	}
	if err := validate(resourcesCopy, edgesCopy); err != nil {
		return ProjectGraph{}, err
	}
	projectID, err := projectRootID(resourcesCopy)
	if err != nil {
		return ProjectGraph{}, err
	}
	sortResources(resourcesCopy)
	sortEdges(edgesCopy)

	wire := graphWire{Version: GraphVersion, Resources: resourcesCopy, Edges: edgesCopy}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return ProjectGraph{}, fmt.Errorf("encode canonical project graph: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return ProjectGraph{
		projectID: projectID,
		resources: resourcesCopy,
		edges:     edgesCopy,
		canonical: canonical,
		digest:    digestPrefix + hex.EncodeToString(sum[:]),
	}, nil
}

// Validate validates a resource and edge collection without constructing a
// graph. It is useful to validate decoded or incrementally assembled inputs.
func Validate(resources []Resource, edges []Edge) error {
	resourcesCopy := cloneResources(resources)
	if err := validate(resourcesCopy, cloneEdges(edges)); err != nil {
		return err
	}
	_, err := projectRootID(resourcesCopy)
	return err
}

// Decode decodes a canonical graph artifact and revalidates its invariants.
func Decode(data []byte) (ProjectGraph, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return ProjectGraph{}, fmt.Errorf("decode project graph: %w", err)
	}
	var wire graphWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return ProjectGraph{}, fmt.Errorf("decode project graph: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return ProjectGraph{}, errors.New("decode project graph: trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return ProjectGraph{}, fmt.Errorf("decode project graph: trailing data: %w", err)
	}
	if wire.Version != GraphVersion {
		return ProjectGraph{}, UnsupportedVersionError{Contract: "project graph", Version: wire.Version}
	}
	return NewProjectGraph(wire.Resources, wire.Edges)
}

// Resources returns a defensive copy sorted by canonical ID.
func (g ProjectGraph) Resources() []Resource { return cloneResources(g.resources) }

// Edges returns a defensive copy sorted by canonical endpoint order.
func (g ProjectGraph) Edges() []Edge { return cloneEdges(g.edges) }

// ProjectID returns the stable ID of the graph's project root resource.
func (g ProjectGraph) ProjectID() ResourceID { return g.projectID }

// Resource returns a defensive copy of the resource with id.
func (g ProjectGraph) Resource(id ResourceID) (Resource, bool) {
	for _, resource := range g.resources {
		if resource.ID == id {
			return cloneResource(resource), true
		}
	}
	return Resource{}, false
}

// CanonicalBytes returns the deterministic portable graph artifact.
func (g ProjectGraph) CanonicalBytes() []byte { return append([]byte(nil), g.canonical...) }

// Digest returns the SHA-256 digest of CanonicalBytes in the project's
// canonical digest form ("sha256:" followed by lowercase hexadecimal).
func (g ProjectGraph) Digest() string { return g.digest }

// MarshalJSON emits the canonical artifact bytes.
func (g ProjectGraph) MarshalJSON() ([]byte, error) {
	if len(g.canonical) == 0 {
		return nil, errors.New("project graph is not initialized")
	}
	return g.CanonicalBytes(), nil
}

// UnmarshalJSON decodes and validates a graph artifact.
func (g *ProjectGraph) UnmarshalJSON(data []byte) error {
	if g == nil {
		return errors.New("cannot unmarshal project graph into nil receiver")
	}
	decoded, err := Decode(data)
	if err != nil {
		return err
	}
	*g = decoded
	return nil
}

// Validate checks an already-constructed graph. NewProjectGraph always returns a
// validated graph; this method is useful for zero-value checks in callers.
func (g ProjectGraph) Validate() error {
	if len(g.canonical) == 0 {
		return errors.New("project graph is not initialized")
	}
	if err := validate(g.resources, g.edges); err != nil {
		return err
	}
	_, err := projectRootID(g.resources)
	return err
}

// ServingIdentity binds one portable project graph to the immutable serving
// scope that owns it. Environment and GenerationID are explicit values; they
// are never inferred from a legacy container, path, or graph metadata.
type ServingIdentity struct {
	ProjectID    ResourceID `json:"projectId"`
	Environment  string     `json:"environment"`
	GenerationID string     `json:"generationId"`
}

// CandidateScope is the canonical serving scope used while preparing a
// private candidate. BaseGenerationID is optional for a first deployment;
// an empty value means there is no base generation, never a fabricated ID.
type CandidateScope struct {
	ProjectID        ResourceID `json:"projectId"`
	Environment      string     `json:"environment"`
	BaseGenerationID string     `json:"baseGenerationId,omitempty"`
}

func (scope CandidateScope) Validate() error {
	if scope.ProjectID.Validate() != nil || scope.ProjectID.String() != strings.TrimSpace(scope.ProjectID.String()) {
		return fmt.Errorf("%w: candidate project id is invalid", ErrInvalidServingIdentity)
	}
	if scope.Environment == "" || scope.Environment != strings.TrimSpace(scope.Environment) || !resourceIDPattern.MatchString(scope.Environment) {
		return fmt.Errorf("%w: candidate environment is invalid", ErrInvalidServingIdentity)
	}
	if scope.BaseGenerationID != "" && (scope.BaseGenerationID != strings.TrimSpace(scope.BaseGenerationID) || !resourceIDPattern.MatchString(scope.BaseGenerationID)) {
		return fmt.Errorf("%w: candidate base generation is invalid", ErrInvalidServingIdentity)
	}
	return nil
}

func (scope CandidateScope) BaseIdentity() (*ServingIdentity, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if scope.BaseGenerationID == "" {
		return nil, nil
	}
	identity, err := NewServingIdentity(scope.ProjectID, scope.Environment, scope.BaseGenerationID)
	if err != nil {
		return nil, err
	}
	return &identity, nil
}

// NewServingIdentity validates the complete immutable serving scope without
// requiring a graph payload. Artifact binding separately verifies that the
// identity's project ID matches the graph root.
func NewServingIdentity(projectID ResourceID, environment, generationID string) (ServingIdentity, error) {
	return normalizeServingIdentity(ServingIdentity{
		ProjectID:    projectID,
		Environment:  environment,
		GenerationID: generationID,
	})
}

// Validate rejects zero, malformed, or non-canonical serving identities.
func (identity ServingIdentity) Validate() error {
	validated, err := normalizeServingIdentity(identity)
	if err != nil {
		return err
	}
	if validated != identity {
		return ErrInvalidServingIdentity
	}
	return nil
}

// ArtifactEnvelope is one serving-scoped artifact containing exactly one
// portable project graph. The graph's canonical bytes are independent of this
// envelope; the envelope adds serving identity for activation and leasing.
type ArtifactEnvelope struct {
	version   int
	identity  ServingIdentity
	graph     ProjectGraph
	canonical []byte
	digest    string
}

// Version returns the serving artifact format version.
func (a ArtifactEnvelope) Version() int { return a.version }

// Identity returns the serving identity by value.
func (a ArtifactEnvelope) Identity() ServingIdentity { return a.identity }

// Graph returns the portable project graph by value.
func (a ArtifactEnvelope) Graph() ProjectGraph { return a.graph }

// NewArtifactEnvelope validates and binds graph to a serving identity.
func NewArtifactEnvelope(identity ServingIdentity, graph ProjectGraph) (ArtifactEnvelope, error) {
	identity, err := normalizeServingIdentity(identity)
	if err != nil {
		return ArtifactEnvelope{}, err
	}
	if err := graph.Validate(); err != nil {
		return ArtifactEnvelope{}, err
	}
	if identity.ProjectID != graph.ProjectID() {
		return ArtifactEnvelope{}, fmt.Errorf("%w: identity %q, graph root %q", ErrProjectIdentityMismatch, identity.ProjectID, graph.ProjectID())
	}
	wire := artifactWire{Version: ArtifactVersion, Identity: identity, Graph: graph}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return ArtifactEnvelope{}, fmt.Errorf("encode canonical project artifact: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return ArtifactEnvelope{
		version: ArtifactVersion, identity: identity, graph: graph,
		canonical: canonical,
		digest:    digestPrefix + hex.EncodeToString(sum[:]),
	}, nil
}

// DecodeArtifactEnvelope decodes and validates a serving-scoped artifact.
func DecodeArtifactEnvelope(data []byte) (ArtifactEnvelope, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return ArtifactEnvelope{}, fmt.Errorf("decode project artifact: %w", err)
	}
	var wire artifactWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return ArtifactEnvelope{}, fmt.Errorf("decode project artifact: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return ArtifactEnvelope{}, errors.New("decode project artifact: trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return ArtifactEnvelope{}, fmt.Errorf("decode project artifact: trailing data: %w", err)
	}
	if wire.Version != ArtifactVersion {
		return ArtifactEnvelope{}, UnsupportedVersionError{Contract: "project artifact", Version: wire.Version}
	}
	return NewArtifactEnvelope(wire.Identity, wire.Graph)
}

// CanonicalBytes returns deterministic serving-scoped artifact bytes.
func (a ArtifactEnvelope) CanonicalBytes() []byte { return append([]byte(nil), a.canonical...) }

// Digest returns the SHA-256 digest of CanonicalBytes.
func (a ArtifactEnvelope) Digest() string { return a.digest }

// MarshalJSON emits canonical serving-scoped artifact bytes.
func (a ArtifactEnvelope) MarshalJSON() ([]byte, error) {
	if len(a.canonical) == 0 {
		return nil, errors.New("project artifact is not initialized")
	}
	return a.CanonicalBytes(), nil
}

// UnmarshalJSON decodes and validates a serving-scoped artifact.
func (a *ArtifactEnvelope) UnmarshalJSON(data []byte) error {
	if a == nil {
		return errors.New("cannot unmarshal project artifact into nil receiver")
	}
	decoded, err := DecodeArtifactEnvelope(data)
	if err != nil {
		return err
	}
	*a = decoded
	return nil
}

type artifactWire struct {
	Version  int             `json:"version"`
	Identity ServingIdentity `json:"identity"`
	Graph    ProjectGraph    `json:"graph"`
}

func normalizeServingIdentity(identity ServingIdentity) (ServingIdentity, error) {
	projectID, err := NewResourceID(identity.ProjectID.String())
	if err != nil {
		return ServingIdentity{}, fmt.Errorf("%w: project id %q (%v)", ErrInvalidServingIdentity, identity.ProjectID, err)
	}
	environment, err := canonicalScopeValue(identity.Environment, "environment")
	if err != nil {
		return ServingIdentity{}, err
	}
	generation, err := canonicalScopeValue(identity.GenerationID, "generation id")
	if err != nil {
		return ServingIdentity{}, err
	}
	return ServingIdentity{ProjectID: projectID, Environment: environment, GenerationID: generation}, nil
}

func canonicalScopeValue(value, label string) (string, error) {
	if !resourceIDPattern.MatchString(value) {
		return "", fmt.Errorf("%w: %s %q", ErrInvalidServingIdentity, label, value)
	}
	return value, nil
}

type graphWire struct {
	Version   int        `json:"version"`
	Resources []Resource `json:"resources"`
	Edges     []Edge     `json:"edges"`
}

func validate(resources []Resource, edges []Edge) error {
	ids := make(map[ResourceID]struct{}, len(resources))
	names := make(map[string]ResourceID, len(resources))
	for index := range resources {
		resource := &resources[index]
		id, err := NewResourceID(resource.ID.String())
		if err != nil {
			return fmt.Errorf("resource %d: %w", index, err)
		}
		resource.ID = id
		kind, err := ParseKind(string(resource.Kind))
		if err != nil {
			return fmt.Errorf("resource %q: %w", id, err)
		}
		resource.Kind = kind
		if resource.Name == "" {
			return fmt.Errorf("resource %q: %w (name is required)", id, ErrInvalidName)
		}
		if !resourceNamePattern.MatchString(resource.Name) {
			return fmt.Errorf("resource %q name: %w", id, ErrInvalidName)
		}
		if _, exists := ids[id]; exists {
			return fmt.Errorf("resource %q: %w", id, ErrDuplicateResourceID)
		}
		ids[id] = struct{}{}
		if previous, exists := names[resource.Name]; exists {
			return fmt.Errorf("resources %q and %q: %w %q", previous, id, ErrDuplicateName, resource.Name)
		}
		names[resource.Name] = id
		resource.Metadata.Tags = sortedStrings(resource.Metadata.Tags)
	}

	edgesSeen := make(map[string]struct{}, len(edges))
	adjacency := make(map[ResourceID][]ResourceID, len(ids))
	for index := range edges {
		edge := &edges[index]
		from, err := NewResourceID(edge.From.String())
		if err != nil {
			return fmt.Errorf("edge %d from: %w", index, err)
		}
		to, err := NewResourceID(edge.To.String())
		if err != nil {
			return fmt.Errorf("edge %d to: %w", index, err)
		}
		edge.From, edge.To = from, to
		if _, ok := ids[from]; !ok {
			return fmt.Errorf("edge %d from %q: %w", index, from, ErrMissingEndpoint)
		}
		if _, ok := ids[to]; !ok {
			return fmt.Errorf("edge %d to %q: %w", index, to, ErrMissingEndpoint)
		}
		// Relation is descriptive; duplicate endpoint pairs are still one
		// dependency edge even when an input gives them different labels.
		key := string(from) + "\x00" + string(to)
		if _, exists := edgesSeen[key]; exists {
			return fmt.Errorf("edge %q -> %q: %w", from, to, ErrDuplicateEdge)
		}
		edgesSeen[key] = struct{}{}
		adjacency[from] = append(adjacency[from], to)
	}
	if cycle := findCycle(ids, adjacency); len(cycle) > 0 {
		return fmt.Errorf("%w: %s", ErrCycle, strings.Join(cycle, " -> "))
	}
	return nil
}

func projectRootID(resources []Resource) (ResourceID, error) {
	var root ResourceID
	for _, resource := range resources {
		if resource.Kind != KindProject {
			continue
		}
		if root != "" {
			return "", fmt.Errorf("%w: multiple project resources", ErrProjectRoot)
		}
		root = resource.ID
	}
	if root == "" {
		return "", fmt.Errorf("%w: project resource is missing", ErrProjectRoot)
	}
	return root, nil
}

func findCycle(ids map[ResourceID]struct{}, adjacency map[ResourceID][]ResourceID) []string {
	state := make(map[ResourceID]uint8, len(ids))
	stack := make([]ResourceID, 0, len(ids))
	positions := make(map[ResourceID]int, len(ids))
	var visit func(ResourceID) []string
	visit = func(node ResourceID) []string {
		switch state[node] {
		case 1:
			start := positions[node]
			cycle := make([]string, 0, len(stack)-start+1)
			for _, id := range stack[start:] {
				cycle = append(cycle, id.String())
			}
			cycle = append(cycle, node.String())
			return cycle
		case 2:
			return nil
		}
		state[node] = 1
		positions[node] = len(stack)
		stack = append(stack, node)
		neighbors := append([]ResourceID(nil), adjacency[node]...)
		sort.Slice(neighbors, func(i, j int) bool { return neighbors[i] < neighbors[j] })
		for _, neighbor := range neighbors {
			if cycle := visit(neighbor); len(cycle) > 0 {
				return cycle
			}
		}
		stack = stack[:len(stack)-1]
		delete(positions, node)
		state[node] = 2
		return nil
	}
	idsSorted := make([]ResourceID, 0, len(ids))
	for id := range ids {
		idsSorted = append(idsSorted, id)
	}
	sort.Slice(idsSorted, func(i, j int) bool { return idsSorted[i] < idsSorted[j] })
	for _, id := range idsSorted {
		if state[id] == 0 {
			if cycle := visit(id); len(cycle) > 0 {
				return cycle
			}
		}
	}
	return nil
}

func cloneResources(resources []Resource) []Resource {
	if resources == nil {
		return nil
	}
	out := make([]Resource, len(resources))
	for index, resource := range resources {
		out[index] = cloneResource(resource)
	}
	return out
}

func cloneResource(resource Resource) Resource {
	resource.Metadata.Tags = append([]string(nil), resource.Metadata.Tags...)
	return resource
}

func cloneEdges(edges []Edge) []Edge { return append([]Edge(nil), edges...) }

func sortResources(resources []Resource) {
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].ID != resources[j].ID {
			return resources[i].ID < resources[j].ID
		}
		if resources[i].Kind != resources[j].Kind {
			return resources[i].Kind < resources[j].Kind
		}
		return resources[i].Name < resources[j].Name
	})
}

func sortEdges(edges []Edge) {
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Relation < edges[j].Relation
	})
}

func sortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	values = append([]string(nil), values...)
	sort.Strings(values)
	return values
}

// rejectDuplicateJSONKeys walks one JSON value before typed decoding. The
// standard decoder intentionally applies last-key-wins semantics; canonical
// project artifacts instead reject ambiguity at every nested object level.
func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			keys := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is not a string")
				}
				canonicalKey := strings.ToLower(key)
				if _, exists := keys[canonicalKey]; exists {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				keys[canonicalKey] = struct{}{}
				if err := walkJSONValue(decoder); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil {
				return err
			}
			if end != json.Delim('}') {
				return fmt.Errorf("JSON object ended with %v", end)
			}
		case '[':
			for decoder.More() {
				if err := walkJSONValue(decoder); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil {
				return err
			}
			if end != json.Delim(']') {
				return fmt.Errorf("JSON array ended with %v", end)
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}
	return nil
}
