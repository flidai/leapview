package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/flidai/leapview/internal/access"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const canonicalProject = projectgraph.ResourceID("project:sales")

type canonicalMetrics struct {
	queryruntime.Metrics
	model      *semanticmodel.Model
	result     dataquery.Result
	arrowError error
}

func (m canonicalMetrics) Catalog() catalog.Catalog {
	return catalog.Catalog{Project: catalog.Project{ID: canonicalProject}}
}
func (m canonicalMetrics) SemanticModel(id string) (*semanticmodel.Model, bool) {
	return m.model, m.model != nil && (id == string(m.model.Name) || id == "semantic_model:sales")
}
func (m canonicalMetrics) ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error) {
	return m.result, nil
}
func (m canonicalMetrics) ExecuteDataQueryArrow(_ context.Context, _ dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	if sink != nil {
		if err := sink.WriteSchema((*arrow.Schema)(nil)); err != nil {
			return dataquery.Result{}, err
		}
	}
	return m.result, m.arrowError
}

type canonicalAuditRecorder struct {
	events []access.CanonicalAuditEvent
	err    error
}

func (r *canonicalAuditRecorder) RecordCanonicalAuditEvent(_ context.Context, event access.CanonicalAuditEvent) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, event)
	return nil
}

func canonicalGraph(t testing.TB) (projectgraph.ProjectGraph, projectgraph.ServingIdentity, access.ResourceRef, access.ResourceRef, access.ResourceRef) {
	t.Helper()
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: canonicalProject, Kind: projectgraph.KindProject, Name: "sales_project"},
		{ID: "semantic_model:sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
		{ID: "model:ratings", Kind: projectgraph.KindModel, Name: "ratings"},
		{ID: "dashboard:dash", Kind: projectgraph.KindDashboard, Name: "dash"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(canonicalProject, "production", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	semantic, _ := access.NewResourceRef("semantic_model:sales", projectgraph.KindSemanticModel)
	physical, _ := access.NewResourceRef("model:ratings", projectgraph.KindModel)
	dashboard, _ := access.NewResourceRef("dashboard:dash", projectgraph.KindDashboard)
	return graph, identity, semantic, physical, dashboard
}

func canonicalSnapshot(t testing.TB, grants []struct {
	id         string
	resource   access.ResourceRef
	capability access.Capability
}, policies []accesssnapshot.DataPolicy) accesssnapshot.AuthorizationSnapshot {
	t.Helper()
	graph, identity, _, _, _ := canonicalGraph(t)
	canonicalGrants := make([]accesssnapshot.Grant, 0, len(grants))
	for _, item := range grants {
		subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
		if err != nil {
			t.Fatal(err)
		}
		grant, err := access.NewCanonicalGrant(graph, subject, item.resource, item.capability)
		if err != nil {
			t.Fatal(err)
		}
		canonicalGrants = append(canonicalGrants, accesssnapshot.Grant{ID: item.id, Canonical: grant})
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, canonicalGrants, policies)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func canonicalMetricsWithSnapshot(t testing.TB, snapshot accesssnapshot.AuthorizationSnapshot, recorder access.CanonicalAuditRecorder, arrowError ...error) Metrics {
	t.Helper()
	underlying := canonicalMetrics{model: &semanticmodel.Model{Name: "sales", Sources: map[string]semanticmodel.Source{"ratings": {}}, Tables: map[string]semanticmodel.Table{"ratings": {Source: "ratings", Dimensions: map[string]semanticmodel.MetricDimension{"region": {Field: "ratings.region", Table: "ratings", Name: "region"}}}}, Dimensions: map[string]semanticmodel.SemanticDimension{"region": {Name: "region", Bindings: map[string]semanticmodel.DimensionBinding{"ratings": {Field: "ratings.region"}}}}}}
	if len(arrowError) > 0 {
		underlying.arrowError = arrowError[0]
	}
	return New(underlying, Options{
		SnapshotFromContext: func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return snapshot, nil },
		SubjectsFromContext: func(context.Context, string) ([]access.SubjectRef, error) {
			subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
			return []access.SubjectRef{subject}, err
		},
		PrincipalFromContext:  func(context.Context) (Principal, bool) { return Principal{ID: "alice"}, true },
		CredentialFromContext: func(context.Context) (access.APICredential, bool) { return access.APICredential{}, false },
		AuditRecorder:         recorder,
	})
}

func TestCanonicalSemanticAndPhysicalAuthorization(t *testing.T) {
	_, _, semantic, physical, _ := canonicalGraph(t)
	semanticSnapshot := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"semantic", semantic, access.CapabilityResourceUse}}, nil)
	metrics := canonicalMetricsWithSnapshot(t, semanticSnapshot, nil)
	semanticQuery := dataquery.Query{ProjectID: canonicalProject, ModelID: "semantic_model:sales", Kind: dataquery.KindSemanticRows}
	if _, _, err := metrics.GovernDataQuery(context.Background(), semanticQuery); err != nil {
		t.Fatalf("semantic authorization: %v", err)
	}
	physicalOnlySnapshot := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"physical", physical, access.CapabilityResourceUse}}, nil)
	physicalOnly := canonicalMetricsWithSnapshot(t, physicalOnlySnapshot, nil)
	physicalQuery := dataquery.Query{ProjectID: canonicalProject, ModelID: "semantic_model:sales", Target: "model:ratings", Kind: dataquery.KindSemanticRows}
	if _, _, err := physicalOnly.GovernDataQuery(context.Background(), physicalQuery); err != nil {
		t.Fatalf("physical authorization: %v", err)
	}
	if _, _, err := physicalOnly.GovernDataQuery(context.Background(), semanticQuery); err == nil {
		t.Fatal("semantic query unexpectedly used a physical-only grant")
	}
	unknownPhysical := physicalQuery
	unknownPhysical.Target = "model:unknown"
	if _, _, err := physicalOnly.GovernDataQuery(context.Background(), unknownPhysical); err == nil {
		t.Fatal("query with an unbound physical dependency was authorized")
	}
}

