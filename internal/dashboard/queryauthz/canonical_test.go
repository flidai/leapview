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

func canonicalMetricsWithSnapshot(t testing.TB, snapshot accesssnapshot.AuthorizationSnapshot, recorder access.CanonicalAuditRecorder) Metrics {
	t.Helper()
	return New(canonicalMetrics{model: &semanticmodel.Model{Name: "sales"}}, Options{
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
	graph, _, semantic, physical, _ := canonicalGraph(t)
	_ = graph
	snapshot := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"semantic", semantic, access.CapabilityResourceUse}, {"physical", physical, access.CapabilityResourceRead}}, nil)
	metrics := canonicalMetricsWithSnapshot(t, snapshot, nil)
	semanticQuery := dataquery.Query{ProjectID: canonicalProject, ModelID: "semantic_model:sales", Kind: dataquery.KindSemanticRows}
	if _, _, err := metrics.GovernDataQuery(context.Background(), semanticQuery); err != nil {
		t.Fatalf("semantic authorization: %v", err)
	}
	physicalQuery := dataquery.Query{ProjectID: canonicalProject, ModelID: "semantic_model:sales", Target: "model:ratings", Kind: dataquery.KindSemanticRows}
	if _, _, err := metrics.GovernDataQuery(context.Background(), physicalQuery); err != nil {
		t.Fatalf("physical authorization: %v", err)
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
}

func TestCanonicalTokenAttenuationAndProjectIdentity(t *testing.T) {
	_, identity, semantic, _, _ := canonicalGraph(t)
	snapshot := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"semantic", semantic, access.CapabilityResourceUse}}, nil)
	metrics := New(canonicalMetrics{model: &semanticmodel.Model{Name: "sales"}}, Options{
		SnapshotFromContext: func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return snapshot, nil },
		SubjectsFromContext: func(context.Context, string) ([]access.SubjectRef, error) {
			subject, _ := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
			return []access.SubjectRef{subject}, nil
		},
		PrincipalFromContext: func(context.Context) (Principal, bool) { return Principal{ID: "alice"}, true },
		CredentialFromContext: func(context.Context) (access.APICredential, bool) {
			token := access.APIToken{Capabilities: []access.Capability{access.CapabilityResourceRead}}
			return access.APICredential{Token: token}, true
		},
	})
	query := dataquery.Query{ProjectID: canonicalProject, ModelID: "semantic_model:sales", Kind: dataquery.KindSemanticRows}
	if _, _, err := metrics.GovernDataQuery(context.Background(), query); err == nil {
		t.Fatal("attenuated token unexpectedly authorized RESOURCE_USE")
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
	recorder.err = errors.New("durable audit unavailable")
	publication := DashboardPublicationCapability{ProjectID: canonicalProject, Publication: "public", Dashboard: dashboard, ModelID: semantic, DependencyAssetIDs: []access.ResourceRef{dashboard, semantic}}
	publicQuery := query
	publicQuery.Surface = dataquery.SurfacePublicDashboard
	publicQuery.Operation = dataquery.OperationDashboardRows
	publicCtx := WithDashboardPublicationCapability(context.Background(), publication)
	if _, err := metrics.ExecuteDataQueryArrow(publicCtx, publicQuery, nil); err == nil {
		t.Fatal("durable Arrow audit failure was swallowed")
	}
}
