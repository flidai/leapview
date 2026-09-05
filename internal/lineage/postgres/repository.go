// Package postgres stores an immutable, compiler-owned lineage graph in
// PostgreSQL.  The graph is content addressed: rows are keyed by a digest of
// a versioned canonical representation, while delivery and generation are
// kept in a separate immutable binding table.
//
// Persistence intentionally accepts the caller's pgx transaction.  This lets
// deployment commit the compiler artifact and its lineage projection in one
// transaction without this package opening a second connection or transaction.
package postgres

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	lineagedb "github.com/flidai/leapview/internal/lineage/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// CanonicalVersion is bumped whenever the canonical lineage wire format
	// changes.  A version is part of the digest input and never inferred.
	CanonicalVersion   = 1
	maxProjectionNodes = 100000
	maxProjectionEdges = 500000
	maxPropertyBytes   = 65536
	maxTraversalDepth  = 64
	maxTraversalNodes  = 10000
	maxTraversalEdges  = 50000
	// MaxTraversalDepth, MaxTraversalNodes and MaxTraversalEdges are the
	// hard server-side bounds applied to every recursive request.
	MaxTraversalDepth = maxTraversalDepth
	MaxTraversalNodes = maxTraversalNodes
	MaxTraversalEdges = maxTraversalEdges
)

var (
	ErrInvalid        = errors.New("invalid lineage projection")
	ErrConflict       = errors.New("lineage projection conflict")
	ErrNotFound       = errors.New("lineage graph not found")
	ErrTampered       = errors.New("lineage graph integrity check failed")
	ErrCycle          = errors.New("lineage graph contains a cycle")
	ErrMissingNode    = errors.New("lineage edge endpoint is missing")
	ErrDuplicate      = errors.New("duplicate lineage row")
	ErrForbidden      = errors.New("lineage resource is not allowed")
	ErrTraversalLimit = errors.New("lineage traversal limit is invalid")
)

// Tx is the complete native transaction contract so mutation methods cannot
// accept a pool and silently execute related statements on different sessions.
type Tx = pgx.Tx

// DB is the read surface used by Load and Traverse.
type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Node is one immutable compiler resource projection. Properties are
// canonical JSON (an object); identity_digest is independent of properties.
type Node struct {
	ID             string          `json:"id"`
	ResourceKind   string          `json:"resource_kind"`
	IdentityDigest string          `json:"identity_digest"`
	Properties     json.RawMessage `json:"properties"`
}

// Edge is a directed dependency. It follows the compiler graph convention:
// From references To (i.e. From depends on To).
type Edge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation,omitempty"`
}

// Projection is an immutable, content-addressed lineage graph. Use New or
// FromGraph to construct one from the compiler graph; callers must not invent
// serving identity in this value.
type Projection struct {
	Version   int    `json:"version"`
	Digest    string `json:"digest"`
	ProjectID string `json:"project_id"`
	Nodes     []Node `json:"nodes"`
	Edges     []Edge `json:"edges"`
	canonical []byte
}

// Binding binds a graph to one explicit delivery/generation identity. Both
// values are required; a generation without its delivery is not a serving
// scope in this capability.
type Binding struct {
	DeliveryID   string `json:"delivery_id"`
	GenerationID string `json:"generation_id"`
	ProjectID    string `json:"project_id,omitempty"`
	GraphDigest  string `json:"graph_digest"`
}

// Revision is the durable publication of a graph for one project scope. A
// scope has at most one current revision (valid_to == nil); historical rows
// remain queryable by revision ID and are never deleted by this capability.
type Revision struct {
	ProjectID   string
	ScopeID     string
	RevisionID  int64
	GraphDigest string
	ValidFrom   time.Time
	ValidTo     *time.Time
	CreatedAt   time.Time
}

// RevisionInput is the explicit identity required to publish a graph. The
// projection's project ID is checked against ProjectID, preventing a caller
// from accidentally publishing another project's graph into this scope.
type RevisionInput struct {
	ProjectID  string
	ScopeID    string
	Projection Projection
	// Graph is a vocabulary alias for callers that refer to the published
	// value as a graph rather than a projection.
	Graph Projection
}

// TraversalDirection controls which endpoint is followed.
type TraversalDirection string

const (
	DirectionUpstream   TraversalDirection = "upstream"
	DirectionDownstream TraversalDirection = "downstream"
)

// TraversalInput is a generation-scoped, security-filtered traversal request.
// AllowedResourceIDs must be supplied by the access-authority caller; an
// empty set fails closed and denied resources cannot be used as transit nodes.
type TraversalInput struct {
	// ProjectID and ScopeID select the current revision directly. For
	// compatibility with serving callers, DeliveryID/GenerationID may instead
	// select an immutable binding. Exactly one selector form is required.
	ProjectID          string
	ScopeID            string
	DeliveryID         string
	GenerationID       string
	RootID             string
	Direction          TraversalDirection
	MaxDepth           int
	MaxNodes           int
	MaxEdges           int
	AllowedResourceIDs []string
}

// TraversalNode is a node plus its shortest discovered depth from the root.
type TraversalNode struct {
	Node  Node
	Depth int
}

// Repository is stateless apart from its read DB. Persistence methods still
// take an explicit transaction so callers retain commit/rollback ownership.
type Repository struct{ db DB }

//go:embed schema.sql
var schemaSQL string

// SchemaSQL returns the standalone capability schema. It contains no
// transaction control and may be executed through a caller-owned tx.
func SchemaSQL() string { return schemaSQL }