func TestCanonicalRLSMasksAndPolicyFingerprint(t *testing.T) {
	_, _, _, physical, _ := canonicalGraph(t)
	row, err := accesspolicy.Compile("rls", "row_filter", `{"field":"ratings.region","operator":"equals","values":["EU"]}`)
	if err != nil {
		t.Fatal(err)
	}
	mask, err := accesspolicy.Compile("mask", "column_mask", `{"field":"ratings.email","mask":"null"}`)
	if err != nil {
		t.Fatal(err)
	}
	policies := []accesssnapshot.DataPolicy{{ID: "rls", Resource: physical, PolicyType: "row_filter", ExpressionJSON: `{"field":"ratings.region","operator":"equals","values":["EU"]}`, Compiled: row}, {ID: "mask", Resource: physical, PolicyType: "column_mask", ExpressionJSON: `{"field":"ratings.email","mask":"null"}`, Compiled: mask}}
	snapshot := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"physical", physical, access.CapabilityResourceUse}}, policies)
	metrics := canonicalMetricsWithSnapshot(t, snapshot, nil)
	request := dataquery.Query{ProjectID: canonicalProject, ModelID: "semantic_model:sales", Target: "model:ratings", Kind: dataquery.KindSemanticRows, Fields: []dataquery.Field{{Field: "ratings.email"}}}
	governed, _, err := metrics.GovernDataQuery(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(governed.Filters) != 1 || len(governed.ColumnMasks) != 1 {
		t.Fatalf("governed query = %#v, want RLS and mask", governed)
	}
	if governed.EffectivePolicyFingerprint == "" {
		t.Fatal("effective policy fingerprint is empty")
	}
	usCompiled, err := accesspolicy.Compile("rls-us", "row_filter", `{"field":"ratings.region","operator":"equals","values":["US"]}`)
	if err != nil {
		t.Fatal(err)
	}
	usSnapshot := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"physical", physical, access.CapabilityResourceUse}}, []accesssnapshot.DataPolicy{{ID: "rls-us", Resource: physical, PolicyType: "row_filter", ExpressionJSON: `{"field":"ratings.region","operator":"equals","values":["US"]}`, Compiled: usCompiled}})
	usGoverned, _, err := canonicalMetricsWithSnapshot(t, usSnapshot, nil).GovernDataQuery(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if governed.EffectivePolicyFingerprint == usGoverned.EffectivePolicyFingerprint {
		t.Fatal("policy fingerprint did not isolate changed RLS policy")
	}
}

