package materialize

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
)

type semanticConsumerAuthority struct {
	mu          sync.Mutex
	registry    access.SemanticAttributeRegistrySnapshot
	registryErr error
	resolutions []access.SemanticAttributeResolution
	resolveErr  error
	calls       int
}

func (a *semanticConsumerAuthority) SemanticAttributeRegistry(context.Context) (access.SemanticAttributeRegistrySnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.registry, a.registryErr
}

func (a *semanticConsumerAuthority) ResolveSemanticAttributes(context.Context) (access.SemanticAttributeResolution, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.resolveErr != nil {
		return access.SemanticAttributeResolution{}, a.resolveErr
	}
	if len(a.resolutions) == 0 {
		return access.SemanticAttributeResolution{}, errors.New("no semantic resolution configured")
	}
	index := a.calls
	a.calls++
	if index >= len(a.resolutions) {
		index = len(a.resolutions) - 1
	}
	return a.resolutions[index], nil
}

func (a *semanticConsumerAuthority) resolveCalls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func semanticConsumerResolution(t testing.TB, subjectID string) access.SemanticAttributeResolution {
	t.Helper()
	definition := access.SemanticAttributeDefinition{
		ID:                "definition-region",
		Name:              "region",
		Type:              semanticvalue.TypeString,
		Shape:             access.SemanticAttributeScalar,
		Profile:           semanticvalue.Profile,
		DefinitionVersion: 1,
		LifecycleState:    access.SemanticAttributeActive,
		Enabled:           true,
	}
	values, digest, err := access.CanonicalSemanticAttributeValues(definition, "us")
	if err != nil {
		t.Fatalf("canonical semantic attribute value: %v", err)
	}
	return access.SemanticAttributeResolution{
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: subjectID},
		Registry: access.SemanticAttributeRegistrySnapshot{
			State:       access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 7, Digest: "sha256:registry"},
			Definitions: []access.SemanticAttributeDefinition{definition},
		},
		ControlState: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 11, Digest: "sha256:control"},
		Attributes: []access.EffectiveSemanticAttribute{{
			DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion,
			Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: digest, Source: "direct",
		}},
	}
}

func semanticConsumerProtectedModel() *semanticmodel.Model {
	model := ownershipModel()
	model.AccessGrants = map[string]semanticmodel.SemanticAccessGrantSpec{
		"regiongrant": {UserAttribute: "region", AllowedValues: []any{"us"}},
		"membergrant": {UserAttribute: "region", AllowedValues: []any{"member"}},
	}
	dataset := model.Datasets["orders"]
	dataset.RequiredAccessGrants = []string{"regiongrant"}
	model.Datasets["orders"] = dataset
	model.Metrics = map[string]semanticmodel.Metric{
		"private_count": {
			Type: "aggregate", Dataset: "orders", Aggregation: "count",
			Input: &semanticmodel.MetricInput{Field: "orders.id"}, RequiredAccessGrants: []string{"membergrant"},
		},
	}
	return model
}

