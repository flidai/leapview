package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	materialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

// Native credentials and current policy cross the mounted router over HTTP.
// API calls deliberately bypass the dashboard result cache.
func TestCurrentPolicyRevocationDeniesScopedSemanticHTTPQuery(t *testing.T) {
	currentPolicyRevocationFixture(t, false)
}

// The dashboard operation uses the assembled product decorators, planner and
// result cache with credentials resolved by PostgreSQL. This is a direct
// materialization test, not mounted dashboard HTTP, browser or row-policy proof.
func TestCurrentPolicyRevocationDeniesWarmedDashboardQuery(t *testing.T) {
	currentPolicyRevocationFixture(t, true)
}

func currentPolicyRevocationFixture(t *testing.T, dashboardCache bool) {
	t.Helper()
	ctx := t.Context()
	store := testStore(t)
	repo := store.fixture.Graph.Access
	target := testPrincipal(t, ctx, store, "cache-target@example.test", "Cache target")
	control := testPrincipal(t, ctx, store, "cache-control@example.test", "Cache control")
	require.NotEqual(t, target.ID, control.ID)
	resource, err := access.NewResourceRef("test", projectgraph.KindSemanticModel)
	require.NoError(t, err)
	read, err := access.NewExactPermissionPair(access.ActionSemanticRead, testProjectID, resource)
	require.NoError(t, err)
	query, err := access.NewExactPermissionPair(access.ActionSemanticQuery, testProjectID, resource)
	require.NoError(t, err)
	tokenPairs, err := access.RequiredPermissionPairs(query)
	require.NoError(t, err)
	tokenPairs = append(tokenPairs, read)

	var bindings []access.RoleBinding
	var capturedGrants []accesssnapshot.Grant
	var nativeGrants []access.AuthorizationGrant
	for index, principal := range []access.Principal{target, control} {
		subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principal.ID}
		binding, err := access.NewTypedRoleBinding([]string{"cache-target-query", "cache-control-query"}[index], "query authority", subject, access.PermissionRoleExplorer, testProjectID)
		require.NoError(t, err)
		bindings = append(bindings, binding)
		// Metadata authority survives removal of the independent query preset.
		id := []string{"cache-target-metadata", "cache-control-metadata"}[index]
		grant, err := accesssnapshot.NewTypedGrant(id, "metadata authority", subject, []access.PermissionPair{read})
		require.NoError(t, err)
		capturedGrants = append(capturedGrants, grant)
		nativeGrants = append(nativeGrants, access.AuthorizationGrant{ID: id, Name: grant.Name, Subject: subject, Resource: resource,
			PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{read}})
	}
	host := cachedRevocationHost(t, store, bindings, capturedGrants)
	lease, err := host.Acquire(ctx)
	require.NoError(t, err)
	identity := lease.Identity()
	lease.Release()
	scope := access.AuthorizationPolicyScope{TargetID: "app-test-target", ProjectID: testProjectID.String(), Environment: identity.Environment}
	policy, err := repo.InitializeAuthorizationPolicyFromServingPolicy(ctx, scope, identity.GenerationID, bindings)
	require.NoError(t, err)
	for _, grant := range nativeGrants {
		policy, err = repo.UpsertAuthorizationGrant(ctx, access.AuthorizationGrantInput{
			Scope: scope, Grant: grant, ExpectedRevision: policy.Revision, IdempotencyKey: "seed-" + grant.ID,
		})
		require.NoError(t, err)
	}
	host.SetAuthorizationSnapshotFilter(currentTargetAuthorizationFilter(repo, scope.TargetID, testProjectID, scope.Environment))
	metrics := newCachedRevocationMetrics(t, identity)
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	options := testStoreOptions(store, assemblyConfig{
		Auth: auth, RuntimeHost: host, DefaultEnvironment: identity.Environment,
	})
	application := assembleRuntime(metrics, options)
	t.Cleanup(application.workloadController().Close)
	server := httptest.NewServer(application.Routes())
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 10 * time.Second
	t.Cleanup(client.CloseIdleConnections)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	type credential struct{ principalID, bearer string }
	issue := func(principal access.Principal) credential {
		bearer, token := testScopedAPIToken(t, ctx, store, access.ScopedAPITokenInput{
			PrincipalID: principal.ID, Name: "cached-query", Permissions: tokenPairs,
		})
		require.Equal(t, access.PermissionCatalogProfile, token.PermissionProfile)
		require.Equal(t, principal.ID, token.PrincipalID)
		subjects, err := options.AccessModule.AuthorizationSubjects(ctx, principal.ID)
		require.NoError(t, err)
		require.Equal(t, []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: principal.ID}}, subjects)
		lease, err := host.Acquire(ctx)
		require.NoError(t, err)
		snapshot := lease.(interface {
			AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
		}).AuthorizationSnapshot()
		lease.Release()
		require.Equal(t, identity, snapshot.Identity())
		effective, err := snapshot.EffectiveTypedPermissions(subjects)
		require.NoError(t, err)
		for _, pair := range tokenPairs {
			require.True(t, access.PermissionSetAllows(effective, pair), "current principal authority lacks %s", pair.Action)
			require.True(t, access.PermissionSetAllows(token.Permissions, pair), "native token ceiling lacks %s", pair.Action)
		}
		t.Logf("native token and current role independently allow exact query: intersectionAllowsQuery=%v", access.PermissionSetAllows(access.IntersectPermissionPairs(token.Permissions, effective), query))
		return credential{principal.ID, bearer}
	}
	targetCredential, controlCredential := issue(target), issue(control)
	request := func(credential credential, method, path, body string, wantStatus int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+credential.bearer)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Serving-Snapshot", identity.GenerationID)
		response, err := client.Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		require.NoError(t, err)
		require.Equal(t, wantStatus, response.StatusCode, "%s %s body=%s", method, path, payload)
		return payload
	}
	const path = "/api/v1/semantic-models/test/query"
	const body = `{"metrics":[{"field":"order_count"}],"limit":1}`
	dashboardQuery := func(credential credential) (dataquery.Result, error) {
		t.Helper()
		resolved, err := repo.CredentialForAPIToken(ctx, credential.bearer)
		require.NoError(t, err)
		require.Equal(t, credential.principalID, resolved.Principal.ID)
		principal := resolved.Principal
		queryCtx := accessmodule.WithPrincipal(ctx, accessmodule.Principal{
			ID: principal.ID, Kind: principal.Kind, Email: principal.Email, DisplayName: principal.DisplayName,
			CreatedAt: principal.CreatedAt, UpdatedAt: principal.UpdatedAt,
		})
		queryCtx = accessmodule.WithAPICredential(queryCtx, resolved)
		return application.runtime.metrics.ExecuteDataQuery(queryCtx, dataquery.Query{
			ProjectID: testProjectID, Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardAggregate,
			ModelID: "test", Kind: dataquery.KindSemanticAggregate, Metrics: []dataquery.Field{{Field: "order_count", Alias: "order_count"}}, Limit: 1,
		})
	}
	assertQuery := func(credential credential, wantOutcome string) {
		t.Helper()
		before := metrics.executionCount()
		if dashboardCache {
			result, err := dashboardQuery(credential)
			require.NoError(t, err)
			require.Equal(t, []dataquery.Row{{"order_count": int64(42)}}, result.Rows)
		} else {
			payload := request(credential, http.MethodPost, path, body, http.StatusOK)
			var response struct {
				Rows [][]any `json:"rows"`
			}
			require.NoError(t, json.Unmarshal(payload, &response))
			require.Equal(t, [][]any{{"42"}}, response.Rows, "query payload=%s", payload)
		}
		require.Equal(t, before+1, metrics.executionCount(), "query did not execute through the product materializer")
		require.Equal(t, wantOutcome, metrics.lastOutcome())
	}
	for _, credential := range []credential{targetCredential, controlCredential} {
		physical := metrics.database.queries.Load()
		if dashboardCache {
			assertQuery(credential, dataquery.CacheMiss)
			require.Equal(t, physical+1, metrics.database.queries.Load())
			assertQuery(credential, dataquery.CacheHit)
			require.Equal(t, physical+1, metrics.database.queries.Load(), "cache hit performed physical execution")
		} else {
			assertQuery(credential, "")
			require.Equal(t, physical+1, metrics.database.queries.Load())
			assertQuery(credential, "")
			require.Equal(t, physical+2, metrics.database.queries.Load(), "API query must bypass the dashboard cache")
		}
	}
	if dashboardCache {
		t.Log("both native principals warmed actual governed dashboard cache entries")
	} else {
		t.Log("both scoped native credentials queried mounted semantic HTTP with explicit cache bypass")
	}
	previous := policy
	policy, err = repo.RemoveAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingDeleteInput{
		Scope: scope, BindingID: bindings[0].ID, ExpectedRevision: previous.Revision, IdempotencyKey: "revoke-target-query",
	})
	require.NoError(t, err)
	current, err := repo.AuthorizationPolicy(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, policy, current)
	require.Equal(t, previous.Revision+1, current.Revision)
	require.NotEqual(t, previous.Digest, current.Digest)
	for _, credential := range []credential{targetCredential, controlCredential} {
		payload := request(credential, http.MethodGet, "/api/v1/me", "", http.StatusOK)
		var principal struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(payload, &principal))
		require.Equal(t, credential.principalID, principal.ID, "credential identity changed after policy mutation")
	}
	physical, executions := metrics.database.queries.Load(), metrics.executionCount()
	if dashboardCache {
		result, err := dashboardQuery(targetCredential)
		var denied queryauthz.DeniedError
		require.ErrorAs(t, err, &denied)
		require.Equal(t, access.ActionSemanticConsume, denied.Action)
		require.Empty(t, result.Rows)
	} else {
		// The generated semantic.query operation rejects before materializer
		// lookup. Its http.Error boundary precedes response normalization.
		denied := request(targetCredential, http.MethodPost, path, body, http.StatusForbidden)
		require.Equal(t, "Forbidden\n", string(denied))
	}
	require.Equal(t, executions, metrics.executionCount())
	require.Equal(t, physical, metrics.database.queries.Load())
	if dashboardCache {
		assertQuery(controlCredential, dataquery.CacheHit)
		require.Equal(t, physical, metrics.database.queries.Load(), "unaffected principal lost its warmed entry")
	} else {
		assertQuery(controlCredential, "")
		require.Equal(t, physical+1, metrics.database.queries.Load())
	}
	lease, err = host.Acquire(ctx)
	require.NoError(t, err)
	withSnapshot, ok := lease.(interface {
		AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
	})
	if !ok {
		lease.Release()
		t.Fatal("active lease does not carry authorization")
	}
	active := withSnapshot.AuthorizationSnapshot()
	lease.Release()
	require.Equal(t, identity, active.Identity(), "revocation replaced the serving generation")
	allowed, err := active.AllowsTyped(bindings[0].Subject, query)
	require.NoError(t, err)
	require.False(t, allowed)
	allowed, err = active.AllowsTyped(bindings[0].Subject, read)
	require.NoError(t, err)
	require.True(t, allowed, "revocation unexpectedly removed metadata authority")
	allowed, err = active.AllowsTyped(bindings[1].Subject, query)
	require.NoError(t, err)
	require.True(t, allowed, "revocation changed control authority")
	if dashboardCache {
		t.Log("committed target-only revocation denied a warmed dashboard query while credential and control cache stayed valid")
	} else {
		t.Log("committed target-only revocation denied semantic HTTP while credential and control query stayed valid")
	}
}