func TestCanonicalTokenAttenuationAndProjectIdentity(t *testing.T) {
	_, identity, semantic, _, _ := canonicalGraph(t)
	snapshot := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"semantic", semantic, access.CapabilityResourceUse}}, nil)
	currentSnapshot := snapshot
	var tokenCaps []access.Capability
	metrics := New(canonicalMetrics{model: &semanticmodel.Model{Name: "sales"}}, Options{
		SnapshotFromContext: func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return currentSnapshot, nil },
		SubjectsFromContext: func(context.Context, string) ([]access.SubjectRef, error) {
			subject, _ := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
			return []access.SubjectRef{subject}, nil
		},
		PrincipalFromContext: func(context.Context) (Principal, bool) { return Principal{ID: "alice"}, true },
		CredentialFromContext: func(context.Context) (access.APICredential, bool) {
			return access.APICredential{Token: access.APIToken{Capabilities: tokenCaps}}, true
		},
	})
	query := dataquery.Query{ProjectID: canonicalProject, ModelID: "semantic_model:sales", Kind: dataquery.KindSemanticRows}
	// Nil means dynamic attenuation and follows current effective capabilities.
	tokenCaps = nil
	if _, _, err := metrics.GovernDataQuery(context.Background(), query); err != nil {
		t.Fatalf("nil token attenuation: %v", err)
	}
	// A non-nil empty list is explicit deny-all, not dynamic.
	tokenCaps = []access.Capability{}
	if _, _, err := metrics.GovernDataQuery(context.Background(), query); err == nil {
		t.Fatal("explicit empty token unexpectedly authorized")
	}
	// An allowlist narrower than the effective capability set is denied.
	tokenCaps = []access.Capability{access.CapabilityResourceRead}
	if _, _, err := metrics.GovernDataQuery(context.Background(), query); err == nil {
		t.Fatal("attenuated token unexpectedly authorized RESOURCE_USE")
	}
	// Dynamic tokens are re-evaluated against a revoked serving snapshot.
	tokenCaps = nil
	graph, revokedIdentity, _, _, _ := canonicalGraph(t)
	revoked, err := accesssnapshot.NewAuthorizationSnapshot(revokedIdentity, graph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	currentSnapshot = revoked
	if _, _, err := metrics.GovernDataQuery(context.Background(), query); err == nil {
		t.Fatal("revoked dynamic token retained authorization")
	}
	_ = identity
}

func TestCanonicalActiveProjectIdentityRejectsMismatch(t *testing.T) {
	_, _, semantic, _, _ := canonicalGraph(t)
	snapshot := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"semantic", semantic, access.CapabilityResourceUse}}, nil)
	metrics := canonicalMetricsWithSnapshot(t, snapshot, nil)
	_, _, err := metrics.GovernDataQuery(context.Background(), dataquery.Query{ProjectID: "project:other", ModelID: "semantic_model:sales", Kind: dataquery.KindSemanticRows})
	if err == nil {
		t.Fatal("project mismatch was authorized")
	}
}

