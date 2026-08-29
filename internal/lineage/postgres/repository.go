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
	"sort"
	"strings"

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

// Tx is the native pgx transaction surface required by persistence. pgx.Tx
// and pgxpool.Tx satisfy it; no database/sql adapter is used.
type Tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

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
	GraphDigest  string `json:"graph_digest"`
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
	DeliveryID         string
	GenerationID       string
	RootID             string
	Direction          TraversalDirection
	MaxDepth           int
	MaxNodes           int
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
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

// New creates a repository whose read methods use db.
func New(db DB) *Repository { return &Repository{db: db} }

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
	if ctx == nil {
		ctx = context.Background()
	}
	var err error
	p, err = canonicalProjection(p)
	if err != nil {
		return err
	}
	var inserted int64
	err = tx.QueryRow(ctx, `INSERT INTO lineage.graphs (graph_digest, graph_version, project_id, node_count, edge_count)
VALUES ($1,$2,$3,$4,$5) ON CONFLICT (graph_digest) DO NOTHING RETURNING 1`, p.Digest, p.Version, p.ProjectID, len(p.Nodes), len(p.Edges)).Scan(&inserted)
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
		if _, err := tx.Exec(ctx, `INSERT INTO lineage.nodes (graph_digest,node_id,resource_kind,identity_digest,properties) VALUES ($1,$2,$3,$4,$5)`, p.Digest, n.ID, n.ResourceKind, n.IdentityDigest, n.Properties); err != nil {
			return err
		}
	}
	for _, e := range p.Edges {
		if _, err := tx.Exec(ctx, `INSERT INTO lineage.edges (graph_digest,from_node_id,to_node_id,relation) VALUES ($1,$2,$3,$4)`, p.Digest, e.From, e.To, e.Relation); err != nil {
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
	if ctx == nil {
		ctx = context.Background()
	}
	var inserted int
	err := tx.QueryRow(ctx, `INSERT INTO lineage.bindings (delivery_id,generation_id,graph_digest) VALUES ($1,$2,$3)
ON CONFLICT (delivery_id,generation_id) DO NOTHING RETURNING 1`, b.DeliveryID, b.GenerationID, b.GraphDigest).Scan(&inserted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	var existing string
	if err := tx.QueryRow(ctx, `SELECT graph_digest FROM lineage.bindings WHERE delivery_id=$1 AND generation_id=$2`, b.DeliveryID, b.GenerationID).Scan(&existing); err != nil {
		return err
	}
	if existing != b.GraphDigest {
		return fmt.Errorf("%w: binding (%s,%s)", ErrConflict, b.DeliveryID, b.GenerationID)
	}
	return nil
}

func validScope(v string) bool { return v == strings.TrimSpace(v) && v != "" && len(v) <= 256 }

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
	if ctx == nil {
		ctx = context.Background()
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
	var version, nodeCount, edgeCount int
	var projectID string
	if err := db.QueryRow(ctx, `SELECT graph_version,project_id,node_count,edge_count FROM lineage.graphs WHERE graph_digest=$1`, digest).Scan(&version, &projectID, &nodeCount, &edgeCount); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Projection{}, ErrNotFound
		}
		return Projection{}, err
	}
	if version != CanonicalVersion || nodeCount < 1 || nodeCount > maxProjectionNodes || edgeCount < 0 || edgeCount > maxProjectionEdges {
		return Projection{}, fmt.Errorf("%w: stored graph metadata exceeds bounds", ErrTampered)
	}
	nodes := make([]Node, 0, nodeCount)
	rows, err := db.Query(ctx, `SELECT node_id,resource_kind,identity_digest,properties FROM lineage.nodes WHERE graph_digest=$1 ORDER BY node_id`, digest)
	if err != nil {
		return Projection{}, err
	}
	for rows.Next() {
		var n Node
		var props []byte
		if err := rows.Scan(&n.ID, &n.ResourceKind, &n.IdentityDigest, &props); err != nil {
			rows.Close()
			return Projection{}, err
		}
		n.Properties = append(json.RawMessage(nil), props...)
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Projection{}, err
	}
	rows.Close()
	edges := make([]Edge, 0, edgeCount)
	rows, err = db.Query(ctx, `SELECT from_node_id,to_node_id,relation FROM lineage.edges WHERE graph_digest=$1 ORDER BY from_node_id,to_node_id`, digest)
	if err != nil {
		return Projection{}, err
	}
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.From, &e.To, &e.Relation); err != nil {
			rows.Close()
			return Projection{}, err
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Projection{}, err
	}
	rows.Close()
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
	if ctx == nil {
		ctx = context.Background()
	}
	var digest string
	if err := db.QueryRow(ctx, `SELECT graph_digest FROM lineage.bindings WHERE delivery_id=$1 AND generation_id=$2`, deliveryID, generationID).Scan(&digest); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Projection{}, ErrNotFound
		}
		return Projection{}, err
	}
	return Load(ctx, db, digest)
}