func cachedRevocationHost(t *testing.T, store *testControlStore, bindings []access.RoleBinding, grants []accesssnapshot.Grant) *runtimehostmodule.Module {
	t.Helper()
	ctx := t.Context()
	graph, err := testRuntimeGraph()
	require.NoError(t, err)
	state, err := store.states.Create(ctx, servingstate.CreateInput{ProjectID: testProjectID, Environment: "prod", CreatedBy: "cache-fixture", Source: servingstate.SourcePublish})
	require.NoError(t, err)
	_, err = store.states.SaveValidated(ctx, state.ID, servingstate.Validation{
		Digest: graph.Digest(), ManifestJSON: "{}", ProjectID: testProjectID, ProjectDigest: graph.Digest(), Graph: graph,
	}, servingstate.Artifact{ID: "artifact_" + string(state.ID), ServingStateID: state.ID, Digest: graph.Digest(), Format: "test-fixture", Path: "test-fixture.tar.gz", ManifestJSON: "{}"})
	require.NoError(t, err)
	require.NoError(t, store.states.RecordDuckLakeSnapshot(ctx, state.ID, 1))
	_, err = store.states.Activate(ctx, testProjectID, "prod", state.ID, "")
	require.NoError(t, err)
	host, err := runtimehostmodule.Build(ctx, runtimehostmodule.Config{
		States: store.states, ProjectID: testProjectID, Environment: "prod",
		Factory: cachedRevocationFactory{graph: graph, bindings: bindings, grants: grants}, Authorization: testRuntimeAuthorizationInstaller{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, host.Close()) })
	return host
}