func TestCanonicalPublicPublicationAndCandidateClosures(t *testing.T) {
	_, _, semantic, physical, dashboard := canonicalGraph(t)
	capability := DashboardPublicationCapability{ProjectID: canonicalProject, Publication: "public", Dashboard: dashboard, ModelID: semantic, DependencyAssetIDs: []access.ResourceRef{dashboard, semantic, physical}}
	objects := []access.ResourceRef{semantic, physical}
	request := dataquery.Query{ProjectID: canonicalProject, Surface: dataquery.SurfacePublicDashboard, Operation: dataquery.OperationDashboardRows, ModelID: semantic.CanonicalID(), Kind: dataquery.KindSemanticRows}
	if err := validateDashboardPublicationQuery(capability, request, objects); err != nil {
		t.Fatal(err)
	}
	request.ModelID = "semantic_model:other"
	if err := validateDashboardPublicationQuery(capability, request, objects); err == nil {
		t.Fatal("publication model expansion was accepted")
	}
	candidate := CandidateQueryCapability{CandidateID: "candidate-1", OwnerPrincipalID: "alice", ProjectID: canonicalProject, PolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	validated, err := validateCandidateQueryCapability(candidate, Principal{ID: "alice"}, dataquery.Query{ProjectID: canonicalProject})
	if err != nil || validated.CandidateID != candidate.CandidateID {
		t.Fatalf("candidate validation = %#v, %v", validated, err)
	}
	malformed := candidate
	malformed.Restrictions = []accesssnapshot.DataPolicy{{ID: "bad", PolicyType: "row_filter"}}
	if _, err := validateCandidateQueryCapability(malformed, Principal{ID: "alice"}, dataquery.Query{ProjectID: canonicalProject}); err == nil {
		t.Fatal("candidate accepted a zero canonical resource restriction")
	}
	malformedPublication := capability
	malformedPublication.Dashboard = access.ResourceRef{}
	if err := validateDashboardPublicationQuery(malformedPublication, dataquery.Query{ProjectID: canonicalProject, Surface: dataquery.SurfacePublicDashboard, Operation: dataquery.OperationDashboardRows, ModelID: semantic.CanonicalID(), Kind: dataquery.KindSemanticRows}, objects); err == nil {
		t.Fatal("publication accepted a zero canonical dashboard resource")
	}
	outside, _ := access.NewResourceRef("model:outside", projectgraph.KindModel)
	if err := validateDashboardPublicationQuery(capability, request, []access.ResourceRef{outside}); err == nil {
		t.Fatal("publication accepted dependency expansion outside its compiled closure")
	}
}

func TestCanonicalPolicyAlgebraAndCandidateRestrictions(t *testing.T) {
	_, _, semantic, physical, _ := canonicalGraph(t)
	globalCompiled, err := accesspolicy.Compile("global", "row_filter", `{"field":"ratings.region","operator":"equals","values":["EU"]}`)
	if err != nil {
		t.Fatal(err)
	}
	group, _ := access.NewSubjectRef(access.SubjectKindGroup, "analysts")
	groupCompiled, err := accesspolicy.Compile("group", "row_filter", `{"field":"ratings.region","operator":"equals","values":["US"]}`)
	if err != nil {
		t.Fatal(err)
	}
	composition, err := composeDataPolicies([]accesssnapshot.DataPolicy{{ID: "global", Resource: physical, PolicyType: "row_filter", ExpressionJSON: `{"field":"ratings.region","operator":"equals","values":["EU"]}`, Compiled: globalCompiled}, {ID: "group", Resource: physical, Subject: &group, PolicyType: "row_filter", ExpressionJSON: `{"field":"ratings.region","operator":"equals","values":["US"]}`, Compiled: groupCompiled}}, nil)
	if err != nil || len(composition.Filters) != 2 {
		t.Fatalf("policy algebra = %#v, %v", composition, err)
	}
	first, _ := accesspolicy.Compile("mask-null", "column_mask", `{"field":"ratings.email","mask":"null"}`)
	second, _ := accesspolicy.Compile("mask-redact", "column_mask", `{"field":"ratings.email","mask":"redact"}`)
	if _, err := composeDataPolicies([]accesssnapshot.DataPolicy{{ID: "null", Resource: physical, PolicyType: "column_mask", ExpressionJSON: `{"field":"ratings.email","mask":"null"}`, Compiled: first}, {ID: "redact", Resource: physical, PolicyType: "column_mask", ExpressionJSON: `{"field":"ratings.email","mask":"redact"}`, Compiled: second}}, nil); err == nil {
		t.Fatal("contradictory masks were accepted")
	}
	rowCompiled, _ := accesspolicy.Compile("candidate", "row_filter", `{"field":"ratings.region","operator":"equals","values":["EU"]}`)
	snapshot := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"physical-use", physical, access.CapabilityResourceUse}, {"physical-read", physical, access.CapabilityResourceRead}}, nil)
	metrics := canonicalMetricsWithSnapshot(t, snapshot, nil)
	ctx := WithCandidateQueryCapability(context.Background(), CandidateQueryCapability{CandidateID: "candidate-1", OwnerPrincipalID: "alice", ProjectID: canonicalProject, PolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Restrictions: []accesssnapshot.DataPolicy{{ID: "candidate", Resource: physical, PolicyType: "row_filter", ExpressionJSON: `{"field":"ratings.region","operator":"equals","values":["EU"]}`, Compiled: rowCompiled}}})
	governed, _, err := metrics.GovernDataQuery(ctx, dataquery.Query{ProjectID: canonicalProject, ModelID: semantic.CanonicalID(), Target: physical.CanonicalID(), Kind: dataquery.KindSemanticRows})
	if err != nil || len(governed.Filters) != 1 {
		t.Fatalf("candidate restriction = %#v, %v", governed, err)
	}
}