func (r *Repository) LoadBound(ctx context.Context, deliveryID, generationID string) (Projection, error) {
	if r == nil {
		return Projection{}, ErrInvalid
	}
	return LoadBound(ctx, r.db, deliveryID, generationID)
}

// Traverse executes a bounded recursive CTE over an exact bound generation.
// Every recursive step joins the caller's allow-set, preventing hidden nodes
// from being used as transit into visible results.
func Traverse(ctx context.Context, db DB, in TraversalInput) ([]TraversalNode, error) {
	if db == nil || !validScope(in.DeliveryID) || !validScope(in.GenerationID) || !validScope(in.RootID) {
		return nil, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if in.Direction != DirectionUpstream && in.Direction != DirectionDownstream {
		return nil, ErrInvalid
	}
	if in.MaxDepth < 0 || in.MaxDepth > maxTraversalDepth || in.MaxNodes <= 0 || in.MaxNodes > maxTraversalNodes {
		return nil, ErrTraversalLimit
	}
	allowed, err := normalizeAllowed(in.AllowedResourceIDs)
	if err != nil {
		return nil, err
	}
	if len(allowed) == 0 {
		return nil, ErrForbidden
	}
	if !contains(allowed, in.RootID) {
		return nil, ErrForbidden
	}
	var digest string
	if err := db.QueryRow(ctx, `SELECT graph_digest FROM lineage.bindings WHERE delivery_id=$1 AND generation_id=$2`, in.DeliveryID, in.GenerationID).Scan(&digest); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// Traversal is a read of the compiler artifact, never a second source of
	// graph truth. Verify the complete projection before exposing any node.
	if _, err := Load(ctx, db, digest); err != nil {
		return nil, err
	}
	// Validate the root exists in this generation before running the walk.
	var exists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lineage.nodes WHERE graph_digest=$1 AND node_id=$2)`, digest, in.RootID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	var query string
	if in.Direction == DirectionUpstream {
		query = `WITH RECURSIVE allowed(node_id) AS (SELECT unnest($3::text[])), walk(node_id,depth) AS (
SELECT $2::text,0 UNION
SELECT e.to_node_id,w.depth+1 FROM walk w
JOIN lineage.edges e ON e.graph_digest=$1 AND e.from_node_id=w.node_id
JOIN allowed a ON a.node_id=e.to_node_id
WHERE w.depth < $4)
SELECT node_id,resource_kind,identity_digest,properties,depth FROM (
SELECT DISTINCT ON (n.node_id) n.node_id,n.resource_kind,n.identity_digest,n.properties,w.depth
FROM walk w JOIN allowed a ON a.node_id=w.node_id
JOIN lineage.nodes n ON n.graph_digest=$1 AND n.node_id=w.node_id
ORDER BY n.node_id,w.depth) unique_nodes
		ORDER BY depth,node_id LIMIT $5`
	} else {
		query = `WITH RECURSIVE allowed(node_id) AS (SELECT unnest($3::text[])), walk(node_id,depth) AS (
SELECT $2::text,0 UNION
SELECT e.from_node_id,w.depth+1 FROM walk w
JOIN lineage.edges e ON e.graph_digest=$1 AND e.to_node_id=w.node_id
JOIN allowed a ON a.node_id=e.from_node_id
		WHERE w.depth < $4)
SELECT node_id,resource_kind,identity_digest,properties,depth FROM (
SELECT DISTINCT ON (n.node_id) n.node_id,n.resource_kind,n.identity_digest,n.properties,w.depth
FROM walk w JOIN allowed a ON a.node_id=w.node_id
JOIN lineage.nodes n ON n.graph_digest=$1 AND n.node_id=w.node_id
ORDER BY n.node_id,w.depth) unique_nodes
		ORDER BY depth,node_id LIMIT $5`
	}
	rows, err := db.Query(ctx, query, digest, in.RootID, allowed, in.MaxDepth, in.MaxNodes+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TraversalNode, 0, in.MaxNodes)
	for rows.Next() {
		var n Node
		var props []byte
		var depth int
		if err := rows.Scan(&n.ID, &n.ResourceKind, &n.IdentityDigest, &props, &depth); err != nil {
			return nil, err
		}
		n.Properties = append(json.RawMessage(nil), props...)
		out = append(out, TraversalNode{Node: n, Depth: depth})
		if len(out) > in.MaxNodes {
			return nil, fmt.Errorf("%w: traversal exceeds %d nodes", ErrTraversalLimit, in.MaxNodes)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repository) Traverse(ctx context.Context, in TraversalInput) ([]TraversalNode, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	return Traverse(ctx, r.db, in)
}

func normalizeAllowed(values []string) ([]string, error) {
	set := map[string]struct{}{}
	for _, id := range values {
		if !validScope(id) {
			return nil, ErrInvalid
		}
		set[id] = struct{}{}
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
