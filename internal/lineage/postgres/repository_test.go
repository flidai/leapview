package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgxpool"
)

func lineageTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "lineage-runtime-secret", Login: true})
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
			err = Persist(ctx, tx, projection)
			if err == nil {
				err = PersistBinding(ctx, tx, Binding{DeliveryID: "delivery-concurrent", GenerationID: "generation-1", ProjectID: projection.ProjectID, GraphDigest: projection.Digest})
			}
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
	upstream, err := Traverse(ctx, p, TraversalInput{ProjectID: "project:p", DeliveryID: "delivery-concurrent", GenerationID: "generation-1", RootID: "dashboard:d", Direction: DirectionUpstream, MaxDepth: 3, MaxNodes: 10, AllowedResourceIDs: []string{"dashboard:d", "model:m", "source:s"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(upstream) != 3 || upstream[0].Node.ID != "dashboard:d" {
		t.Fatalf("upstream = %#v", upstream)
	}
	if _, err := Traverse(ctx, p, TraversalInput{ProjectID: "project:p", DeliveryID: "delivery-concurrent", GenerationID: "generation-1", RootID: "dashboard:d", Direction: DirectionUpstream, MaxDepth: 3, MaxNodes: 2, AllowedResourceIDs: []string{"dashboard:d", "model:m", "source:s"}}); !errors.Is(err, ErrTraversalLimit) {
		t.Fatalf("traversal overflow error = %v", err)
	}
	if _, err := Traverse(ctx, p, TraversalInput{ProjectID: "project:p", DeliveryID: "delivery-concurrent", GenerationID: "generation-1", RootID: "dashboard:d", Direction: DirectionUpstream, MaxDepth: 3, MaxNodes: 10, MaxEdges: 1, AllowedResourceIDs: []string{"dashboard:d", "model:m", "source:s"}}); !errors.Is(err, ErrTraversalLimit) {
		t.Fatalf("edge traversal overflow error = %v", err)
	}
	filtered, err := Traverse(ctx, p, TraversalInput{ProjectID: "project:p", DeliveryID: "delivery-concurrent", GenerationID: "generation-1", RootID: "dashboard:d", Direction: DirectionUpstream, MaxDepth: 3, MaxNodes: 10, AllowedResourceIDs: []string{"dashboard:d", "model:m"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("filtered traversal = %#v", filtered)
	}
	if _, err := Traverse(ctx, p, TraversalInput{ProjectID: "project:p", DeliveryID: "delivery-concurrent", GenerationID: "generation-1", RootID: "dashboard:d", Direction: DirectionUpstream, MaxDepth: 3, MaxNodes: 10}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("empty allow-set error = %v", err)
	}
}

func TestRevisionPublicationReplacementRollbackAndProjectIsolation(t *testing.T) {
	p := lineageTestDB(t)
	ctx := context.Background()
	graph := sampleGraph(t)
	projection, err := FromGraph(graph)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := PublishRevision(ctx, tx, RevisionInput{ProjectID: graph.ProjectID().String(), ScopeID: "scope-a", Projection: projection})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if first.RevisionID != 1 || first.ValidTo != nil {
		t.Fatalf("first revision = %#v", first)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := CurrentRevision(ctx, p, graph.ProjectID().String(), "scope-a")
	if err != nil || current.RevisionID != 1 {
		t.Fatalf("current revision = %#v, err=%v", current, err)
	}
	// Concurrent retries of the same publication serialize on the scope lock
	// and converge on one idempotent revision.
	const workers = 4
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			publication, beginErr := p.Begin(ctx)
			if beginErr != nil {
				errCh <- beginErr
				return
			}
			_, publishErr := PublishRevision(ctx, publication, RevisionInput{ProjectID: graph.ProjectID().String(), ScopeID: "scope-concurrent", Projection: projection})
			if publishErr == nil {
				publishErr = publication.Commit(ctx)
			} else {
				_ = publication.Rollback(ctx)
			}
			errCh <- publishErr
		}()
	}
	wg.Wait()
	close(errCh)
	for publishErr := range errCh {
		if publishErr != nil {
			t.Fatal(publishErr)
		}
	}
	if concurrent, err := CurrentRevision(ctx, p, graph.ProjectID().String(), "scope-concurrent"); err != nil || concurrent.RevisionID != 1 {
		t.Fatalf("concurrent current revision = %#v, err=%v", concurrent, err)
	}

	// A second graph replaces the current row atomically and leaves revision 1
	// closed. A caller rollback must not expose either graph or revision.
	resources := graph.Resources()
	resources[1].Metadata.Description = "second revision"
	graph2, err := projectgraph.NewProjectGraph(resources, graph.Edges())
	if err != nil {
		t.Fatal(err)
	}
	projection2, err := FromGraph(graph2)
	if err != nil {
		t.Fatal(err)
	}
	tx, err = p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PublishRevision(ctx, tx, RevisionInput{ProjectID: graph2.ProjectID().String(), ScopeID: "scope-a", Projection: projection2})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if second.RevisionID != 2 {
		t.Fatalf("second revision = %#v", second)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := CurrentRevision(ctx, p, graph2.ProjectID().String(), "scope-a"); err != nil || got.GraphDigest != projection2.Digest {
		t.Fatalf("replacement current = %#v, err=%v", got, err)
	}
	// Re-publishing a previously active graph is a legitimate new revision
	// (for example, an operational rollback), not a digest conflict.
	tx, err = p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	third, err := PublishRevision(ctx, tx, RevisionInput{ProjectID: graph.ProjectID().String(), ScopeID: "scope-a", Projection: projection})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if third.RevisionID != 3 || third.GraphDigest != projection.Digest {
		t.Fatalf("rollback revision = %#v", third)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := CurrentRevision(ctx, p, graph.ProjectID().String(), "scope-a"); err != nil || got.RevisionID != 3 || got.GraphDigest != projection.Digest {
		t.Fatalf("rollback current = %#v, err=%v", got, err)
	}
	var closed int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM lineage.revisions WHERE project_id=$1 AND scope_id='scope-a' AND valid_to IS NOT NULL`, graph.ProjectID().String()).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if closed != 2 {
		t.Fatalf("closed revisions = %d", closed)
	}

	rollback, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PublishRevision(ctx, rollback, RevisionInput{ProjectID: graph2.ProjectID().String(), ScopeID: "scope-rollback", Projection: projection2}); err != nil {
		_ = rollback.Rollback(ctx)
		t.Fatal(err)
	}
	if err := rollback.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := CurrentRevision(ctx, p, graph2.ProjectID().String(), "scope-rollback"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled back revision error = %v", err)
	}

	// A project-scoped traversal cannot resolve another project's scope even
	// when the caller knows its scope identifier.
	if _, err := Traverse(ctx, p, TraversalInput{ProjectID: "project:other", ScopeID: "scope-a", RootID: "dashboard:d", Direction: DirectionUpstream, MaxDepth: 3, MaxNodes: 10, AllowedResourceIDs: []string{"dashboard:d"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project traversal error = %v", err)
	}
}

func TestTraversalDiamondIsDeduplicatedAndEdgeBounded(t *testing.T) {
	p := lineageTestDB(t)
	resources := []projectgraph.Resource{
		{ID: "project:diamond", Kind: projectgraph.KindProject, Name: "diamond"},
		{ID: "dashboard:root", Kind: projectgraph.KindDashboard, Name: "root"},
		{ID: "model:left", Kind: projectgraph.KindModel, Name: "left"},
		{ID: "model:right", Kind: projectgraph.KindModel, Name: "right"},
		{ID: "source:shared", Kind: projectgraph.KindSource, Name: "shared"},
	}
	edges := []projectgraph.Edge{
		{From: "dashboard:root", To: "model:left"},
		{From: "dashboard:root", To: "model:right"},
		{From: "model:left", To: "source:shared"},
		{From: "model:right", To: "source:shared"},
	}
	g, err := projectgraph.NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := FromGraph(g)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := Persist(context.Background(), tx, projection); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := PersistBinding(context.Background(), tx, Binding{DeliveryID: "diamond-delivery", GenerationID: "diamond-generation", ProjectID: "project:diamond", GraphDigest: projection.Digest}); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	allowed := []string{"dashboard:root", "model:left", "model:right", "source:shared"}
	got, err := Traverse(context.Background(), p, TraversalInput{ProjectID: "project:diamond", DeliveryID: "diamond-delivery", GenerationID: "diamond-generation", RootID: "dashboard:root", Direction: DirectionUpstream, MaxDepth: 3, MaxNodes: 10, MaxEdges: 4, AllowedResourceIDs: allowed})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[len(got)-1].Node.ID != "source:shared" {
		t.Fatalf("diamond traversal = %#v", got)
	}
	if _, err := Traverse(context.Background(), p, TraversalInput{ProjectID: "project:diamond", DeliveryID: "diamond-delivery", GenerationID: "diamond-generation", RootID: "dashboard:root", Direction: DirectionUpstream, MaxDepth: 3, MaxNodes: 10, MaxEdges: 3, AllowedResourceIDs: allowed}); !errors.Is(err, ErrTraversalLimit) {
		t.Fatalf("diamond edge bound error = %v", err)
	}
}

func TestRuntimePublicationRoleBoundaryAndCompositeProjectFK(t *testing.T) {
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "lineage-runtime-secret", Login: true})
	db := h.NewDatabase(t, "lineage_role_test")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	tx, err := admin.Begin(t.Context())
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
	runtimeDB, err := pgxpool.New(t.Context(), db.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeDB.Close()
	graph := sampleGraph(t)
	projection, err := FromGraph(graph)
	if err != nil {
		t.Fatal(err)
	}
	runtimeTx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := Persist(t.Context(), runtimeTx, projection); err != nil {
		_ = runtimeTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := PersistBinding(t.Context(), runtimeTx, Binding{DeliveryID: "runtime-delivery", GenerationID: "runtime-generation", ProjectID: projection.ProjectID, GraphDigest: projection.Digest}); err != nil {
		_ = runtimeTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := PublishRevision(t.Context(), runtimeTx, RevisionInput{ProjectID: projection.ProjectID, ScopeID: "runtime-scope", Projection: projection}); err != nil {
		_ = runtimeTx.Rollback(t.Context())
		t.Fatalf("runtime publication failed: %v", err)
	}
	if err := runtimeTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE lineage.revisions SET valid_to=clock_timestamp() WHERE project_id=$1`, projection.ProjectID); err == nil {
		t.Fatal("runtime role directly updated revisions")
	}
	if _, err := runtimeDB.Exec(t.Context(), `INSERT INTO lineage.revisions(project_id,scope_id,revision_id,graph_digest) VALUES ($1,'forbidden',1,$2)`, projection.ProjectID, projection.Digest); err == nil {
		t.Fatal("runtime role directly inserted revisions")
	}

	badDigest := "sha256:" + strings.Repeat("0", 64)
	if _, err := admin.Exec(t.Context(), `INSERT INTO lineage.graphs(graph_digest,graph_version,project_id,node_count,edge_count) VALUES ($1,1,'project:one',1,0)`, badDigest); err != nil {
		t.Fatal(err)
	}
	identity, err := IdentityDigest(projectgraph.KindProject, projectgraph.ResourceID("project:two"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO lineage.nodes(graph_digest,project_id,node_id,resource_kind,identity_digest,properties) VALUES ($1,'project:two','project:two','project',$2,'{}'::jsonb)`, badDigest, identity); err == nil {
		t.Fatal("cross-project node insert bypassed composite foreign key")
	}
}