func TestCanonicalViewAsRequiresProjectAdmin(t *testing.T) {
	_, _, _, _, _ = canonicalGraph(t)
	graph, identity, _, _, _ := canonicalGraph(t)
	projectRef, _ := access.NewResourceRef(canonicalProject, projectgraph.KindProject)
	subject, _ := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
	grant, err := access.NewCanonicalGrant(graph, subject, projectRef, access.CapabilityProjectAdmin)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, []accesssnapshot.Grant{{ID: "admin", Canonical: grant}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	metrics := canonicalMetricsWithSnapshot(t, snapshot, nil)
	request := dataquery.Query{ProjectID: canonicalProject, ModelID: "semantic_model:sales", Kind: dataquery.KindSemanticRows}
	ctx := WithViewAsCapability(context.Background(), ViewAsCapability{ActorPrincipalID: "alice", SubjectPrincipalID: "bob", ProjectID: canonicalProject})
	if _, err := metrics.authorizeViewAs(ctx, Principal{ID: "alice"}, request, ViewAsCapability{ActorPrincipalID: "alice", SubjectPrincipalID: "bob", ProjectID: canonicalProject}); err != nil {
		t.Fatal(err)
	}
	deniedSnapshot := canonicalSnapshot(t, nil, nil)
	deniedMetrics := canonicalMetricsWithSnapshot(t, deniedSnapshot, nil)
	if _, err := deniedMetrics.authorizeViewAs(ctx, Principal{ID: "alice"}, request, ViewAsCapability{ActorPrincipalID: "alice", SubjectPrincipalID: "bob", ProjectID: canonicalProject}); err == nil {
		t.Fatal("view-as without PROJECT_ADMIN was authorized")
	}
}

func TestCanonicalArrowAuditRecordsSuccessAndFailure(t *testing.T) {
	_, _, semantic, _, dashboard := canonicalGraph(t)
	snapshot := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"semantic", semantic, access.CapabilityResourceUse}}, nil)
	recorder := &canonicalAuditRecorder{}
	metrics := canonicalMetricsWithSnapshot(t, snapshot, recorder)
	query := dataquery.Query{ProjectID: canonicalProject, ModelID: semantic.CanonicalID(), Kind: dataquery.KindSemanticRows}
	if _, err := metrics.ExecuteDataQueryArrow(context.Background(), query, nil); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != 1 || recorder.events[0].Status != "success" {
		t.Fatalf("audit events = %#v", recorder.events)
	}
	publication := DashboardPublicationCapability{ProjectID: canonicalProject, Publication: "public", Dashboard: dashboard, ModelID: semantic, DependencyAssetIDs: []access.ResourceRef{dashboard, semantic}}
	publicQuery := query
	publicQuery.Surface = dataquery.SurfacePublicDashboard
	publicQuery.Operation = dataquery.OperationDashboardRows
	publicQuery.Fields = []dataquery.Field{{Field: "region"}}
	publicQuery.Target = "ratings"
	publicCtx := WithDashboardPublicationCapability(context.Background(), publication)
	publicMetrics := canonicalMetricsWithSnapshot(t, snapshot, recorder)
	if _, err := publicMetrics.ExecuteDataQueryArrow(publicCtx, publicQuery, nil); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != 3 || recorder.events[1].Status != "started" || recorder.events[2].Status != "success" {
		t.Fatalf("durable success audit events = %#v", recorder.events)
	}
	errorRecorder := &canonicalAuditRecorder{}
	errorMetrics := canonicalMetricsWithSnapshot(t, snapshot, errorRecorder, errors.New("executor failed"))
	if _, err := errorMetrics.ExecuteDataQueryArrow(publicCtx, publicQuery, nil); err == nil {
		t.Fatal("Arrow executor failure was swallowed")
	}
	if len(errorRecorder.events) != 2 || errorRecorder.events[0].Status != "started" || errorRecorder.events[1].Status != "error" {
		t.Fatalf("durable failure audit events = %#v", errorRecorder.events)
	}
}