type cachedRevocationFactory struct {
	graph    projectgraph.ProjectGraph
	bindings []access.RoleBinding
	grants   []accesssnapshot.Grant
}

func (f cachedRevocationFactory) Prepare(ctx context.Context, input runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	base, err := (testRuntimeFactory{graph: f.graph}).Prepare(ctx, input)
	if err != nil {
		return nil, err
	}
	prepared := base.(testPreparedRuntime)
	prepared.authorization, err = accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(prepared.authorization.Identity(), f.graph, f.bindings, f.grants, nil)
	return prepared, err
}

type cachedRevocationMetrics struct {
	fakeMetrics
	core     *materialize.Runtime
	database *cachedRevocationDatabase
	mu       sync.Mutex
	outcomes []string
}

func newCachedRevocationMetrics(t *testing.T, identity projectgraph.ServingIdentity) *cachedRevocationMetrics {
	t.Helper()
	model := testSemanticModel()
	digest, err := semanticquery.SemanticModelDigest(model)
	require.NoError(t, err)
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{
		Kind: resultidentity.PartitionProduction, TargetID: "app-test-target", ProjectID: identity.ProjectID, Environment: identity.Environment,
	})
	require.NoError(t, err)
	evidence, err := resultidentity.NewEvidence(resultidentity.EvidenceInput{
		SemanticModelID: "test", SemanticModelDigest: digest,
		DatasetRelations:   []resultidentity.DatasetRelation{{Dataset: "orders", Relation: resultidentity.RelationRevision{RelationID: "model.orders", RevisionDigest: "sha256:" + strings.Repeat("a", 64)}}},
		BindingFingerprint: "sha256:" + strings.Repeat("b", 64), RuntimeDigest: "sha256:" + strings.Repeat("c", 64), CapabilityDigest: "sha256:" + strings.Repeat("d", 64),
	})
	require.NoError(t, err)
	database := &cachedRevocationDatabase{}
	core, err := materialize.NewRuntimeView(t.Context(), materialize.RuntimeConfig{
		ServingStateID: identity.GenerationID, ModelID: "test", Model: model, Database: database, Sources: cachedRevocationSources{},
		SnapshotOnly: true, ResultPartition: partition, DependencyEvidence: evidence,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, core.Close()) })
	return &cachedRevocationMetrics{core: core, database: database}
}