// This is a dependency-boundary regression, not an activation success test.
// The deployment verifier must not acquire a fabricated principal merely to
// make protected representative plans compile.
func TestProtectedSemanticActivationVerificationNeedsSeparateCompilerDesign(t *testing.T) {
	model := semanticConsumerProtectedModel()
	table := model.Tables["orders"]
	table.Schema = semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}}
	model.Tables["orders"] = table
	model.Dimensions = map[string]semanticmodel.SemanticDimension{
		"order_id": {Datatype: semanticmodel.DataTypeInteger, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.id"}}},
	}
	if err := model.ValidateDiscoveredSchemas(); err != nil {
		t.Fatal(err)
	}
	resolution := semanticConsumerResolution(t, "principal-1")
	runtime, err := NewRuntimeView(t.Context(), RuntimeConfig{
		ModelID: "sales", Model: model, Database: cacheRuntimeDatabase{}, Sources: ownershipSources{},
		SemanticAccessCompileContext: &semanticquery.SemanticAccessCompileContext{Registry: resolution.Registry}, ServingStateID: "serving-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.CloseView()
	err = runtime.VerifySemantic(t.Context())
	if err == nil || !strings.Contains(err.Error(), "semantic access denied") {
		t.Fatalf("expected protected representative-plan denial, got %v", err)
	}
}

func newSemanticConsumerRuntime(t testing.TB, database Database, authority SemanticAccessAuthority) *Runtime {
	t.Helper()
	resolution := semanticConsumerResolution(t, "principal-1")
	runtime, err := NewRuntimeView(context.Background(), RuntimeConfig{
		ModelID:                      "sales",
		Model:                        semanticConsumerProtectedModel(),
		Database:                     database,
		Sources:                      ownershipSources{},
		SemanticAccessAuthority:      authority,
		SemanticAccessCompileContext: &semanticquery.SemanticAccessCompileContext{Registry: resolution.Registry},
		ServingStateID:               "serving-test",
	})
	if err != nil {
		t.Fatalf("NewRuntimeView: %v", err)
	}
	t.Cleanup(func() { _ = runtime.CloseView() })
	return runtime
}

func semanticConsumerRequest() dataquery.Query {
	return dataquery.Query{
		ProjectID: projectgraph.ResourceID("project:test"),
		Surface:   dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		PrincipalID: "principal-1", RequestID: "request-1", ModelID: "sales",
		Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
	}
}

func semanticConsumerCountRequest(field string) dataquery.Query {
	request := semanticConsumerRequest()
	request.Operation = dataquery.OperationDashboardCount
	request.Fields = nil
	request.Metrics = nil
	request.IncludeTotal = true
	request.AuthorizationFields = []dataquery.Field{{Field: field}}
	return request
}

func TestProtectedSemanticConsumerRejectsMissingOrDeniedAuthorityBeforeExecution(t *testing.T) {
	tests := []struct {
		name      string
		authority SemanticAccessAuthority
	}{
		{name: "missing"},
		{name: "denied", authority: &semanticConsumerAuthority{resolveErr: errors.New("denied")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := &countingCacheRuntimeDatabase{}
			runtime := newSemanticConsumerRuntime(t, database, test.authority)
			if _, err := runtime.ExecuteDataQuery(context.Background(), semanticConsumerRequest()); err == nil {
				t.Fatal("protected query unexpectedly succeeded without semantic authority")
			}
			if got := database.queries.Load(); got != 0 {
				t.Fatalf("database executions = %d, want 0", got)
			}
		})
	}
}

func TestProtectedSemanticConsumerRejectsStaleControlAfterAdmission(t *testing.T) {
	first := semanticConsumerResolution(t, "principal-1")
	second := first
	second.ControlState.Revision++
	second.ControlState.Digest = "sha256:stale-control"
	authority := &semanticConsumerAuthority{resolutions: []access.SemanticAttributeResolution{first, second}}
	database := &countingCacheRuntimeDatabase{}
	runtime := newSemanticConsumerRuntime(t, database, authority)
	if _, err := runtime.ExecuteDataQuery(context.Background(), semanticConsumerRequest()); err == nil {
		t.Fatal("query with stale semantic control unexpectedly succeeded")
	}
	if got := authority.resolveCalls(); got < 2 {
		t.Fatalf("semantic authority calls = %d, want admission and post-admission validation", got)
	}
}

func TestProtectedSemanticConsumerDropsBufferedResultWhenControlChanges(t *testing.T) {
	initial := semanticConsumerResolution(t, "principal-1")
	changed := initial
	changed.ControlState.Revision++
	authority := &semanticConsumerAuthority{resolutions: []access.SemanticAttributeResolution{initial, initial, changed}}
	database := &countingCacheRuntimeDatabase{}
	runtime := newSemanticConsumerRuntime(t, database, authority)
	result, err := runtime.ExecuteDataQuery(context.Background(), semanticConsumerRequest())
	if err == nil {
		t.Fatal("stale buffered result was returned")
	}
	if database.queries.Load() != 1 {
		t.Fatalf("expected one physical execution before release check, got %d", database.queries.Load())
	}
	if len(result.Rows) != 0 {
		t.Fatal("failed release retained rows")
	}
}

func TestProtectedSemanticBorrowedConsumerDoesNotCloseActivation(t *testing.T) {
	authority := &semanticConsumerAuthority{resolutions: []access.SemanticAttributeResolution{semanticConsumerResolution(t, "principal-1")}}
	database := &countingCacheRuntimeDatabase{}
	runtime := newSemanticConsumerRuntime(t, database, authority)
	borrowed, err := runtime.admitSemanticConsumer(t.Context(), semanticConsumerRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err := borrowed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := borrowed.CloseView(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExecuteDataQuery(t.Context(), semanticConsumerRequest()); err != nil {
		t.Fatalf("closing borrowed consumer disrupted activation: %v", err)
	}
	if err := borrowed.validateSemanticResolution(t.Context()); err == nil {
		t.Fatal("closed borrowed consumer remained usable")
	}
}

func TestProtectedSemanticConsumerRejectsPolicyRemovedAfterActivation(t *testing.T) {
	authority := &semanticConsumerAuthority{resolutions: []access.SemanticAttributeResolution{semanticConsumerResolution(t, "principal-1")}}
	database := &countingCacheRuntimeDatabase{}
	runtime := newSemanticConsumerRuntime(t, database, authority)
	runtime.model.AccessGrants = nil
	dataset := runtime.model.Datasets["orders"]
	dataset.RequiredAccessGrants = nil
	runtime.model.Datasets["orders"] = dataset
	if _, err := runtime.ExecuteDataQuery(t.Context(), semanticConsumerRequest()); err == nil {
		t.Fatal("removed policy bypassed admission")
	}
	if _, _, err := runtime.LookupImmutableBytes("forged"); err == nil {
		t.Fatal("removed policy enabled byte-cache bypass")
	}
	if database.queries.Load() != 0 {
		t.Fatal("stale policy executed")
	}
}

func TestProtectedSemanticConsumerRejectsClosedRuntimeAndPrincipalMismatch(t *testing.T) {
	t.Run("closed", func(t *testing.T) {
		database := &countingCacheRuntimeDatabase{}
		authority := &semanticConsumerAuthority{resolutions: []access.SemanticAttributeResolution{semanticConsumerResolution(t, "principal-1")}}
		runtime := newSemanticConsumerRuntime(t, database, authority)
		if err := runtime.CloseView(); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.ExecuteDataQuery(context.Background(), semanticConsumerRequest()); err == nil {
			t.Fatal("closed protected runtime unexpectedly succeeded")
		}
		if got := database.queries.Load(); got != 0 {
			t.Fatalf("database executions = %d, want 0", got)
		}
	})

	t.Run("principal-mismatch", func(t *testing.T) {
		database := &countingCacheRuntimeDatabase{}
		authority := &semanticConsumerAuthority{resolutions: []access.SemanticAttributeResolution{semanticConsumerResolution(t, "principal-1")}}
		runtime := newSemanticConsumerRuntime(t, database, authority)
		request := semanticConsumerRequest()
		request.PrincipalID = "principal-fake"
		if _, err := runtime.ExecuteDataQuery(context.Background(), request); err == nil {
			t.Fatal("query for a different principal unexpectedly succeeded")
		}
		if got := database.queries.Load(); got != 0 {
			t.Fatalf("database executions = %d, want 0", got)
		}
	})
}

func TestProtectedSemanticConsumerAllowsValidAuthorityWithoutResultCache(t *testing.T) {
	authority := &semanticConsumerAuthority{resolutions: []access.SemanticAttributeResolution{semanticConsumerResolution(t, "principal-1")}}
	database := &countingCacheRuntimeDatabase{}
	runtime := newSemanticConsumerRuntime(t, database, authority)
	request := semanticConsumerRequest()
	for range 2 {
		result, err := runtime.ExecuteDataQuery(context.Background(), request)
		if err != nil {
			t.Fatalf("valid protected query: %v", err)
		}
		if result.CacheOutcome == dataquery.CacheHit {
			t.Fatal("protected query reused a result-cache entry")
		}
	}
	if got := database.queries.Load(); got != 2 {
		t.Fatalf("database executions = %d, want 2", got)
	}
	if got := runtime.queryCache.scope.Stats().Entries; got != 0 {
		t.Fatalf("protected result-cache entries = %d, want 0", got)
	}
}

func TestProtectedSemanticConsumerCountAuthorizesAuthorizationFields(t *testing.T) {
	tests := []struct {
		name      string
		field     string
		wantError bool
	}{
		{name: "allowed member", field: "orders.id"},
		{name: "denied member", field: "private_count", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := &countingCacheRuntimeDatabase{}
			authority := &semanticConsumerAuthority{resolutions: []access.SemanticAttributeResolution{semanticConsumerResolution(t, "principal-1")}}
			runtime := newSemanticConsumerRuntime(t, database, authority)
			result, err := runtime.ExecuteDataQuery(context.Background(), semanticConsumerCountRequest(test.field))
			if test.wantError {
				if err == nil {
					t.Fatal("count with denied authorization member unexpectedly succeeded")
				}
				if got := database.queries.Load(); got != 0 {
					t.Fatalf("denied count database executions = %d, want 0", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("count with allowed authorization member: %v", err)
			}
			if got := database.queries.Load(); got != 1 {
				t.Fatalf("allowed count database executions = %d, want 1", got)
			}
			if result.Status == dataquery.StatusError {
				t.Fatalf("allowed count returned error status: %#v", result)
			}
		})
	}
}

func TestProtectedSemanticConsumerAsyncContextStillRequiresAuthority(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := newSemanticConsumerRuntime(t, database, nil)
	done := make(chan error, 1)
	go func() {
		_, err := runtime.ExecuteDataQuery(context.Background(), semanticConsumerRequest())
		done <- err
	}()
	if err := <-done; err == nil {
		t.Fatal("async protected query unexpectedly succeeded without authority")
	}
	if got := database.queries.Load(); got != 0 {
		t.Fatalf("database executions = %d, want 0", got)
	}
}

type semanticConsumerTestSink struct {
	schemas atomic.Int32
	records atomic.Int32
}

func (s *semanticConsumerTestSink) WriteSchema(*arrow.Schema) error {
	s.schemas.Add(1)
	return nil
}

func (s *semanticConsumerTestSink) WriteRecord(arrow.RecordBatch) error {
	s.records.Add(1)
	return nil
}

var _ arrowquery.Sink = (*semanticConsumerTestSink)(nil)

func TestProtectedSemanticConsumerStreamingRechecksBeforeEachRecord(t *testing.T) {
	initial := semanticConsumerResolution(t, "principal-1")
	changed := initial
	changed.ControlState.Revision++
	authority := &semanticConsumerAuthority{resolutions: []access.SemanticAttributeResolution{initial, initial, initial, changed}}
	database := &countingCacheRuntimeDatabase{}
	runtime := newSemanticConsumerRuntime(t, database, authority)
	sink := &semanticConsumerTestSink{}
	if _, err := runtime.ExecuteDataQueryArrow(context.Background(), semanticConsumerRequest(), sink); err == nil {
		t.Fatal("stale Arrow record was returned")
	}
	if database.queries.Load() != 1 || sink.schemas.Load() != 1 || sink.records.Load() != 0 {
		t.Fatalf("executions=%d schemas=%d records=%d; want 1/1/0", database.queries.Load(), sink.schemas.Load(), sink.records.Load())
	}
}

func TestProtectedSemanticConsumerStreamingRejectsBeforeSink(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	authority := &semanticConsumerAuthority{resolveErr: errors.New("denied")}
	runtime := newSemanticConsumerRuntime(t, database, authority)
	sink := &semanticConsumerTestSink{}
	if _, err := runtime.ExecuteDataQueryArrow(context.Background(), semanticConsumerRequest(), sink); err == nil {
		t.Fatal("denied protected Arrow query unexpectedly succeeded")
	}
	if got := database.queries.Load(); got != 0 {
		t.Fatalf("database executions = %d, want 0", got)
	}
	if got := sink.schemas.Load() + sink.records.Load(); got != 0 {
		t.Fatalf("sink callbacks = %d, want 0", got)
	}
}

func TestProtectedSemanticConsumerBundlesAreIncompatibleBeforeExecution(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := newSemanticConsumerRuntime(t, database, nil)
	request := semanticConsumerRequest()
	_, err := runtime.ExecuteDataQueryBundle(context.Background(), []dataquery.BundleRequest{
		{ID: "one", Query: request}, {ID: "two", Query: request},
	})
	if !dataquery.IsBundleIncompatible(err) {
		t.Fatalf("bundle error = %v, want incompatible", err)
	}
	if got := database.queries.Load(); got != 0 {
		t.Fatalf("database executions = %d, want 0", got)
	}
}

func TestProtectedSemanticConsumerDeniesImmutableByteCacheOperations(t *testing.T) {
	authority := &semanticConsumerAuthority{resolutions: []access.SemanticAttributeResolution{semanticConsumerResolution(t, "principal-1")}}
	runtime := newSemanticConsumerRuntime(t, &countingCacheRuntimeDatabase{}, authority)
	if _, found, err := runtime.LookupImmutableBytes("protected"); err == nil || found {
		t.Fatalf("LookupImmutableBytes = found=%v err=%v, want denial", found, err)
	}
	if runtime.StoreImmutableBytes("protected", []byte("value")) {
		t.Fatal("StoreImmutableBytes accepted protected cache data")
	}
	called := false
	if _, err := runtime.CoalesceImmutableBytes(context.Background(), "protected", func(context.Context) error {
		called = true
		return nil
	}); err == nil || called {
		t.Fatalf("CoalesceImmutableBytes = called=%v err=%v, want denial before callback", called, err)
	}
}