// ApplySchema applies the capability schema through the supplied transaction.
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrInvalid
	}
	// sqlc-exception: schema-ddl. Capability-owned schema DDL is applied as a
	// single caller-owned migration transaction.
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

// New creates a repository whose read methods use db.
func New(db DB) *Repository { return &Repository{db: db} }

// Configured reports whether the repository has a native database handle.
// Schema readiness remains the migration/lifecycle owner's responsibility.
func (r *Repository) Configured() bool {
	if r == nil || r.db == nil {
		return false
	}
	v := reflect.ValueOf(r.db)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !v.IsNil()
	default:
		return true
	}
}

// FromGraph projects the validated compiler graph into the canonical
// lineage representation. The compiler graph remains authoritative: no
// names, paths, or serving metadata are accepted as alternate identity.
func FromGraph(g projectgraph.ProjectGraph) (Projection, error) {
	if err := g.Validate(); err != nil {
		return Projection{}, fmt.Errorf("%w: compiler graph: %v", ErrInvalid, err)
	}
	nodes := make([]Node, 0, len(g.Resources()))
	for _, resource := range g.Resources() {
		props, err := json.Marshal(struct {
			Name       string                  `json:"name"`
			Metadata   projectgraph.Metadata   `json:"metadata,omitempty"`
			Provenance projectgraph.Provenance `json:"provenance,omitempty"`
		}{resource.Name, resource.Metadata, resource.Provenance})
		if err != nil {
			return Projection{}, fmt.Errorf("%w: encode properties: %v", ErrInvalid, err)
		}
		identity, err := IdentityDigest(resource.Kind, resource.ID)
		if err != nil {
			return Projection{}, err
		}
		nodes = append(nodes, Node{ID: resource.ID.String(), ResourceKind: string(resource.Kind), IdentityDigest: identity, Properties: props})
	}
	edges := make([]Edge, 0, len(g.Edges()))
	for _, edge := range g.Edges() {
		edges = append(edges, Edge{From: edge.From.String(), To: edge.To.String(), Relation: edge.Relation})
	}
	return NewProjectionFromRows(g.ProjectID().String(), nodes, edges)
}

// GraphDigest computes the projection digest for a compiler graph without
// persisting it.
func GraphDigest(g projectgraph.ProjectGraph) (string, error) {
	p, err := FromGraph(g)
	if err != nil {
		return "", err
	}
	return p.Digest, nil
}

// CompilerGraphDigest reconstructs the compiler-owned graph represented by a
// canonical lineage projection and returns its digest. The lineage projection
// has its own content-addressing domain, so its Digest must never be compared
// directly with ProjectGraph.Digest.
func CompilerGraphDigest(p Projection) (string, error) {
	p, err := canonicalProjection(p)
	if err != nil {
		return "", err
	}
	resources := make([]projectgraph.Resource, 0, len(p.Nodes))
	for _, node := range p.Nodes {
		id, err := projectgraph.NewResourceID(node.ID)
		if err != nil {
			return "", fmt.Errorf("%w: compiler resource id: %v", ErrInvalid, err)
		}
		kind, err := projectgraph.ParseKind(node.ResourceKind)
		if err != nil {
			return "", fmt.Errorf("%w: compiler resource kind: %v", ErrInvalid, err)
		}
		var properties struct {
			Name       string                  `json:"name"`
			Metadata   projectgraph.Metadata   `json:"metadata,omitempty"`
			Provenance projectgraph.Provenance `json:"provenance,omitempty"`
		}
		if err := strictjson.DecodeWithOptions(node.Properties, &properties, strictjson.Options{MaxBytes: maxPropertyBytes}); err != nil {
			return "", fmt.Errorf("%w: decode compiler resource properties: %v", ErrInvalid, err)
		}
		resources = append(resources, projectgraph.Resource{ID: id, Kind: kind, Name: properties.Name, Metadata: properties.Metadata, Provenance: properties.Provenance})
	}
	edges := make([]projectgraph.Edge, 0, len(p.Edges))
	for _, edge := range p.Edges {
		from, err := projectgraph.NewResourceID(edge.From)
		if err != nil {
			return "", fmt.Errorf("%w: compiler edge source: %v", ErrInvalid, err)
		}
		to, err := projectgraph.NewResourceID(edge.To)
		if err != nil {
			return "", fmt.Errorf("%w: compiler edge destination: %v", ErrInvalid, err)
		}
		edges = append(edges, projectgraph.Edge{From: from, To: to, Relation: edge.Relation})
	}
	graph, err := projectgraph.NewProjectGraph(resources, edges)
	if err != nil {
		return "", fmt.Errorf("%w: reconstruct compiler graph: %v", ErrInvalid, err)
	}
	if graph.ProjectID().String() != p.ProjectID {
		return "", fmt.Errorf("%w: compiler graph project differs from lineage projection", ErrTampered)
	}
	return graph.Digest(), nil
}

// FromArtifact projects the graph carried by an immutable compiler artifact.
// No serving identity or manifest projection is inferred here.
func FromArtifact(a interface {
	Graph() projectgraph.ProjectGraph
}) (Projection, error) {
	if a == nil {
		return Projection{}, ErrInvalid
	}
	return FromGraph(a.Graph())
}

