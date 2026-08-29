package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgxpool"
)

func lineageTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "lineage_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func sampleGraph(t *testing.T) projectgraph.ProjectGraph {
	t.Helper()
	resources := []projectgraph.Resource{
		{ID: projectgraph.ResourceID("project:p"), Kind: projectgraph.KindProject, Name: "project", Metadata: projectgraph.Metadata{Tags: []string{"z", "a"}}},
		{ID: projectgraph.ResourceID("source:s"), Kind: projectgraph.KindSource, Name: "source"},
		{ID: projectgraph.ResourceID("model:m"), Kind: projectgraph.KindModel, Name: "model"},
		{ID: projectgraph.ResourceID("dashboard:d"), Kind: projectgraph.KindDashboard, Name: "dashboard"},
	}
	edges := []projectgraph.Edge{
		{From: projectgraph.ResourceID("model:m"), To: projectgraph.ResourceID("source:s"), Relation: "reads"},
		{From: projectgraph.ResourceID("dashboard:d"), To: projectgraph.ResourceID("model:m"), Relation: "uses"},
	}
	g, err := projectgraph.NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestProjectionDeterministicAndCycleValidation(t *testing.T) {
	g := sampleGraph(t)
	a, err := FromGraph(g)
	if err != nil {
		t.Fatal(err)
	}
	b, err := FromGraph(g)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest || string(a.CanonicalBytes()) != string(b.CanonicalBytes()) {
		t.Fatalf("projection is not deterministic")
	}
	projectID := "project:p"
	nodes := cloneNodes(a.Nodes)
	edges := cloneEdges(a.Edges)
	edges = append(edges, Edge{From: "source:s", To: "dashboard:d"})
	if _, err := NewProjectionFromRows(projectID, nodes, edges); !errors.Is(err, ErrCycle) {
		t.Fatalf("cycle error = %v", err)
	}
	for i := range nodes {
		if nodes[i].ID == "model:m" {
			nodes[i].Properties = []byte(`{"outer":{"x":1,"x":2}}`)
		}
	}
	if _, err := NewProjectionFromRows(projectID, nodes, a.Edges); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate property key error = %v", err)
	}
}

func TestProjectionPersistenceLoadTamperRollbackAndBinding(t *testing.T) {
	p := lineageTestDB(t)
	g := sampleGraph(t)
	projection, err := FromGraph(g)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PersistGraph(ctx, tx, g, Binding{DeliveryID: "delivery-1", GenerationID: "generation-1", GraphDigest: projection.Digest}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(ctx, p, projection.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Digest != projection.Digest || len(loaded.Nodes) != len(projection.Nodes) {
		t.Fatalf("loaded projection = %#v", loaded)
	}
	if _, err := LoadBound(ctx, p, "delivery-1", "wrong-generation"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong generation error = %v", err)
	}
	// Runtime mutation is rejected by the database. Simulate a privileged
	// storage fault only after proving the immutable-row trigger is active.
	if _, err := p.Exec(ctx, `UPDATE lineage.nodes SET properties='{"name":"tampered"}'::jsonb WHERE graph_digest=$1 AND node_id='model:m'`, projection.Digest); err != nil {
		// Expected.
	} else {
		t.Fatal("lineage node update bypassed immutability trigger")
	}
	if _, err := p.Exec(ctx, `ALTER TABLE lineage.nodes DISABLE TRIGGER lineage_nodes_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE lineage.nodes SET properties='{"name":"tampered"}'::jsonb WHERE graph_digest=$1 AND node_id='model:m'`, projection.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `ALTER TABLE lineage.nodes ENABLE TRIGGER lineage_nodes_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(ctx, p, projection.Digest); !errors.Is(err, ErrTampered) {
		t.Fatalf("tamper error = %v", err)
	}
	conflictTx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := Persist(ctx, conflictTx, projection); !errors.Is(err, ErrConflict) {
		_ = conflictTx.Rollback(ctx)
		t.Fatalf("changed rows persistence error = %v", err)
	}
	_ = conflictTx.Rollback(ctx)

	// Restore the fixture, then prove a caller rollback leaves a newly
	// projected graph absent.
	var originalProperties []byte
	for _, node := range projection.Nodes {
		if node.ID == "model:m" {
			originalProperties = node.Properties
		}
	}
	if _, err := p.Exec(ctx, `ALTER TABLE lineage.nodes DISABLE TRIGGER lineage_nodes_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE lineage.nodes SET properties=$1::jsonb WHERE graph_digest=$2 AND node_id='model:m'`, originalProperties, projection.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `ALTER TABLE lineage.nodes ENABLE TRIGGER lineage_nodes_immutable`); err != nil {
		t.Fatal(err)
	}
	resources := g.Resources()
	for i := range resources {
		if resources[i].ID == projectgraph.ResourceID("model:m") {
			resources[i].Metadata.Description = "rollback-only"
		}
	}
	rollbackGraph, err := projectgraph.NewProjectGraph(resources, g.Edges())
	if err != nil {
		t.Fatal(err)
	}
	rollbackProjection, err := FromGraph(rollbackGraph)
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := Persist(ctx, rollback, rollbackProjection); err != nil {
		t.Fatal(err)
	}
	if err := rollback.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(ctx, p, rollbackProjection.Digest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled back projection error = %v", err)
	}
}

func TestProjectionConcurrentIdempotentPersistenceAndTraversalSecurity(t *testing.T) {
	p := lineageTestDB(t)
	g := sampleGraph(t)
	projection, err := FromGraph(g)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const workers = 4
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := p.Begin(ctx)
			if err != nil {
				errs <- err
				return
			}
			_, err = PersistGraph(ctx, tx, g, Binding{DeliveryID: "delivery-concurrent", GenerationID: "generation-1", GraphDigest: projection.Digest})
			if err == nil {
				err = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(ctx)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	upstream, err := Traverse(ctx, p, TraversalInput{DeliveryID: "delivery-concurrent", GenerationID: "generation-1", RootID: "dashboard:d", Direction: DirectionUpstream, MaxDepth: 3, MaxNodes: 10, AllowedResourceIDs: []string{"dashboard:d", "model:m", "source:s"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(upstream) != 3 || upstream[0].Node.ID != "dashboard:d" {
		t.Fatalf("upstream = %#v", upstream)
	}
	if _, err := Traverse(ctx, p, TraversalInput{DeliveryID: "delivery-concurrent", GenerationID: "generation-1", RootID: "dashboard:d", Direction: DirectionUpstream, MaxDepth: 3, MaxNodes: 2, AllowedResourceIDs: []string{"dashboard:d", "model:m", "source:s"}}); !errors.Is(err, ErrTraversalLimit) {
		t.Fatalf("traversal overflow error = %v", err)
	}
	filtered, err := Traverse(ctx, p, TraversalInput{DeliveryID: "delivery-concurrent", GenerationID: "generation-1", RootID: "dashboard:d", Direction: DirectionUpstream, MaxDepth: 3, MaxNodes: 10, AllowedResourceIDs: []string{"dashboard:d", "model:m"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("filtered traversal = %#v", filtered)
	}
	if _, err := Traverse(ctx, p, TraversalInput{DeliveryID: "delivery-concurrent", GenerationID: "generation-1", RootID: "dashboard:d", Direction: DirectionUpstream, MaxDepth: 3, MaxNodes: 10}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("empty allow-set error = %v", err)
	}
}
