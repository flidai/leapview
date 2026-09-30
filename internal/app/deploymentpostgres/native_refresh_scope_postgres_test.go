package deploymentpostgres

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/project"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshartifact "github.com/flidai/leapview/internal/refresh/artifact"
	refreshplan "github.com/flidai/leapview/internal/refresh/plan"
	"github.com/flidai/leapview/internal/release"
)

func TestNativeRefreshScopePostgresPlanAndBuildBoundaries(t *testing.T) {
	const sourceDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const attestationDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	identity := projectgraph.ServingIdentity{ProjectID: "project_native_plan", Environment: "prod", GenerationID: "base-native-refresh-scope"}
	source, artifacts, fullPlan, partialPlan := nativeRefreshScopePostgresFixture(t, sourceDigest, attestationDigest, identity)

	// The planner sees the full compiled warehouse candidate, but the queued
	// orders-only pipeline captures only one model. Admission must reject that
	// mismatch before it asks for target binding evidence or opens a write tx.
	db, repository := nativePlanPostgresDB(t)
	sourceReader := &nativePlanSourceReader{snap: source}
	inspector := &nativePlanArtifactInspector{set: artifacts}
	coordinator := nativePlanCoordinator(t, db, sourceReader, inspector)
	resolverFailure := errors.New("binding resolution sentinel")
	resolver := &nativeConnectionLeaser{err: resolverFailure}
	coordinator.bindingEvidence = resolver
	request := nativePlanRequest()
	request.Operation = string(deployment.DeliveryOperationRestatement)
	request.PipelinePlan = &partialPlan
	if _, err := coordinator.CreatePlan(t.Context(), request); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("partial native refresh plan error = %v, want delivery conflict", err)
	}
	if sourceReader.count() != 1 || inspector.count() != 1 || resolver.resolveCalls != 0 {
		t.Fatalf("partial scope reached source/inspector/binding resolver %d/%d/%d, want 1/1/0", sourceReader.count(), inspector.count(), resolver.resolveCalls)
	}
	if _, err := repository.Target(t.Context(), request.TargetID); !errors.Is(err, deploymentnative.ErrNotFound) {
		t.Fatalf("partial scope created target before rejection: %v", err)
	}
	var operations, plans, events, audits int
	if err := db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM platform.operation), (SELECT count(*) FROM delivery.delivery_plan), (SELECT count(*) FROM event.event_log), (SELECT count(*) FROM audit.audit_event)`).Scan(&operations, &plans, &events, &audits); err != nil {
		t.Fatal(err)
	}
	if operations != 0 || plans != 0 || events != 0 || audits != 0 {
		t.Fatalf("partial scope persisted operation/plan/event/audit %d/%d/%d/%d", operations, plans, events, audits)
	}

	// The same real candidate passes inspection scope validation when the
	// captured plan covers its complete model and source closure. Stop at a
	// sentinel binding error before the write tx; reaching the resolver proves
	// the exact scope passed the guard.
	request.PipelinePlan = &fullPlan
	if _, err := coordinator.CreatePlan(t.Context(), request); !errors.Is(err, resolverFailure) {
		t.Fatalf("full native refresh plan error = %v, want binding resolver sentinel", err)
	}
	if sourceReader.count() != 2 || inspector.count() != 2 || resolver.resolveCalls != 1 {
		t.Fatalf("full scope reached source/inspector/binding resolver %d/%d/%d, want 2/2/1", sourceReader.count(), inspector.count(), resolver.resolveCalls)
	}

	buildRequest := deploymentmodule.NativeDeliveryBuildRequest{ProjectID: identity.ProjectID, TargetID: request.TargetID, Environment: request.Environment}
	deliveryPlan := deployment.DeliveryPlan{SourceDigest: sourceDigest, PipelinePlan: &partialPlan}
	if err := validateNativeBuildArtifacts(artifacts, buildRequest, deliveryPlan); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("partial native build scope error = %v, want delivery conflict", err)
	}
	deliveryPlan.PipelinePlan = &fullPlan
	if err := validateNativeBuildArtifacts(artifacts, buildRequest, deliveryPlan); err != nil {
		t.Fatalf("full native build scope: %v", err)
	}
}

func nativeRefreshScopePostgresFixture(t *testing.T, sourceDigest, attestationDigest string, identity projectgraph.ServingIdentity) (project.CandidateSourceSnapshot, release.CandidateArtifactSet, projectpipelineplan.Plan, projectpipelineplan.Plan) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve native refresh test fixture location")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	exampleRoot := filepath.Join(repositoryRoot, "examples", "dbt-warehouse-boundary", "leapview")
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(exampleRoot)); err != nil {
		t.Fatalf("copy compiled fixture: %v", err)
	}

	writeFixtureFile := func(name, value string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFixtureFile("connections/warehouse.yaml", `apiVersion: leapview.dev/v1