// NewProjectionFromRows validates and canonicalizes already projected rows.
// It is useful for integrity checks and tests; production callers should use
// FromGraph so the compiler artifact is the authority.
func NewProjectionFromRows(projectID string, nodes []Node, edges []Edge) (Projection, error) {
	p := Projection{Version: CanonicalVersion, ProjectID: projectID, Nodes: cloneNodes(nodes), Edges: cloneEdges(edges)}
	if err := p.validateAndCanonicalize(); err != nil {
		return Projection{}, err
	}
	return p, nil
}

// IdentityDigest returns the canonical digest of a resource's explicit kind
// and ID. Properties never affect this identity digest.
func IdentityDigest(kind projectgraph.Kind, id projectgraph.ResourceID) (string, error) {
	if !kind.Valid() {
		return "", fmt.Errorf("%w: invalid resource kind %q", ErrInvalid, kind)
	}
	if _, err := projectgraph.NewResourceID(id.String()); err != nil {
		return "", fmt.Errorf("%w: resource id: %v", ErrInvalid, err)
	}
	b, _ := json.Marshal(struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}{string(kind), id.String()})
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// CanonicalBytes returns a defensive copy of the canonical digest input.
func (p Projection) CanonicalBytes() []byte { return append([]byte(nil), p.canonical...) }

type canonicalWire struct {
	Version int    `json:"version"`
	Nodes   []Node `json:"nodes"`
	Edges   []Edge `json:"edges"`
}

func (p *Projection) validateAndCanonicalize() error {
	if p == nil || p.Version != CanonicalVersion {
		return fmt.Errorf("%w: unsupported canonical version %d", ErrInvalid, p.Version)
	}
	if len(p.ProjectID) > 256 {
		return fmt.Errorf("%w: project id exceeds 256 bytes", ErrInvalid)
	}
	if _, err := projectgraph.NewResourceID(p.ProjectID); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	if len(p.Nodes) == 0 {
		return fmt.Errorf("%w: graph has no nodes", ErrInvalid)
	}
	if len(p.Nodes) > maxProjectionNodes || len(p.Edges) > maxProjectionEdges {
		return fmt.Errorf("%w: graph exceeds projection bounds", ErrInvalid)
	}
	sort.Slice(p.Nodes, func(i, j int) bool { return p.Nodes[i].ID < p.Nodes[j].ID })
	seen := make(map[string]struct{}, len(p.Nodes))
	projectRoots := 0
	for i := range p.Nodes {
		n := &p.Nodes[i]
		id, err := projectgraph.NewResourceID(n.ID)
		if err != nil {
			return fmt.Errorf("%w: node %d id: %v", ErrInvalid, i, err)
		}
		n.ID = id.String()
		kind, err := projectgraph.ParseKind(n.ResourceKind)
		if err != nil {
			return fmt.Errorf("%w: node %q kind: %v", ErrInvalid, n.ID, err)
		}
		n.ResourceKind = string(kind)
		if len(n.ID) > 256 {
			return fmt.Errorf("%w: node %q id exceeds 256 bytes", ErrInvalid, n.ID)
		}
		if _, ok := seen[n.ID]; ok {
			return fmt.Errorf("%w: node %q", ErrDuplicate, n.ID)
		}
		seen[n.ID] = struct{}{}
		if n.ResourceKind == string(projectgraph.KindProject) {
			projectRoots++
			if n.ID != p.ProjectID {
				return fmt.Errorf("%w: project root %q does not match %q", ErrInvalid, n.ID, p.ProjectID)
			}
		}
		if !validDigest(n.IdentityDigest) {
			return fmt.Errorf("%w: node %q identity digest", ErrInvalid, n.ID)
		}
		wantIdentity, _ := IdentityDigest(kind, id)
		if n.IdentityDigest != wantIdentity {
			return fmt.Errorf("%w: node %q identity digest mismatch", ErrTampered, n.ID)
		}
		if len(n.Properties) == 0 || len(n.Properties) > maxPropertyBytes {
			return fmt.Errorf("%w: node %q properties", ErrInvalid, n.ID)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(n.Properties, &object); err != nil || object == nil {
			return fmt.Errorf("%w: node %q properties must be an object", ErrInvalid, n.ID)
		}
		canonical, err := canonicalJSON(n.Properties)
		if err != nil {
			return fmt.Errorf("%w: node %q properties: %v", ErrInvalid, n.ID, err)
		}
		n.Properties = canonical
	}
	if projectRoots != 1 {
		return fmt.Errorf("%w: graph must contain exactly one project root", ErrInvalid)
	}
	if len(p.Edges) > 0 {
		sort.Slice(p.Edges, func(i, j int) bool {
			a, b := p.Edges[i], p.Edges[j]
			if a.From != b.From {
				return a.From < b.From
			}
			if a.To != b.To {
				return a.To < b.To
			}
			return a.Relation < b.Relation
		})
	}
	seenEdges := make(map[string]struct{}, len(p.Edges))
	adj := make(map[string][]string, len(p.Nodes))
	for i := range p.Edges {
		e := &p.Edges[i]
		if e.Relation != strings.TrimSpace(e.Relation) || len(e.Relation) > 128 {
			return fmt.Errorf("%w: edge %d relation", ErrInvalid, i)
		}
		if _, err := projectgraph.NewResourceID(e.From); err != nil {
			return fmt.Errorf("%w: edge %d from: %v", ErrInvalid, i, err)
		}
		if _, err := projectgraph.NewResourceID(e.To); err != nil {
			return fmt.Errorf("%w: edge %d to: %v", ErrInvalid, i, err)
		}
		if _, ok := seen[e.From]; !ok {
			return fmt.Errorf("%w: %s", ErrMissingNode, e.From)
		}
		if _, ok := seen[e.To]; !ok {
			return fmt.Errorf("%w: %s", ErrMissingNode, e.To)
		}
		if e.From == e.To {
			return fmt.Errorf("%w: self edge %q", ErrCycle, e.From)
		}
		key := e.From + "\x00" + e.To
		if _, ok := seenEdges[key]; ok {
			return fmt.Errorf("%w: %s -> %s", ErrDuplicate, e.From, e.To)
		}
		seenEdges[key] = struct{}{}
		adj[e.From] = append(adj[e.From], e.To)
	}
	if cycle := findCycle(seen, adj); len(cycle) > 0 {
		return fmt.Errorf("%w: %s", ErrCycle, strings.Join(cycle, " -> "))
	}
	wire := canonicalWire{Version: CanonicalVersion, Nodes: cloneNodes(p.Nodes), Edges: cloneEdges(p.Edges)}
	b, err := json.Marshal(wire)
	if err != nil {
		return fmt.Errorf("%w: canonical encoding: %v", ErrInvalid, err)
	}
	sum := sha256.Sum256(b)
	p.Digest = "sha256:" + hex.EncodeToString(sum[:])
	p.canonical = b
	return nil
}

func (p Projection) Validate() error {
	supplied := p.Digest
	copy := p
	copy.Nodes, copy.Edges = cloneNodes(p.Nodes), cloneEdges(p.Edges)
	if err := copy.validateAndCanonicalize(); err != nil {
		return err
	}
	if supplied != "" && supplied != copy.Digest {
		return fmt.Errorf("%w: digest %s, recomputed %s", ErrTampered, supplied, copy.Digest)
	}
	return nil
}

// canonicalProjection returns a detached, fully canonical projection. An
// omitted digest is filled from the rows; a supplied digest must match.
func canonicalProjection(p Projection) (Projection, error) {
	supplied := p.Digest
	p.Nodes, p.Edges = cloneNodes(p.Nodes), cloneEdges(p.Edges)
	p.Digest = ""
	if err := p.validateAndCanonicalize(); err != nil {
		return Projection{}, err
	}
	if supplied != "" && supplied != p.Digest {
		return Projection{}, fmt.Errorf("%w: digest %s, recomputed %s", ErrTampered, supplied, p.Digest)
	}
	return p, nil
}

func validDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}