func (m *cachedRevocationMetrics) Planner(id string) (consumer.Planner, bool) {
	return m.core.Planner(), id == "test"
}

func (m *cachedRevocationMetrics) ExecuteDataQuery(ctx context.Context, query dataquery.Query) (dataquery.Result, error) {
	result, err := m.core.ExecuteDataQuery(ctx, query)
	m.mu.Lock()
	m.outcomes = append(m.outcomes, result.CacheOutcome)
	m.mu.Unlock()
	return result, err
}

func (m *cachedRevocationMetrics) executionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.outcomes)
}

func (m *cachedRevocationMetrics) lastOutcome() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.outcomes[len(m.outcomes)-1]
}

type cachedRevocationDatabase struct{ queries atomic.Int64 }

func (*cachedRevocationDatabase) Path() string { return "synthetic-cached-revocation" }
func (*cachedRevocationDatabase) Close() error { return nil }
func (*cachedRevocationDatabase) Exec(context.Context, string) error {
	return errors.New("snapshot-only fixture must not execute setup SQL")
}
func (d *cachedRevocationDatabase) QueryArrow(_ context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	d.queries.Add(1)
	if len(plan.Columns) != 1 || plan.Columns[0] != "order_count" {
		return errors.New("unexpected analytical projection")
	}
	builder := array.NewInt64Builder(memory.DefaultAllocator)
	builder.Append(42)
	values := builder.NewArray()
	builder.Release()
	defer values.Release()
	schema := arrow.NewSchema([]arrow.Field{{Name: "order_count", Type: arrow.PrimitiveTypes.Int64}}, nil)
	record := array.NewRecordBatch(schema, []arrow.Array{values}, 1)
	defer record.Release()
	if err := sink.WriteSchema(schema); err != nil {
		return err
	}
	return sink.WriteRecord(record)
}

type cachedRevocationSources struct{}

func (cachedRevocationSources) Prepare(context.Context, *semanticmodel.Model) (materialize.PreparedSources, error) {
	return nil, errors.New("snapshot-only fixture must not prepare mutable sources")
}