kind: Connection
metadata:
  id: connection:warehouse
  name: warehouse
spec:
  type: postgres
`)
	for name, relation := range map[string]string{
		"sources/dim_customers.yaml": "dim_customers",
		"sources/fct_orders.yaml":    "fct_orders",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read fixture source %s: %v", name, err)
		}
		text := string(contents)
		start := strings.Index(text, "  location:\n")
		if start < 0 {
			t.Fatalf("fixture source %s has no expected location block", name)
		}
		end := strings.Index(text[start:], "  schema:\n")
		if end < 0 {
			t.Fatalf("fixture source %s location has no schema boundary", name)
		}
		end += start
		text = text[:start] + "  location:\n    type: relation\n    catalog: analytics\n    schema: commerce\n    name: " + relation + "\n" + text[end:]
		writeFixtureFile(name, text)
	}
	writeFixtureFile("semantic-models/orders-only.yaml", `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata:
  id: semantic-model:orders-only
  name: orders-only
spec:
  datasets:
    - name: orders
      model: orders
`)
	writeFixtureFile("pipelines/full-refresh.yaml", `apiVersion: leapview.dev/v1
kind: Pipeline
metadata:
  id: pipeline:full-refresh
  name: full-refresh
spec:
  selection:
    semanticModel: warehouse_sales
`)
	writeFixtureFile("pipelines/orders-only.yaml", `apiVersion: leapview.dev/v1
kind: Pipeline
metadata:
  id: pipeline:orders-only-refresh
  name: orders-only-refresh
spec:
  selection:
    semanticModel: orders-only
`)

	compiled, err := projectcompiler.Compile(root)
	if err != nil {
		t.Fatalf("compile native refresh scope fixture: %v", err)
	}
	bundlePlan, err := projectcompiler.PlanSourceRoot(root)
	if err != nil {
		t.Fatalf("plan native refresh scope fixture: %v", err)
	}
	activations, err := compiled.ConnectionActivations()
	if err != nil {
		t.Fatalf("resolve fixture connection activations: %v", err)
	}
	var requirements []release.CandidateConnectionRequirement
	for _, activation := range activations {
		if activation.Mode != projectartifact.TargetBindingActivation {
			t.Fatalf("fixture warehouse activation = %q, want target binding", activation.Mode)
		}
		id, err := projectgraph.NewResourceID(activation.LogicalConnectionID)
		if err != nil {
			t.Fatal(err)
		}
		requirements = append(requirements, release.CandidateConnectionRequirement{ConnectionID: id, ConnectorKind: activation.ConnectorKind, Access: activation.Access})
	}
	if len(requirements) != 1 {
		t.Fatalf("fixture target binding requirements = %d, want one", len(requirements))
	}
	artifacts := release.CandidateArtifactSet{
		Artifact:                    release.ProjectArtifactProvenance{SourceDigest: sourceDigest, ProjectDigest: compiled.Digest(), CompilerVersion: projectartifact.CompilerVersion, SchemaVersion: projectartifact.Version},
		AuthorizationPolicyRevision: 1, AuthorizationPolicyDigest: createPlanTestDigest('b'), AuthorizationFingerprint: createPlanTestDigest('c'),
		Generation: release.CandidateGenerationArtifact{Identity: identity, DataRevision: "sources:1", DataMode: release.GenerationDataRefreshSources, Deterministic: true, Connections: requirements},
		Compiler:   release.CandidateCompilerEvidence{Graph: compiled.Graph(), Manifest: compiled.Manifest(), Artifact: compiled, Plan: bundlePlan},
	}
	snapshot := project.CandidateSourceSnapshot{ProjectID: identity.ProjectID, ArtifactDigest: sourceDigest, SourceAttestationDigest: attestationDigest, ProjectDigest: compiled.Digest()}
	definition := compiled.RefreshDefinition()
	full, err := nativeRefreshScopePostgresPipelinePlan(t, definition, identity, sourceDigest, "pipeline:full-refresh")
	if err != nil {
		t.Fatal(err)
	}
	partial, err := nativeRefreshScopePostgresPipelinePlan(t, definition, identity, sourceDigest, "pipeline:orders-only-refresh")
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, artifacts, full, partial
}

func nativeRefreshScopePostgresPipelinePlan(t *testing.T, definition *refreshartifact.Definition, identity projectgraph.ServingIdentity, sourceDigest, pipelineID string) (projectpipelineplan.Plan, error) {
	t.Helper()
	refresh, err := refreshplan.ForPipeline(definition, identity.ProjectID, projectgraph.ResourceID(pipelineID))
	if err != nil {
		return projectpipelineplan.Plan{}, err
	}
	bound, err := refresh.BindGeneration(identity, sourceDigest)
	if err != nil {
		return projectpipelineplan.Plan{}, err
	}
	return bound.DeliveryPipelinePlan(refreshplan.InvocationPolicy{InvocationSource: "manual"})
}