func canonicalJSON(value []byte) ([]byte, error) {
	var validated json.RawMessage
	if err := strictjson.DecodeWithOptions(value, &validated, strictjson.Options{
		MaxBytes:           maxPropertyBytes,
		MaxDepth:           100,
		DuplicateKeys:      strictjson.CaseSensitiveKeys,
		AllowUnknownFields: true,
	}); err != nil {
		return nil, err
	}
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	if len(canonical) > maxPropertyBytes {
		return nil, fmt.Errorf("canonical JSON exceeds %d bytes", maxPropertyBytes)
	}
	return canonical, nil
}

func findCycle(nodes map[string]struct{}, adj map[string][]string) []string {
	state := make(map[string]uint8, len(nodes))
	stack := []string{}
	pos := map[string]int{}
	keys := make([]string, 0, len(nodes))
	for id := range nodes {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	var visit func(string) []string
	visit = func(id string) []string {
		switch state[id] {
		case 1:
			c := append([]string(nil), stack[pos[id]:]...)
			return append(c, id)
		case 2:
			return nil
		}
		state[id] = 1
		pos[id] = len(stack)
		stack = append(stack, id)
		next := append([]string(nil), adj[id]...)
		sort.Strings(next)
		for _, child := range next {
			if c := visit(child); len(c) > 0 {
				return c
			}
		}
		stack = stack[:len(stack)-1]
		delete(pos, id)
		state[id] = 2
		return nil
	}
	for _, id := range keys {
		if state[id] == 0 {
			if c := visit(id); len(c) > 0 {
				return c
			}
		}
	}
	return nil
}

func cloneNodes(in []Node) []Node {
	out := make([]Node, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Properties = append(json.RawMessage(nil), in[i].Properties...)
	}
	return out
}
func cloneEdges(in []Edge) []Edge { return append([]Edge(nil), in...) }

// Persist stores a projection and (optionally) no binding. It is idempotent
// for exact replay and reports ErrConflict when an existing digest's rows no
// longer match the supplied projection.
func Persist(ctx context.Context, tx Tx, p Projection) error {
	if tx == nil {
		return ErrInvalid
	}
	var err error
	p, err = canonicalProjection(p)
	if err != nil {
		return err
	}
	q := lineagedb.New(tx)
	_, err = q.InsertGraph(ctx, lineagedb.InsertGraphParams{
		GraphDigest: p.Digest, GraphVersion: int32(p.Version), ProjectID: p.ProjectID,
		NodeCount: int32(len(p.Nodes)), EdgeCount: int32(len(p.Edges)),
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if err := verifyStoredProjection(ctx, tx, p); err != nil {
			if errors.Is(err, ErrTampered) {
				return fmt.Errorf("%w: existing graph rows differ: %v", ErrConflict, err)
			}
			return err
		}
		return nil
	}
	for _, n := range p.Nodes {
		if err := q.InsertNode(ctx, lineagedb.InsertNodeParams{
			GraphDigest: p.Digest, ProjectID: p.ProjectID, NodeID: n.ID,
			ResourceKind: n.ResourceKind, IdentityDigest: n.IdentityDigest, Properties: n.Properties,
		}); err != nil {
			return err
		}
	}
	for _, e := range p.Edges {
		if err := q.InsertEdge(ctx, lineagedb.InsertEdgeParams{
			GraphDigest: p.Digest, ProjectID: p.ProjectID, FromNodeID: e.From, ToNodeID: e.To, Relation: e.Relation,
		}); err != nil {
			return err
		}
	}
	return nil
}

// PersistGraph projects and stores a compiler graph and atomically records its
// delivery/generation binding in the caller-owned transaction.
func PersistGraph(ctx context.Context, tx Tx, g projectgraph.ProjectGraph, b Binding) (Projection, error) {
	p, err := FromGraph(g)
	if err != nil {
		return Projection{}, err
	}
	if b.GraphDigest == "" {
		b.GraphDigest = p.Digest
	}
	if b.GraphDigest != p.Digest {
		return Projection{}, fmt.Errorf("%w: binding digest does not match projection", ErrConflict)
	}
	if b.ProjectID == "" {
		b.ProjectID = p.ProjectID
	} else if b.ProjectID != p.ProjectID {
		return Projection{}, fmt.Errorf("%w: binding project does not match projection", ErrConflict)
	}
	if err := Persist(ctx, tx, p); err != nil {
		return Projection{}, err
	}
	if err := PersistBinding(ctx, tx, b); err != nil {
		return Projection{}, err
	}
	return p, nil
}

func (r *Repository) Persist(ctx context.Context, tx Tx, p Projection) error {
	if r == nil {
		return ErrInvalid
	}
	return Persist(ctx, tx, p)
}
func (r *Repository) PersistGraph(ctx context.Context, tx Tx, g projectgraph.ProjectGraph, b Binding) (Projection, error) {
	if r == nil {
		return Projection{}, ErrInvalid
	}
	return PersistGraph(ctx, tx, g, b)
}

// PersistBinding records one immutable binding and verifies exact replay.
func PersistBinding(ctx context.Context, tx Tx, b Binding) error {
	if tx == nil || !validScope(b.DeliveryID) || !validScope(b.GenerationID) || !validDigest(b.GraphDigest) {
		return ErrInvalid
	}
	q := lineagedb.New(tx)
	if b.ProjectID == "" {
		projectID, err := q.GetGraphProjectID(ctx, b.GraphDigest)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		b.ProjectID = projectID
	}
	if !validScope(b.ProjectID) {
		return ErrInvalid
	}
	_, err := q.InsertBinding(ctx, lineagedb.InsertBindingParams{
		DeliveryID: b.DeliveryID, GenerationID: b.GenerationID,
		ProjectID: b.ProjectID, GraphDigest: b.GraphDigest,
	})
	// The project ID is part of the binding identity.  Keep the conflict
	// comparison below explicit so retries with a graph from another project
	// cannot silently reuse a serving scope.
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	existing, err := q.GetBinding(ctx, lineagedb.GetBindingParams{DeliveryID: b.DeliveryID, GenerationID: b.GenerationID})
	if err != nil {
		return err
	}
	if existing.GraphDigest != b.GraphDigest || existing.ProjectID != b.ProjectID {
		return fmt.Errorf("%w: binding (%s,%s)", ErrConflict, b.DeliveryID, b.GenerationID)
	}
	return nil
}

func validScope(v string) bool { return v == strings.TrimSpace(v) && v != "" && len(v) <= 256 }

// PublishRevision atomically replaces the current revision for a project
// scope. It stores the projection (idempotently), closes the previous
// validity interval using a database timestamp, and inserts the next revision
// under one advisory transaction lock. The caller owns commit/rollback.
func PublishRevision(ctx context.Context, tx Tx, in RevisionInput) (Revision, error) {
	if tx == nil || !validScope(in.ProjectID) || !validScope(in.ScopeID) {
		return Revision{}, ErrInvalid
	}
	projection := in.Projection
	if projection.ProjectID == "" && len(projection.Nodes) == 0 && len(projection.Edges) == 0 {
		projection = in.Graph
	}
	p, err := canonicalProjection(projection)
	if err != nil {
		return Revision{}, err
	}
	if p.ProjectID != in.ProjectID {
		return Revision{}, fmt.Errorf("%w: revision project does not match projection", ErrConflict)
	}
	if err := Persist(ctx, tx, p); err != nil {
		return Revision{}, err
	}
	q := lineagedb.New(tx)
	row, err := q.PublishRevision(ctx, lineagedb.PublishRevisionParams{ProjectID: in.ProjectID, ScopeID: in.ScopeID, GraphDigest: p.Digest})
	if err != nil {
		if isUniqueViolation(err) {
			return Revision{}, fmt.Errorf("%w: revision publication raced", ErrConflict)
		}
		return Revision{}, err
	}
	return revisionFromRow(lineagedb.LineageRevision{
		ProjectID: row.ProjectID, ScopeID: row.ScopeID, RevisionID: row.RevisionID,
		GraphDigest: row.GraphDigest, ValidFrom: row.ValidFrom, ValidTo: row.ValidTo, CreatedAt: row.CreatedAt,
	})
}

// ReplaceRevision is the explicit replacement spelling retained for callers
// that treat revisions as current-scope state.
func ReplaceRevision(ctx context.Context, tx Tx, in RevisionInput) (Revision, error) {
	return PublishRevision(ctx, tx, in)
}

// Publish is a concise compatibility alias for PublishRevision.
func Publish(ctx context.Context, tx Tx, in RevisionInput) (Revision, error) {
	return PublishRevision(ctx, tx, in)
}

// PublishRevisionForScope is a convenience form for callers that already
// hold a canonical projection and separate scope coordinates.
func PublishRevisionForScope(ctx context.Context, tx Tx, projectID, scopeID string, p Projection) (Revision, error) {
	return PublishRevision(ctx, tx, RevisionInput{ProjectID: projectID, ScopeID: scopeID, Projection: p})
}

func (r *Repository) PublishRevision(ctx context.Context, tx Tx, in RevisionInput) (Revision, error) {
	if r == nil {
		return Revision{}, ErrInvalid
	}
	return PublishRevision(ctx, tx, in)
}

func (r *Repository) ReplaceRevision(ctx context.Context, tx Tx, in RevisionInput) (Revision, error) {
	if r == nil {
		return Revision{}, ErrInvalid
	}
	return PublishRevision(ctx, tx, in)
}

// CurrentRevision returns the current (valid_to IS NULL) graph revision for a
// project scope. Historical rows are intentionally not selected here.
func CurrentRevision(ctx context.Context, db DB, projectID, scopeID string) (Revision, error) {
	if db == nil || !validScope(projectID) || !validScope(scopeID) {
		return Revision{}, ErrInvalid
	}
	q := lineagedb.New(db)
	row, err := q.GetCurrentRevision(ctx, lineagedb.GetCurrentRevisionParams{ProjectID: projectID, ScopeID: scopeID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Revision{}, ErrNotFound
	}
	if err != nil {
		return Revision{}, err
	}
	return revisionFromRow(row)
}

func (r *Repository) CurrentRevision(ctx context.Context, projectID, scopeID string) (Revision, error) {
	if r == nil {
		return Revision{}, ErrInvalid
	}
	return CurrentRevision(ctx, r.db, projectID, scopeID)
}

// LoadRevision reads one historical revision by its project-scoped ID.
func LoadRevision(ctx context.Context, db DB, projectID, scopeID string, revisionID int64) (Revision, error) {
	if db == nil || !validScope(projectID) || !validScope(scopeID) || revisionID <= 0 {
		return Revision{}, ErrInvalid
	}
	q := lineagedb.New(db)
	row, err := q.GetRevision(ctx, lineagedb.GetRevisionParams{ProjectID: projectID, ScopeID: scopeID, RevisionID: revisionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Revision{}, ErrNotFound
	}
	if err != nil {
		return Revision{}, err
	}
	return revisionFromRow(row)
}

func (r *Repository) LoadRevision(ctx context.Context, projectID, scopeID string, revisionID int64) (Revision, error) {
	if r == nil {
		return Revision{}, ErrInvalid
	}
	return LoadRevision(ctx, r.db, projectID, scopeID, revisionID)
}

func revisionFromRow(row lineagedb.LineageRevision) (Revision, error) {
	if !row.ValidFrom.Valid || !row.CreatedAt.Valid {
		return Revision{}, errors.New("lineage revision contains NULL timestamp")
	}
	out := Revision{
		ProjectID: row.ProjectID, ScopeID: row.ScopeID, RevisionID: row.RevisionID,
		GraphDigest: row.GraphDigest, ValidFrom: row.ValidFrom.Time, CreatedAt: row.CreatedAt.Time,
	}
	if row.ValidTo.Valid {
		validTo := row.ValidTo.Time
		out.ValidTo = &validTo
	}
	return out, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func verifyStoredProjection(ctx context.Context, db DB, want Projection) error {
	got, err := loadDigest(ctx, db, want.Digest)
	if err != nil {
		return err
	}
	if got.ProjectID != want.ProjectID || got.Version != want.Version || got.Digest != want.Digest {
		return fmt.Errorf("%w: graph metadata differs", ErrConflict)
	}
	if !equalProjectionRows(got, want) {
		return fmt.Errorf("%w: graph rows differ for %s", ErrConflict, want.Digest)
	}
	return nil
}

// Load fetches and verifies a graph by digest. Missing rows, altered rows,
// endpoint closure failures, cycles, and digest mismatches all fail closed.
func Load(ctx context.Context, db DB, digest string) (Projection, error) {
	if db == nil || !validDigest(digest) {
		return Projection{}, ErrInvalid
	}
	return loadDigest(ctx, db, digest)
}

func (r *Repository) Load(ctx context.Context, digest string) (Projection, error) {
	if r == nil {
		return Projection{}, ErrInvalid
	}
	return Load(ctx, r.db, digest)
}

func loadDigest(ctx context.Context, db DB, digest string) (Projection, error) {
	q := lineagedb.New(db)
	metadata, err := q.GetGraphMetadata(ctx, digest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Projection{}, ErrNotFound
		}
		return Projection{}, err
	}
	version, projectID := int(metadata.GraphVersion), metadata.ProjectID
	nodeCount, edgeCount := int(metadata.NodeCount), int(metadata.EdgeCount)
	if version != CanonicalVersion || nodeCount < 1 || nodeCount > maxProjectionNodes || edgeCount < 0 || edgeCount > maxProjectionEdges {
		return Projection{}, fmt.Errorf("%w: stored graph metadata exceeds bounds", ErrTampered)
	}
	nodes := make([]Node, 0, nodeCount)
	nodeRows, err := q.ListNodes(ctx, lineagedb.ListNodesParams{GraphDigest: digest, RowLimit: int32(nodeCount + 1)})
	if err != nil {
		return Projection{}, err
	}
	for _, row := range nodeRows {
		var n Node
		n.ID, n.ResourceKind, n.IdentityDigest = row.NodeID, row.ResourceKind, row.IdentityDigest
		if row.ProjectID != projectID {
			return Projection{}, fmt.Errorf("%w: node project mismatch", ErrTampered)
		}
		n.Properties = append(json.RawMessage(nil), row.Properties...)
		nodes = append(nodes, n)
	}
	edges := make([]Edge, 0, edgeCount)
	edgeRows, err := q.ListEdges(ctx, lineagedb.ListEdgesParams{GraphDigest: digest, RowLimit: int32(edgeCount + 1)})
	if err != nil {
		return Projection{}, err
	}
	for _, row := range edgeRows {
		var e Edge
		e.From, e.To, e.Relation = row.FromNodeID, row.ToNodeID, row.Relation
		if row.ProjectID != projectID {
			return Projection{}, fmt.Errorf("%w: edge project mismatch", ErrTampered)
		}
		edges = append(edges, e)
	}
	if len(nodes) != nodeCount || len(edges) != edgeCount {
		return Projection{}, fmt.Errorf("%w: stored row count differs", ErrTampered)
	}
	p := Projection{Version: version, ProjectID: projectID, Digest: digest, Nodes: nodes, Edges: edges}
	if err := p.validateAndCanonicalize(); err != nil {
		return Projection{}, fmt.Errorf("%w: %v", ErrTampered, err)
	}
	if p.Digest != digest {
		return Projection{}, fmt.Errorf("%w: recomputed digest %s", ErrTampered, p.Digest)
	}
	return p, nil
}

func equalProjectionRows(a, b Projection) bool {
	return a.ProjectID == b.ProjectID && a.Version == b.Version && a.Digest == b.Digest && string(a.CanonicalBytes()) == string(b.CanonicalBytes())
}

// LoadBound resolves an exact delivery/generation binding and then verifies
// the referenced graph. A wrong generation is indistinguishable from missing
// data to callers (fail closed).
func LoadBound(ctx context.Context, db DB, deliveryID, generationID string) (Projection, error) {
	if db == nil || !validScope(deliveryID) || !validScope(generationID) {
		return Projection{}, ErrInvalid
	}
	q := lineagedb.New(db)
	binding, err := q.GetBinding(ctx, lineagedb.GetBindingParams{DeliveryID: deliveryID, GenerationID: generationID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Projection{}, ErrNotFound
		}
		return Projection{}, err
	}
	p, err := Load(ctx, db, binding.GraphDigest)
	if err != nil {
		return Projection{}, err
	}
	if p.ProjectID != binding.ProjectID {
		return Projection{}, ErrTampered
	}
	return p, nil
}

// LoadBoundForProject resolves a binding only when it belongs to the supplied
// project scope. This prevents a caller that knows a delivery/generation ID
// from crossing project boundaries.
func LoadBoundForProject(ctx context.Context, db DB, projectID, deliveryID, generationID string) (Projection, error) {
	if !validScope(projectID) {
		return Projection{}, ErrInvalid
	}
	p, err := LoadBound(ctx, db, deliveryID, generationID)
	if err != nil {
		return Projection{}, err
	}
	if p.ProjectID != projectID {
		return Projection{}, ErrNotFound
	}
	return p, nil
}

func (r *Repository) LoadBoundForProject(ctx context.Context, projectID, deliveryID, generationID string) (Projection, error) {
	if r == nil {
		return Projection{}, ErrInvalid
	}
	return LoadBoundForProject(ctx, r.db, projectID, deliveryID, generationID)
}

func (r *Repository) LoadBound(ctx context.Context, deliveryID, generationID string) (Projection, error) {
	if r == nil {
		return Projection{}, ErrInvalid
	}
	return LoadBound(ctx, r.db, deliveryID, generationID)
}

// Traverse executes a bounded recursive CTE over an exact project scope or
// delivery/generation binding. Every recursive step joins the caller's
// allow-set, preventing hidden nodes from being used as transit into visible
// results.
func Traverse(ctx context.Context, db DB, in TraversalInput) ([]TraversalNode, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	if !validScope(in.ProjectID) {
		return nil, ErrInvalid
	}
	if _, err := projectgraph.NewResourceID(in.RootID); err != nil {
		return nil, ErrInvalid
	}
	if in.Direction != DirectionUpstream && in.Direction != DirectionDownstream {
		return nil, ErrInvalid
	}
	if in.MaxDepth < 0 || in.MaxDepth > maxTraversalDepth || in.MaxNodes <= 0 || in.MaxNodes > maxTraversalNodes {
		return nil, ErrTraversalLimit
	}
	if in.MaxEdges == 0 {
		in.MaxEdges = maxTraversalEdges
	}
	if in.MaxEdges < 0 || in.MaxEdges > maxTraversalEdges {
		return nil, ErrTraversalLimit
	}
	allowed, err := normalizeAllowed(in.AllowedResourceIDs)
	if err != nil {
		return nil, err
	}
	if len(allowed) == 0 {
		return nil, ErrForbidden
	}
	// The recursive relation is deduplicated by (node, depth). Bounding the
	// caller-supplied allow-set by MaxNodes therefore bounds the work at
	// MaxDepth*MaxNodes even for high-fan-in DAGs.
	if len(allowed) > in.MaxNodes {
		return nil, ErrTraversalLimit
	}
	if !contains(allowed, in.RootID) {
		return nil, ErrForbidden
	}
	q := lineagedb.New(db)
	var digest string
	if in.ScopeID != "" {
		if !validScope(in.ScopeID) || in.DeliveryID != "" || in.GenerationID != "" {
			return nil, ErrInvalid
		}
		var err error
		digest, err = q.GetRevisionDigest(ctx, lineagedb.GetRevisionDigestParams{ProjectID: in.ProjectID, ScopeID: in.ScopeID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
	} else {
		if !validScope(in.DeliveryID) || !validScope(in.GenerationID) {
			return nil, ErrInvalid
		}
		var err error
		digest, err = q.GetBindingDigestForProject(ctx, lineagedb.GetBindingDigestForProjectParams{ProjectID: in.ProjectID, DeliveryID: in.DeliveryID, GenerationID: in.GenerationID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
	}
	// Traversal is a read of the compiler artifact, never a second source of
	// graph truth. Verify the complete projection before exposing any node.
	if _, err := Load(ctx, db, digest); err != nil {
		return nil, err
	}
	// Validate the root exists in this generation before running the walk.
	exists, err := q.NodeExists(ctx, lineagedb.NodeExistsParams{GraphDigest: digest, NodeID: in.RootID})
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	// This preflight counts every edge reachable by the same bounded,
	// deduplicating walk. It is intentionally executed before materializing
	// node payloads, so an explicit edge budget bounds recursive work.
	var edgeCount int64
	countParams := lineagedb.CountUpstreamEdgesParams{
		GraphDigest: digest, ProjectID: in.ProjectID, RootID: in.RootID,
		Allowed: allowed, MaxDepth: int32(in.MaxDepth),
	}
	if in.Direction == DirectionUpstream {
		edgeCount, err = q.CountUpstreamEdges(ctx, countParams)
	} else {
		edgeCount, err = q.CountDownstreamEdges(ctx, lineagedb.CountDownstreamEdgesParams{
			GraphDigest: digest, ProjectID: in.ProjectID, RootID: in.RootID,
			Allowed: allowed, MaxDepth: int32(in.MaxDepth),
		})
	}
	if err != nil {
		return nil, err
	}
	if edgeCount > int64(in.MaxEdges) {
		return nil, fmt.Errorf("%w: traversal exceeds %d edges", ErrTraversalLimit, in.MaxEdges)
	}
	rowLimit := int32(in.MaxNodes + 1)
	var out []TraversalNode
	if in.Direction == DirectionUpstream {
		rows, queryErr := q.TraverseUpstream(ctx, lineagedb.TraverseUpstreamParams{
			GraphDigest: digest, ProjectID: in.ProjectID, RootID: in.RootID,
			Allowed: allowed, MaxDepth: int32(in.MaxDepth), RowLimit: rowLimit,
		})
		if queryErr != nil {
			return nil, queryErr
		}
		out = make([]TraversalNode, 0, len(rows))
		for _, row := range rows {
			out = append(out, TraversalNode{Node: Node{ID: row.NodeID, ResourceKind: row.ResourceKind, IdentityDigest: row.IdentityDigest, Properties: append(json.RawMessage(nil), row.Properties...)}, Depth: int(row.Depth)})
		}
	} else {
		rows, queryErr := q.TraverseDownstream(ctx, lineagedb.TraverseDownstreamParams{
			GraphDigest: digest, ProjectID: in.ProjectID, RootID: in.RootID,
			Allowed: allowed, MaxDepth: int32(in.MaxDepth), RowLimit: rowLimit,
		})
		if queryErr != nil {
			return nil, queryErr
		}
		out = make([]TraversalNode, 0, len(rows))
		for _, row := range rows {
			out = append(out, TraversalNode{Node: Node{ID: row.NodeID, ResourceKind: row.ResourceKind, IdentityDigest: row.IdentityDigest, Properties: append(json.RawMessage(nil), row.Properties...)}, Depth: int(row.Depth)})
		}
	}
	if len(out) > in.MaxNodes {
		return nil, fmt.Errorf("%w: traversal exceeds %d nodes", ErrTraversalLimit, in.MaxNodes)
	}
	return out, nil
}

func (r *Repository) Traverse(ctx context.Context, in TraversalInput) ([]TraversalNode, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	return Traverse(ctx, r.db, in)
}

// Impact returns bounded downstream impact for the supplied scope/binding.
// It is intentionally just the downstream traversal contract so callers do
// not accidentally maintain a second graph-walking implementation.
func Impact(ctx context.Context, db DB, in TraversalInput) ([]TraversalNode, error) {
	in.Direction = DirectionDownstream
	return Traverse(ctx, db, in)
}

func (r *Repository) Impact(ctx context.Context, in TraversalInput) ([]TraversalNode, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	return Impact(ctx, r.db, in)
}

func normalizeAllowed(values []string) ([]string, error) {
	set := map[string]struct{}{}
	for _, id := range values {
		canonical, err := projectgraph.NewResourceID(id)
		if err != nil || !validScope(id) {
			return nil, ErrInvalid
		}
		set[canonical.String()] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
func contains(values []string, want string) bool {
	i := sort.SearchStrings(values, want)
	return i < len(values) && values[i] == want
}
