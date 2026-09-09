package module

// This is deliberately an integration test at the composition seam.  The
// identity and Access readers below are the production PostgreSQL
// implementations; only the analytical executor is a bounded test double so
// this test does not require a second database service or a source connector.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	apptesting "github.com/flidai/leapview/internal/app/testing"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/project/contractprojection"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	identityledger "github.com/flidai/leapview/internal/project/identityledger"
	identitypostgres "github.com/flidai/leapview/internal/project/identityledger/postgres"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/jackc/pgx/v5/pgxpool"
	ocidigest "github.com/opencontainers/go-digest"
)

func TestContractActivationFenceUsesExactActivePublicationEvidence(t *testing.T) {
	db := newQualificationDatabase(t)
	if _, err := db.ledger.Activate(t.Context(), qualificationCandidate(qualificationInstance, "bundle-1", "", true)); err != nil {
		t.Fatal(err)
	}
	qualificationSeedSemanticAuthority(t, db)
	qualificationAuthorization(t, db, qualificationInstance, qualificationProject(t))
	input := qualificationSemanticPublication(qualificationInstance, "1.0.0")
	input.PolicyContext.ExpectedRegistry = qualificationRegistryReference(t, db, qualificationInstance)
	publication, err := db.ledger.PublishContract(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := identityledger.NewPolicyActivationReference(publication, ocidigest.FromString("qualification-graph").String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ledger.Activate(t.Context(), qualificationCandidate(qualificationInstance, "bundle-2", "bundle-1", true)); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := db.ledger.WithContractActivationFence(t.Context(), "bundle-2", []identityledger.PolicyActivationReference{reference}, func(context.Context) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("matching activation fence called=%t err=%v", called, err)
	}
	retained, err := db.ledger.ReadLatestLifecycleEvidence(t.Context(), qualificationInstance, reference.Publication.AuthoredID, reference.Publication.ResourceKind)
	if err != nil {
		t.Fatalf("read retained publication after successor activation: %v", err)
	}
	if retained.Sequence != 2 || retained.Identity.ActiveBundleID != "bundle-2" || retained.Publication.Digest != publication.Digest {
		t.Fatalf("retained publication/current lifecycle evidence = %#v", retained)
	}
	nextReference, err := identityledger.NewPolicyActivationReference(retained.Publication, reference.GraphDigest)
	if err != nil {
		t.Fatal(err)
	}
	nextReference.LifecycleSequence = retained.Sequence
	nextReference.ActiveBundleID = retained.Identity.ActiveBundleID
	if _, err := db.ledger.Activate(t.Context(), qualificationCandidate(qualificationInstance, "bundle-3", "bundle-2", true)); err != nil {
		t.Fatal(err)
	}
	if err := db.ledger.WithContractActivationFence(t.Context(), "bundle-3", []identityledger.PolicyActivationReference{nextReference}, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("retained immutable publication rejected for unchanged successor: %v", err)
	}
	changed := reference
	changed.Publication.Digest = ocidigest.FromString("changed-publication").String()
	if err := db.ledger.WithContractActivationFence(t.Context(), "bundle-3", []identityledger.PolicyActivationReference{changed}, func(context.Context) error { return nil }); !errors.Is(err, identityledger.ErrPolicyEvidenceConflict) {
		t.Fatalf("changed publication error = %v", err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	fenceDone, mutationDone := make(chan error, 1), make(chan error, 1)
	go func() {
		fenceDone <- db.ledger.WithContractActivationFence(t.Context(), "bundle-3", []identityledger.PolicyActivationReference{nextReference}, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	go func() {
		_, mutationErr := db.ledger.Activate(t.Context(), qualificationCandidate(qualificationInstance, "bundle-4", "bundle-3", false))
		mutationDone <- mutationErr
	}()
	select {
	case err := <-mutationDone:
		t.Fatalf("lifecycle mutation crossed activation fence: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-fenceDone; err != nil {
		t.Fatalf("activation fence: %v", err)
	}
	if err := <-mutationDone; err != nil {
		t.Fatalf("serialized lifecycle mutation: %v", err)
	}
	if err := db.ledger.WithContractActivationFence(t.Context(), "bundle-4", []identityledger.PolicyActivationReference{nextReference}, func(context.Context) error { return nil }); !errors.Is(err, identityledger.ErrPolicyEvidenceConflict) {
		t.Fatalf("non-active identity error = %v", err)
	}
}

func TestContractActivationFenceSerializesConcurrentPublication(t *testing.T) {
	db := newQualificationDatabase(t)
	if _, err := db.ledger.Activate(t.Context(), qualificationCandidate(qualificationInstance, "bundle-1", "", true)); err != nil {
		t.Fatal(err)
	}
	qualificationSeedSemanticAuthority(t, db)
	qualificationAuthorization(t, db, qualificationInstance, qualificationProject(t))
	input := qualificationSemanticPublication(qualificationInstance, "1.0.0")
	input.PolicyContext.ExpectedRegistry = qualificationRegistryReference(t, db, qualificationInstance)
	publication, err := db.ledger.PublishContract(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := identityledger.NewPolicyActivationReference(publication, ocidigest.FromString("qualification-graph").String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ledger.Activate(t.Context(), qualificationCandidate(qualificationInstance, "bundle-2", "bundle-1", true)); err != nil {
		t.Fatal(err)
	}
	nextRegistry := qualificationRegistryReference(t, db, qualificationInstance)

	entered, release := make(chan struct{}), make(chan struct{})
	fenceDone, publicationDone := make(chan error, 1), make(chan error, 1)
	go func() {
		fenceDone <- db.ledger.WithContractActivationFence(t.Context(), "bundle-2", []identityledger.PolicyActivationReference{reference}, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	publicationStarted := make(chan struct{})
	go func() {
		close(publicationStarted)
		next := qualificationSemanticPublication(qualificationInstance, "1.1.0")
		next.PolicyContext = &identityledger.PolicyContext{
			BaselineKind: identityledger.PolicyBaselineExisting, Baseline: &reference.Publication,
			ExpectedLifecycleSequence: 2, ExpectedRegistry: nextRegistry,
		}
		_, publicationErr := db.ledger.PublishContract(t.Context(), next)
		publicationDone <- publicationErr
	}()
	<-publicationStarted
	select {
	case err := <-publicationDone:
		t.Fatalf("publication crossed activation fence: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-fenceDone; err != nil {
		t.Fatalf("activation fence: %v", err)
	}
	if err := <-publicationDone; err != nil {
		t.Fatalf("serialized publication: %v", err)
	}
	if err := db.ledger.WithContractActivationFence(t.Context(), "bundle-2", []identityledger.PolicyActivationReference{reference}, func(context.Context) error { return nil }); !errors.Is(err, identityledger.ErrPolicyEvidenceConflict) {
		t.Fatalf("superseded publication reference error=%v, want ErrPolicyEvidenceConflict", err)
	}
}

func TestContractActivationFenceRejectsCurrentRegistryDrift(t *testing.T) {
	db := newQualificationDatabase(t)
	if _, err := db.ledger.Activate(t.Context(), qualificationCandidate(qualificationInstance, "bundle-1", "", true)); err != nil {
		t.Fatal(err)
	}
	definition := qualificationSeedSemanticAuthority(t, db)
	qualificationAuthorization(t, db, qualificationInstance, qualificationProject(t))
	input := qualificationSemanticPublication(qualificationInstance, "1.0.0")
	input.PolicyContext.ExpectedRegistry = qualificationRegistryReference(t, db, qualificationInstance)
	publication, err := db.ledger.PublishContract(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := identityledger.NewPolicyActivationReference(publication, ocidigest.FromString("qualification-graph").String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ledger.Activate(t.Context(), qualificationCandidate(qualificationInstance, "bundle-2", "bundle-1", true)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.access.UpdateSemanticAttributeMetadata(t.Context(), access.UpdateSemanticAttributeMetadataInput{
		Name: "region", ExpectedVersion: definition.DefinitionVersion,
		Metadata: access.SemanticAttributeMetadata{DisplayName: "Region changed"},
		Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: qualificationActor},
	}); err != nil {
		t.Fatal(err)
	}
	err = db.ledger.WithContractActivationFence(t.Context(), "bundle-2", []identityledger.PolicyActivationReference{reference}, func(context.Context) error { return nil })
	if !errors.Is(err, identityledger.ErrPolicyEvidenceConflict) || !strings.Contains(err.Error(), "registry") {
		t.Fatalf("registry drift error=%v, want policy conflict identifying registry", err)
	}
}

const (
	qualificationInstance  = "instance-qualification"
	qualificationOther     = "instance-qualification-other"
	qualificationProjectID = projectgraph.ResourceID("project:qualification")
	qualificationModel     = projectgraph.ResourceID("semantic:orders")
	qualificationActor     = "00000000-0000-7000-8000-000000000001"
	qualificationSubject   = "00000000-0000-7000-8000-000000000002"
)

type qualificationDatabase struct {
	admin   *pgxpool.Pool
	runtime *pgxpool.Pool
	ledger  *identitypostgres.Repository
	access  *accesspostgres.Repository
}

func newQualificationDatabase(t *testing.T) qualificationDatabase {
	t.Helper()
	h := postgrestest.Start(t, apptesting.PostgresConformanceRequired())
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{
		Name: "leapview_control_runtime", Password: "leapview-conformance-secret", Login: true,
	})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "")
	h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SET ROLE leapview_control_migrator`); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		t.Fatalf("apply PostgreSQL migrations: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()

	for _, id := range []string{qualificationActor, qualificationSubject} {
		if _, err := admin.Exec(ctx, `
			INSERT INTO access.principal(id,principal_type,status,display_name)
			VALUES($1::uuid,'user','active','qualification test') ON CONFLICT (id) DO NOTHING`, id); err != nil {
			t.Fatalf("seed principal %s: %v", id, err)
		}
	}
	runtime, err := pgxpool.New(ctx, database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	accessRepo, err := accesspostgres.NewAccess(runtime, accesspostgres.FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := identitypostgres.New(runtime, identitypostgres.Config{SemanticRegistryReader: accesspostgres.ReadSemanticRegistryTx})
	if err != nil {
		t.Fatal(err)
	}
	return qualificationDatabase{admin: admin, runtime: runtime, ledger: ledger, access: accessRepo}
}

func qualificationProject(t *testing.T) projectgraph.ProjectGraph {
	t.Helper()
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{
		ID: qualificationProjectID, Kind: projectgraph.KindProject, Name: "Qualification",
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func qualificationCandidate(instance, bundle, expected string, includeModel bool) identityledger.Candidate {
	resources := []identityledger.Resource{}
	if includeModel {
		resources = append(resources, identityledger.Resource{AuthoredID: qualificationModel, Kind: projectgraph.KindSemanticModel})
	}
	return identityledger.Candidate{InstanceID: instance, BundleID: bundle, ExpectedBundleID: expected, ActorID: "qualification-test", Resources: resources}
}

func qualificationSemanticPublication(instance, version string) identityledger.ContractPublicationInput {
	document := map[string]any{
		"apiVersion": "leapview.dev/v1", "kind": "SemanticModel",
		"metadata": map[string]any{"id": qualificationModel.String(), "name": "orders"},
		"spec": map[string]any{
			"datasets":      map[string]any{"orders": map[string]any{"model": "orders", "requiredAccessGrants": []any{"regiongrant"}}},
			"accessGrants":  map[string]any{"regiongrant": map[string]any{"userAttribute": "region", "allowedValues": []string{"us"}}},
			"relationships": map[string]any{}, "dimensions": map[string]any{}, "filters": map[string]any{}, "metrics": map[string]any{},
		},
	}
	encoded, _ := json.Marshal(document)
	var authored projectcontracts.SemanticModel
	if err := json.Unmarshal(encoded, &authored); err != nil {
		panic(err)
	}
	projection, err := contractprojection.ProjectSemanticModel(authored, contractprojection.Contract{Version: version, Compatibility: "backward"})
	if err != nil {
		panic(err)
	}
	return identityledger.ContractPublicationInput{
		InstanceID: instance, Projection: projection,
		PolicyContext: &identityledger.PolicyContext{BaselineKind: identityledger.PolicyBaselineGenesis, ExpectedLifecycleSequence: 1},
		Validation:    identityledger.ValidationEvidence{Version: 1, Checks: []identityledger.ValidationCheck{{Name: "qualification-test", Outcome: identityledger.ValidationPassed, Reference: "integration"}}},
	}
}

func qualificationAuthorization(t *testing.T, db qualificationDatabase, instance string, project projectgraph.ProjectGraph) access.AuthorizationControlRevision {
	t.Helper()
	state, err := db.access.InitializeControlState(t.Context(), access.ControlStateSeed{
		InstanceID: instance, ProjectID: project.ProjectID().String(), ActorID: qualificationActor,
	}, project)
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision <= 0 {
		t.Fatalf("initialized control revision = %d, want positive", state.Revision)
	}
	revision, err := db.access.ReadAuthorizationControlRevision(t.Context(), instance)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func qualificationRegistryReference(t *testing.T, db qualificationDatabase, instance string) *identityledger.PolicyRegistryReference {
	t.Helper()
	context, err := db.access.ReadSemanticRegistry(t.Context(), instance)
	if err != nil {
		t.Fatal(err)
	}
	return &identityledger.PolicyRegistryReference{
		InstanceID: context.Control.InstanceID, ProjectID: projectgraph.ResourceID(context.Control.ProjectID),
		ControlRevision: context.Control.Revision, Profile: context.Registry.State.Profile,
		Revision: context.Registry.State.Revision, Digest: context.Registry.State.Digest,
	}
}

func qualificationSeedSemanticAuthority(t *testing.T, db qualificationDatabase) access.SemanticAttributeDefinition {
	t.Helper()
	definition, err := db.access.RegisterSemanticAttribute(t.Context(), access.RegisterSemanticAttributeInput{
		Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeList,
		Metadata: access.SemanticAttributeMetadata{DisplayName: "Region"},
		Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: qualificationActor},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.access.SetSemanticAttributeAssignment(t.Context(), access.SemanticAttributeAssignmentInput{
		DefinitionID: definition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: qualificationSubject}, Values: []string{"us"},
		Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: qualificationActor},
	}); err != nil {
		t.Fatal(err)
	}
	return definition
}

type qualificationSemanticAuthority struct {
	repo    *accesspostgres.Repository
	subject access.SubjectRef
}

func (a qualificationSemanticAuthority) SemanticAttributeRegistry(ctx context.Context) (access.SemanticAttributeRegistrySnapshot, error) {
	return a.repo.SemanticAttributeRegistry(ctx)
}

func (a qualificationSemanticAuthority) ResolveSemanticAttributes(ctx context.Context) (access.SemanticAttributeResolution, error) {
	return a.repo.ResolveSemanticAttributes(ctx, a.subject)
}

func qualificationBinding(t *testing.T, db qualificationDatabase, instance string, auth access.AuthorizationControlRevision) (resultidentity.SemanticLifecycle, func(context.Context) (resultidentity.SemanticLifecycle, error)) {
	t.Helper()
	evidence, err := db.ledger.ReadLifecycleEvidence(t.Context(), instance, qualificationModel, projectgraph.KindSemanticModel, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	binding := resultidentity.SemanticLifecycle{
		InstanceID: instance, ProjectID: projectgraph.ResourceID(auth.ProjectID), AuthoredID: qualificationModel, ResourceKind: projectgraph.KindSemanticModel,
		Sequence: evidence.Sequence, ActiveBundleID: evidence.Identity.ActiveBundleID, PublicationVersion: evidence.Publication.Version,
		ProjectionProfile: evidence.Publication.ProjectionProfile, PublicationDigest: evidence.Publication.Digest, AuthorizationRevision: auth.Revision,
	}
	reader, err := SemanticCacheLifecycleReader(db.ledger, db.access, binding, auth)
	if err != nil {
		t.Fatal(err)
	}
	return binding, reader
}

func qualificationModelDefinition() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: qualificationModel.String(),
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName: "orders", Execution: semanticmodel.ExecutionDefinition{SQL: "SELECT 1 AS id"},
			Dimensions: map[string]semanticmodel.MetricDimension{"id": {Name: "id", Type: "integer", Datatype: semanticmodel.DataTypeInteger}},
			Entities:   map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}}, GrainEntity: "order",
		}},
		Datasets:     map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders", RequiredAccessGrants: []string{"regiongrant"}}},
		AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{"regiongrant": {UserAttribute: "region", AllowedValues: []any{"us"}}},
	}
}

type qualificationSources struct{}

func (qualificationSources) Prepare(context.Context, *semanticmodel.Model) (materialize.PreparedSources, error) {
	return qualificationPreparedSources{}, nil
}

type qualificationPreparedSources struct{}

func (qualificationPreparedSources) PlanModelTable(context.Context, *semanticmodel.Model, string, semanticmodel.Table) (materialize.ModelTablePlan, error) {
	return materialize.ModelTablePlan{Mode: materialize.PlanModeModelSQL, SQL: "SELECT 1 AS id"}, nil
}
func (qualificationPreparedSources) Close() error { return nil }

type qualificationDatabaseExecutor struct{ queries atomic.Int32 }

func (d *qualificationDatabaseExecutor) Exec(context.Context, string) error { return nil }
func (d *qualificationDatabaseExecutor) Close() error                       { return nil }
func (d *qualificationDatabaseExecutor) Path() string                       { return "qualification-test" }
func (d *qualificationDatabaseExecutor) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	d.queries.Add(1)
	fields := make([]arrow.Field, len(plan.Columns))
	arrays := make([]arrow.Array, len(plan.Columns))
	for i, name := range plan.Columns {
		fields[i] = arrow.Field{Name: name, Type: arrow.PrimitiveTypes.Int64}
		builder := array.NewInt64Builder(memory.DefaultAllocator)
		builder.Append(1)
		arrays[i] = builder.NewArray()
		builder.Release()
	}
	schema := arrow.NewSchema(fields, nil)
	if err := sink.WriteSchema(schema); err != nil {
		for _, value := range arrays {
			value.Release()
		}
		return err
	}
	record := array.NewRecordBatch(schema, arrays, 1)
	for _, value := range arrays {
		value.Release()
	}
	defer record.Release()
	if err := arrowquery.ConsumeResultBudget(ctx, record); err != nil {
		return err
	}
	return sink.WriteRecord(record)
}

func qualificationRuntime(t *testing.T, db qualificationDatabase, cacheScope *resultcache.Scope, binding resultidentity.SemanticLifecycle, reader func(context.Context) (resultidentity.SemanticLifecycle, error), registry access.SemanticAttributeRegistrySnapshot, executor *qualificationDatabaseExecutor, authRevision int64) *materialize.Runtime {
	t.Helper()
	model := qualificationModelDefinition()
	modelDigest, err := semanticquery.SemanticModelDigest(model)
	if err != nil {
		t.Fatal(err)
	}
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionProduction, ProjectID: qualificationProjectID, Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := resultidentity.NewEvidence(resultidentity.EvidenceInput{
		SemanticModelID: qualificationModel, SemanticModelDigest: modelDigest,
		DatasetRelations:   []resultidentity.DatasetRelation{{Dataset: "orders", Relation: resultidentity.RelationRevision{RelationID: "model:orders", RevisionDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111"}}},
		BindingFingerprint: "sha256:2222222222222222222222222222222222222222222222222222222222222222", RuntimeDigest: "sha256:3333333333333333333333333333333333333333333333333333333333333333", CapabilityDigest: "sha256:4444444444444444444444444444444444444444444444444444444444444444",
	})
	if err != nil {
		t.Fatal(err)
	}
	executorRef := executor
	runtime, err := materialize.NewRuntimeView(t.Context(), materialize.RuntimeConfig{
		ModelID: qualificationModel.String(), Model: model, Database: executorRef, Sources: qualificationSources{}, SnapshotOnly: true,
		SemanticAccessAuthority:      qualificationSemanticAuthority{repo: db.access, subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: qualificationSubject}},
		SemanticAccessCompileContext: &semanticquery.SemanticAccessCompileContext{Registry: registry}, ServingStateID: "serving:qualification",
		SemanticAudit: &materialize.SemanticAuditConfig{
			InstanceID: binding.InstanceID,
			Identity:   projectgraph.ServingIdentity{ProjectID: qualificationProjectID, Environment: "production", GenerationID: "serving:qualification"},
			Recorder:   db.access, ActorFromContext: func(context.Context) (string, error) { return qualificationSubject, nil },
		},
		ResultPartition: partition, DependencyEvidence: evidence, QueryResultCache: cacheScope, ImmutableByteCache: cacheScope,
		SemanticCache: &materialize.SemanticCacheConfig{Binding: binding, ReadCurrent: reader},
	})
	if err != nil {
		t.Fatalf("NewRuntimeView (authorization revision %d): %v", authRevision, err)
	}
	t.Cleanup(func() { _ = runtime.CloseView() })
	return runtime
}

func qualificationRequest() dataquery.Query {
	return dataquery.Query{
		ProjectID: qualificationProjectID, Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		PrincipalID: qualificationSubject, RequestID: "00000000-0000-7000-8000-000000000003", ModelID: qualificationModel.String(), Kind: dataquery.KindSemanticRows,
		Target: "orders", Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
		EffectivePolicyFingerprint: "sha256:5555555555555555555555555555555555555555555555555555555555555555",
	}
}

func qualificationQuery(t *testing.T, runtime *materialize.Runtime, want string) dataquery.Result {
	t.Helper()
	result, err := runtime.ExecuteDataQuery(t.Context(), qualificationRequest())
	if err != nil {
		t.Fatalf("protected query: %v", err)
	}
	if result.CacheOutcome != want {
		t.Fatalf("cache outcome = %q, want %q", result.CacheOutcome, want)
	}
	return result
}

type qualificationAuditRecord struct {
	ID       string
	Event    access.CanonicalAuditEvent
	Evidence access.SemanticDecisionEvidence
}

// Read through the existing Access repository and verify every retained digest,
// not just row existence. Repeated requests append fresh events; this is not
// an idempotent request-retry assertion.
func qualificationAuditRecords(t *testing.T, db qualificationDatabase, instanceID string) []qualificationAuditRecord {
	t.Helper()
	rows, err := db.runtime.Query(t.Context(), `SELECT audit_id::text FROM audit.audit_event WHERE action=$1 AND metadata->>'instanceId'=$2 ORDER BY occurred_at, audit_id`, access.SemanticDecisionAuditAction, instanceID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	records := make([]qualificationAuditRecord, 0, len(ids))
	for _, id := range ids {
		event, err := db.access.ReadSemanticDecisionAuditEvent(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		evidence, err := access.DecodeSemanticDecisionEvidence(event.MetadataJSON)
		if err != nil {
			t.Fatal(err)
		}
		if evidence.InstanceID != instanceID || event.Identity.ProjectID != qualificationProjectID || event.Identity.GenerationID != "serving:qualification" || event.PrincipalID != qualificationSubject || event.Resource.ID() != qualificationModel || event.RequestID != qualificationRequest().RequestID {
			t.Fatalf("retained semantic decision identity differs: %#v / %#v", event, evidence)
		}
		records = append(records, qualificationAuditRecord{ID: id, Event: event, Evidence: evidence})
	}
	return records
}

func qualificationAuditCount(t *testing.T, db qualificationDatabase, instanceID string) int {
	t.Helper()
	return len(qualificationAuditRecords(t, db, instanceID))
}

func qualificationAuditDelta(before, after []qualificationAuditRecord) []qualificationAuditRecord {
	seen := make(map[string]struct{}, len(before))
	for _, record := range before {
		seen[record.ID] = struct{}{}
	}
	delta := make([]qualificationAuditRecord, 0, len(after)-len(before))
	for _, record := range after {
		if _, ok := seen[record.ID]; !ok {
			delta = append(delta, record)
		}
	}
	return delta
}

func qualificationAssertCurrentAuditEvidence(t *testing.T, records []qualificationAuditRecord, resolution access.SemanticAttributeResolution) {
	t.Helper()
	modelDigest, err := semanticquery.SemanticModelDigest(qualificationModelDefinition())
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Event.Status != "success" || !record.Evidence.Allowed || record.Evidence.Reason != "" {
			t.Fatalf("fresh protected decision was not successful: %#v", record.Event)
		}
		if record.Evidence.ActorPrincipalID != qualificationSubject || record.Evidence.SemanticModelDigest != modelDigest {
			t.Fatalf("audit actor/model binding differs from admitted request: %#v", record.Evidence)
		}
		if record.Evidence.Registry.Profile != resolution.Registry.State.Profile || record.Evidence.Registry.Revision != resolution.Registry.State.Revision || record.Evidence.Registry.Digest != resolution.Registry.State.Digest {
			t.Fatalf("audit registry evidence differs from admitted authority: %#v / %#v", record.Evidence, resolution.Registry.State)
		}
		if record.Evidence.Control.Profile != resolution.ControlState.Profile || record.Evidence.Control.Revision != resolution.ControlState.Revision || record.Evidence.Control.Digest != resolution.ControlState.Digest {
			t.Fatalf("audit control evidence differs from admitted authority: %#v / %#v", record.Evidence, resolution.ControlState)
		}
		if record.Evidence.Target.Dataset != "orders" || record.Evidence.Target.Dimension != "" || record.Evidence.Target.Metric != "" {
			t.Fatalf("audit target differs from admitted dataset: %#v", record.Evidence.Target)
		}
		foundGrant := false
		for _, grant := range record.Evidence.Grants {
			if grant.Grant == "regiongrant" && grant.UserAttribute == "region" && grant.Satisfied {
				foundGrant = true
			}
			if grant.Grant == "" || grant.UserAttribute == "" || grant.AttributeDefinitionID == "" || grant.AttributeDefinitionVersion <= 0 {
				t.Fatalf("audit grant identity is incomplete: %#v", grant)
			}
		}
		if !foundGrant {
			t.Fatalf("audit omitted the required grant outcome: %#v", record.Evidence.Grants)
		}
		for _, filter := range record.Evidence.Filters {
			if filter.Identity == "" || filter.Dataset == "" || filter.Dimension == "" || filter.UserAttribute == "" || filter.AttributeDefinitionID == "" || filter.AttributeDefinitionVersion <= 0 {
				t.Fatalf("audit filter identity is incomplete: %#v", filter)
			}
		}
		for _, attribute := range resolution.Attributes {
			foundAttribute := false
			for _, retained := range record.Evidence.Attributes {
				if retained.DefinitionID == attribute.DefinitionID && retained.DefinitionName == attribute.DefinitionName && retained.DefinitionVersion == attribute.DefinitionVersion && retained.Source == attribute.Source && retained.ValueDigest == attribute.ValueDigest {
					foundAttribute = true
					break
				}
			}
			if !foundAttribute {
				t.Fatalf("audit omitted effective attribute identity %q: %#v", attribute.DefinitionID, record.Evidence.Attributes)
			}
		}
	}
}

func qualificationFreshRuntimeAudit(t *testing.T, db qualificationDatabase, instanceID string, runtime *materialize.Runtime, resolution access.SemanticAttributeResolution, database *qualificationDatabaseExecutor) []qualificationAuditRecord {
	t.Helper()
	before := qualificationAuditRecords(t, db, instanceID)
	qualificationQuery(t, runtime, dataquery.CacheMiss)
	afterMiss := qualificationAuditRecords(t, db, instanceID)
	miss := qualificationAuditDelta(before, afterMiss)
	if len(miss) == 0 {
		t.Fatal("fresh protected cache miss wrote no semantic decision audit")
	}
	qualificationAssertCurrentAuditEvidence(t, miss, resolution)
	qualificationQuery(t, runtime, dataquery.CacheHit)
	afterHit := qualificationAuditRecords(t, db, instanceID)
	hit := qualificationAuditDelta(afterMiss, afterHit)
	if len(hit) == 0 {
		t.Fatal("fresh protected cache hit wrote no semantic decision audit")
	}
	qualificationAssertCurrentAuditEvidence(t, hit, resolution)
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("fresh protected miss/hit physical executions = %d, want 1", got)
	}
	return append(miss, hit...)
}

func TestSemanticQualificationPostgreSQL18LifecycleAndAuthority(t *testing.T) {
	db := newQualificationDatabase(t)
	project := qualificationProject(t)
	if _, err := db.ledger.Activate(t.Context(), qualificationCandidate(qualificationInstance, "bundle-1", "", true)); err != nil {
		t.Fatal(err)
	}
	qualificationSeedSemanticAuthority(t, db)
	auth := qualificationAuthorization(t, db, qualificationInstance, project)
	publication := qualificationSemanticPublication(qualificationInstance, "1.0.0")
	publication.PolicyContext.ExpectedRegistry = qualificationRegistryReference(t, db, qualificationInstance)
	if _, err := db.ledger.PublishContract(t.Context(), publication); err != nil {
		t.Fatalf("publish semantic contract: %v", err)
	}
	binding, reader := qualificationBinding(t, db, qualificationInstance, auth)
	registry, err := db.access.SemanticAttributeRegistry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cachePool, err := resultcache.New(resultcache.Limits{RuntimeEntries: 32, RuntimeBytes: 1 << 20, NodeEntries: 32, NodeBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	cacheScope, err := cachePool.OpenSharedScope(resultcache.ScopeID{RuntimeID: "qualification-shared"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cacheScope.Close(); _ = cachePool.Close() })

	firstDB := &qualificationDatabaseExecutor{}
	firstRuntime := qualificationRuntime(t, db, cacheScope, binding, reader, registry, firstDB, auth.Revision)
	firstResolution, err := db.access.ResolveSemanticAttributes(t.Context(), access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: qualificationSubject})
	if err != nil {
		t.Fatal(err)
	}
	historical := qualificationFreshRuntimeAudit(t, db, qualificationInstance, firstRuntime, firstResolution, firstDB)

	// A second instance starts with the same lifecycle/publication/control
	// values as the first, except for InstanceID. It must not consume the
	// first instance's retained entry, and the first entry must remain usable.
	if _, err := db.ledger.Activate(t.Context(), qualificationCandidate(qualificationOther, "bundle-1", "", true)); err != nil {
		t.Fatal(err)
	}
	otherAuth := qualificationAuthorization(t, db, qualificationOther, project)
	otherPublication := qualificationSemanticPublication(qualificationOther, "1.0.0")
	otherPublication.PolicyContext.ExpectedRegistry = qualificationRegistryReference(t, db, qualificationOther)
	if _, err := db.ledger.PublishContract(t.Context(), otherPublication); err != nil {
		t.Fatal(err)
	}
	otherEvidence, err := db.ledger.ReadLifecycleEvidence(t.Context(), qualificationOther, qualificationModel, projectgraph.KindSemanticModel, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	otherBinding := resultidentity.SemanticLifecycle{InstanceID: qualificationOther, ProjectID: qualificationProjectID, AuthoredID: qualificationModel, ResourceKind: projectgraph.KindSemanticModel, Sequence: otherEvidence.Sequence, ActiveBundleID: otherEvidence.Identity.ActiveBundleID, PublicationVersion: otherEvidence.Publication.Version, ProjectionProfile: otherEvidence.Publication.ProjectionProfile, PublicationDigest: otherEvidence.Publication.Digest, AuthorizationRevision: otherAuth.Revision}
	equivalentBinding := otherBinding
	equivalentBinding.InstanceID = binding.InstanceID
	if equivalentBinding != binding {
		t.Fatalf("instance isolation fixture differs beyond instance: first=%+v second=%+v", binding, otherBinding)
	}
	if _, err := SemanticCacheLifecycleReader(db.ledger, db.access, otherBinding, auth); err == nil {
		t.Fatal("cross-instance authorization was accepted for lifecycle binding")
	}
	otherReader, err := SemanticCacheLifecycleReader(db.ledger, db.access, otherBinding, otherAuth)
	if err != nil {
		t.Fatal(err)
	}
	otherRegistry, err := db.access.SemanticAttributeRegistry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	otherDB := &qualificationDatabaseExecutor{}
	otherRuntime := qualificationRuntime(t, db, cacheScope, otherBinding, otherReader, otherRegistry, otherDB, otherAuth.Revision)
	otherResolution, err := db.access.ResolveSemanticAttributes(t.Context(), access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: qualificationSubject})
	if err != nil {
		t.Fatal(err)
	}
	qualificationFreshRuntimeAudit(t, db, qualificationOther, otherRuntime, otherResolution, otherDB)
	beforeFirstReplay := qualificationAuditRecords(t, db, qualificationInstance)
	qualificationQuery(t, firstRuntime, dataquery.CacheHit)
	afterFirstReplay := qualificationAuditRecords(t, db, qualificationInstance)
	historical = append(historical, qualificationAuditDelta(beforeFirstReplay, afterFirstReplay)...)
	if got := firstDB.queries.Load(); got != 1 {
		t.Fatalf("first-instance re-hit physical executions = %d, want 1", got)
	}
	if got := len(afterFirstReplay); got < 3 {
		t.Fatalf("first-instance replay audit count = %d, want at least 3", got)
	}

	// Removing the authored resource leaves its immutable publication readable,
	// but the active runtime must not revive the old cache entry.
	if _, err := db.ledger.Activate(t.Context(), qualificationCandidate(qualificationInstance, "bundle-2", "bundle-1", false)); err != nil {
		t.Fatal(err)
	}
	beforeTombstone := qualificationAuditRecords(t, db, qualificationInstance)
	if _, err := firstRuntime.ExecuteDataQuery(t.Context(), qualificationRequest()); err == nil {
		t.Fatal("tombstoned resource reused protected cache")
	}
	afterTombstone := qualificationAuditRecords(t, db, qualificationInstance)
	historical = append(historical, qualificationAuditDelta(beforeTombstone, afterTombstone)...)
	if got := firstDB.queries.Load(); got != 1 {
		t.Fatalf("tombstone physical executions = %d, want 1", got)
	}

	if _, err := db.ledger.RestoreAndActivate(t.Context(), identityledger.Restore{
		Candidate: qualificationCandidate(qualificationInstance, "bundle-3", "bundle-2", true), AuthoredIDs: []projectgraph.ResourceID{qualificationModel}, Reason: "qualification restore",
	}); err != nil {
		t.Fatal(err)
	}
	restoredAuth, err := db.access.ReadAuthorizationControlRevision(t.Context(), qualificationInstance)
	if err != nil {
		t.Fatal(err)
	}
	restoredBinding, restoredReader := qualificationBinding(t, db, qualificationInstance, restoredAuth)
	restoredDB := &qualificationDatabaseExecutor{}
	restoredRegistry, err := db.access.SemanticAttributeRegistry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	restoredRuntime := qualificationRuntime(t, db, cacheScope, restoredBinding, restoredReader, restoredRegistry, restoredDB, restoredAuth.Revision)
	restoredResolution, err := db.access.ResolveSemanticAttributes(t.Context(), access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: qualificationSubject})
	if err != nil {
		t.Fatal(err)
	}
	historical = append(historical, qualificationFreshRuntimeAudit(t, db, qualificationInstance, restoredRuntime, restoredResolution, restoredDB)...)
	beforePreRestore := qualificationAuditRecords(t, db, qualificationInstance)
	if _, err := firstRuntime.ExecuteDataQuery(t.Context(), qualificationRequest()); err == nil {
		t.Fatal("old pre-restore runtime accepted restored lifecycle")
	}
	afterPreRestore := qualificationAuditRecords(t, db, qualificationInstance)
	historical = append(historical, qualificationAuditDelta(beforePreRestore, afterPreRestore)...)
	if got := firstDB.queries.Load(); got != 1 {
		t.Fatalf("pre-restore stale physical executions = %d, want 1", got)
	}

	if _, err := db.ledger.Rollback(t.Context(), identityledger.Rollback{InstanceID: qualificationInstance, BundleID: "bundle-1", ExpectedBundleID: "bundle-3", ActorID: "qualification-test", Reason: "qualification rollback"}); err != nil {
		t.Fatal(err)
	}
	beforePreRollback := qualificationAuditRecords(t, db, qualificationInstance)
	if _, err := restoredRuntime.ExecuteDataQuery(t.Context(), qualificationRequest()); err == nil {
		t.Fatal("pre-rollback runtime accepted rollback lifecycle")
	}
	afterPreRollback := qualificationAuditRecords(t, db, qualificationInstance)
	historical = append(historical, qualificationAuditDelta(beforePreRollback, afterPreRollback)...)
	if got := restoredDB.queries.Load(); got != 1 {
		t.Fatalf("pre-rollback stale physical executions = %d, want 1", got)
	}
	rollbackAuth, err := db.access.ReadAuthorizationControlRevision(t.Context(), qualificationInstance)
	if err != nil {
		t.Fatal(err)
	}
	rollbackBinding, rollbackReader := qualificationBinding(t, db, qualificationInstance, rollbackAuth)
	rollbackRegistry, err := db.access.SemanticAttributeRegistry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rollbackDB := &qualificationDatabaseExecutor{}
	rollbackRuntime := qualificationRuntime(t, db, cacheScope, rollbackBinding, rollbackReader, rollbackRegistry, rollbackDB, rollbackAuth.Revision)
	rollbackResolution, err := db.access.ResolveSemanticAttributes(t.Context(), access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: qualificationSubject})
	if err != nil {
		t.Fatal(err)
	}
	historical = append(historical, qualificationFreshRuntimeAudit(t, db, qualificationInstance, rollbackRuntime, rollbackResolution, rollbackDB)...)

	// Real role/control mutation fences the runtime's retained lifecycle. Then
	// a fresh runtime is admitted at the new control revision so the semantic
	// attribute registry/assignment checks can be exercised independently.
	assignmentID := "00000000-0000-7000-8000-000000000301"
	if _, err := db.access.CreateRoleAssignment(t.Context(), access.RoleAssignmentInput{ID: assignmentID, InstanceID: qualificationInstance, ProjectID: qualificationProjectID.String(), Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: qualificationSubject}, Role: string(access.ProjectRoleViewer), Name: "qualification viewer", ActorID: qualificationActor}); err != nil {
		t.Fatal(err)
	}
	beforeRoleControl := qualificationAuditRecords(t, db, qualificationInstance)
	if _, err := rollbackRuntime.ExecuteDataQuery(t.Context(), qualificationRequest()); err == nil {
		t.Fatal("role/control mutation reused protected cache")
	}
	afterRoleControl := qualificationAuditRecords(t, db, qualificationInstance)
	historical = append(historical, qualificationAuditDelta(beforeRoleControl, afterRoleControl)...)
	if got := rollbackDB.queries.Load(); got != 1 {
		t.Fatalf("role/control stale physical executions = %d, want 1", got)
	}
	controlAuth, err := db.access.ReadAuthorizationControlRevision(t.Context(), qualificationInstance)
	if err != nil {
		t.Fatal(err)
	}
	controlBinding, controlReader := qualificationBinding(t, db, qualificationInstance, controlAuth)
	controlRegistry, err := db.access.SemanticAttributeRegistry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	controlDB := &qualificationDatabaseExecutor{}
	controlRuntime := qualificationRuntime(t, db, cacheScope, controlBinding, controlReader, controlRegistry, controlDB, controlAuth.Revision)
	controlResolution, err := db.access.ResolveSemanticAttributes(t.Context(), access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: qualificationSubject})
	if err != nil {
		t.Fatal(err)
	}
	historical = append(historical, qualificationFreshRuntimeAudit(t, db, qualificationInstance, controlRuntime, controlResolution, controlDB)...)

	definition, err := db.access.SemanticAttributeDefinition(t.Context(), "region")
	if err != nil {
		t.Fatal(err)
	}
	assignmentRows, err := db.access.SemanticAttributeAssignments(t.Context(), access.SemanticAttributeAssignmentFilter{DefinitionID: definition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: qualificationSubject}})
	if err != nil || len(assignmentRows) != 1 {
		t.Fatalf("read semantic assignment = %#v, %v", assignmentRows, err)
	}
	updatedDefinition, err := db.access.UpdateSemanticAttributeMetadata(t.Context(), access.UpdateSemanticAttributeMetadataInput{Name: "region", Metadata: access.SemanticAttributeMetadata{DisplayName: "Region changed"}, ExpectedVersion: definition.DefinitionVersion, Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: qualificationActor}})
	if err != nil {
		t.Fatal(err)
	}
	beforeRegistry := qualificationAuditRecords(t, db, qualificationInstance)
	if _, err := controlRuntime.ExecuteDataQuery(t.Context(), qualificationRequest()); err == nil {
		t.Fatal("registry mutation reused protected cache")
	}
	afterRegistry := qualificationAuditRecords(t, db, qualificationInstance)
	historical = append(historical, qualificationAuditDelta(beforeRegistry, afterRegistry)...)
	if got := controlDB.queries.Load(); got != 1 {
		t.Fatalf("registry stale physical executions = %d, want 1", got)
	}
	// Definition lifecycle is versioned independently from assignment values;
	// revalidate the assignment against the new definition before constructing
	// the next live authority snapshot.
	currentAssignment, err := db.access.SetSemanticAttributeAssignment(t.Context(), access.SemanticAttributeAssignmentInput{AssignmentID: assignmentRows[0].ID, DefinitionID: updatedDefinition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: qualificationSubject}, Values: []string{"us"}, ExpectedVersion: assignmentRows[0].AssignmentVersion, Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: qualificationActor}})
	if err != nil {
		t.Fatal(err)
	}
	registryBinding, registryReader := qualificationBinding(t, db, qualificationInstance, controlAuth)
	registrySnapshot, err := db.access.SemanticAttributeRegistry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	registryDB := &qualificationDatabaseExecutor{}
	registryRuntime := qualificationRuntime(t, db, cacheScope, registryBinding, registryReader, registrySnapshot, registryDB, controlAuth.Revision)
	registryResolution, err := db.access.ResolveSemanticAttributes(t.Context(), access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: qualificationSubject})
	if err != nil {
		t.Fatal(err)
	}
	historical = append(historical, qualificationFreshRuntimeAudit(t, db, qualificationInstance, registryRuntime, registryResolution, registryDB)...)

	if _, err := db.access.SetSemanticAttributeAssignment(t.Context(), access.SemanticAttributeAssignmentInput{AssignmentID: currentAssignment.ID, DefinitionID: updatedDefinition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: qualificationSubject}, Values: []string{"eu"}, ExpectedVersion: currentAssignment.AssignmentVersion, Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: qualificationActor}}); err != nil {
		t.Fatal(err)
	}
	beforeAssignment := qualificationAuditRecords(t, db, qualificationInstance)
	if _, err := registryRuntime.ExecuteDataQuery(t.Context(), qualificationRequest()); err == nil {
		t.Fatal("assignment mutation reused protected cache")
	}
	afterAssignment := qualificationAuditRecords(t, db, qualificationInstance)
	historical = append(historical, qualificationAuditDelta(beforeAssignment, afterAssignment)...)
	if got := registryDB.queries.Load(); got != 1 {
		t.Fatalf("assignment stale physical executions = %d, want 1", got)
	}
	// Verify the original event objects captured before each transition, not a
	// freshly read copy. This proves retained historical evidence remains
	// immutable and cannot be substituted by a later valid decision.
	for _, record := range historical {
		if err := db.access.VerifySemanticDecisionAuditEvent(t.Context(), record.ID, record.Event); err != nil {
			t.Fatalf("historical semantic decision %s failed retained replay verification: %v", record.ID, err)
		}
	}
}

var _ materialize.Database = (*qualificationDatabaseExecutor)(nil)
var _ materialize.SourcePreparer = qualificationSources{}
var _ materialize.SemanticAccessAuthority = qualificationSemanticAuthority{}
