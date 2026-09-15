//go:build duckdb_arrow

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	appruntimefactory "github.com/flidai/leapview/internal/app/runtimefactory"
	"github.com/flidai/leapview/internal/deployment"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/servingstate"
)

const dbtProofTarget = "dbt-target-production-boundary-proof"

type dbtProofBoundDelivery struct {
	Plan        deployment.DeliveryPlan
	State       servingstate.State
	Artifact    servingstate.Artifact
	ManagedData runtimehost.ManagedDataResolution
	Spec        warehouseBoundarySpec
}

func newDBTProofBoundDelivery(
	input deployment.DeliveryCandidateBuildInput,
	artifacts release.CandidateArtifactSet,
	runtimeVersion string,
	now time.Time,
	roots map[string]string,
	accessPolicy semanticmodel.SemanticAccessPolicy,
	producerTarget string,
) (dbtProofBoundDelivery, error) {
	if err := validateDBTProofConnectionRequirements(artifacts); err != nil {
		return dbtProofBoundDelivery{}, err
	}
	artifacts, bundleBytes, err := prepareDBTProofServingArtifact(artifacts)
	if err != nil {
		return dbtProofBoundDelivery{}, err
	}
	request, err := appruntimefactory.CandidatePlanRequest(input, artifacts, runtimeVersion, now)
	if err != nil {
		return dbtProofBoundDelivery{}, err
	}
	request.ServingArtifactDigest = artifacts.Generation.ArtifactDigest
	return bindDBTProofDeliveryRequest(input, artifacts, request, bundleBytes, roots, accessPolicy, producerTarget)
}

func bindDBTProofDeliveryRequest(
	input deployment.DeliveryCandidateBuildInput,
	artifacts release.CandidateArtifactSet,
	request deployment.DeliveryPlanRequest,
	bundleBytes []byte,
	roots map[string]string,
	accessPolicy semanticmodel.SemanticAccessPolicy,
	producerTarget string,
) (dbtProofBoundDelivery, error) {
	if err := validateDBTProofConnectionRequirements(artifacts); err != nil {
		return dbtProofBoundDelivery{}, err
	}
	if err := request.Governance.Validate(); err != nil {
		return dbtProofBoundDelivery{}, fmt.Errorf("delivery governance: %w", err)
	}
	if request.Governance.PolicyDigest != artifacts.AuthorizationFingerprint || request.Governance.AuthorizationDigest != artifacts.AuthorizationFingerprint {
		return dbtProofBoundDelivery{}, fmt.Errorf("delivery governance is not bound to the candidate authorization fingerprint")
	}
	if producerTarget != dbtProofTarget {
		return dbtProofBoundDelivery{}, fmt.Errorf("dbt target provenance is missing from delivery metadata")
	}
	request.Provenance.BuildDefinition += "+dbt-target:" + producerTarget
	projectID, err := projectgraph.NewResourceID(request.ProjectID)
	if err != nil {
		return dbtProofBoundDelivery{}, fmt.Errorf("delivery project identity: %w", err)
	}
	plan, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID: request.ID, ActorID: request.ActorID, SourceOwnerID: input.OwnerID,
		TargetID: request.TargetID, ProjectID: projectID, Environment: request.Environment,
		Operation: request.Operation, SourceDigest: request.SourceDigest,
		ServingArtifactDigest: request.ServingArtifactDigest,
		Execution:             request.Execution, Provenance: request.Provenance,
		Governance: request.Governance, Evidence: request.Evidence,
		PipelinePlan: request.PipelinePlan, CreatedAt: request.CreatedAt,
	})
	if err != nil {
		return dbtProofBoundDelivery{}, fmt.Errorf("validate production-shaped delivery plan: %w", err)
	}
	if err := plan.Governance.Validate(); err != nil {
		return dbtProofBoundDelivery{}, fmt.Errorf("validated plan governance: %w", err)
	}

	validation, compiled, err := projectbundle.ValidateArtifactBytes(bundleBytes)
	if err != nil {
		return dbtProofBoundDelivery{}, fmt.Errorf("validate persisted serving artifact: %w", err)
	}
	if validation.Digest != plan.ServingArtifactDigest || artifacts.Generation.ArtifactDigest != plan.ServingArtifactDigest {
		return dbtProofBoundDelivery{}, fmt.Errorf("persisted serving artifact differs from validated delivery plan")
	}
	decodedArtifact, err := projectartifact.NewSourceBundle(compiled.Graph, compiled.Manifest)
	if err != nil {
		return dbtProofBoundDelivery{}, fmt.Errorf("decode persisted serving artifact: %w", err)
	}
	if decodedArtifact.Digest() != plan.SourceDigest {
		return dbtProofBoundDelivery{}, fmt.Errorf("persisted serving artifact has different portable source identity")
	}
	manifest := decodedArtifact.Manifest()
	model := manifest.SemanticModels["semantic-model:warehouse_sales"]
	if model == nil {
		return dbtProofBoundDelivery{}, fmt.Errorf("compiled warehouse_sales semantic model is missing")
	}
	if len(roots) != len(model.Connections) {
		return dbtProofBoundDelivery{}, fmt.Errorf("delivery roots=%d, want exact connection closure of %d", len(roots), len(model.Connections))
	}
	model.AccessPolicy = accessPolicy
	managedRoots := make(map[string]string, len(roots))
	managedRevisions := make(map[string]string, len(artifacts.Generation.ManagedDataPins))
	for _, pin := range artifacts.Generation.ManagedDataPins {
		managedRevisions[pin.ConnectionID] = pin.RevisionID
		name := strings.TrimPrefix(pin.ConnectionID, "connection:")
		root := strings.TrimSpace(roots[name])
		if root == "" {
			return dbtProofBoundDelivery{}, fmt.Errorf("delivery root for connection %q is missing", pin.ConnectionID)
		}
		managedRoots[pin.ConnectionID] = root
	}
	managedData := runtimehost.ManagedDataResolution{RevisionID: artifacts.Generation.DataRevision, Roots: managedRoots, Revisions: managedRevisions}

	generationID := artifacts.Generation.Identity.GenerationID
	stateID := servingstate.ID(generationID)
	state := servingstate.State{
		ID: stateID, ProjectID: plan.ProjectID, Environment: servingstate.Environment(plan.Environment),
		Status: servingstate.StatusValidated, ProjectDigest: plan.SourceDigest, Digest: plan.ServingArtifactDigest,
	}
	servingArtifact := servingstate.Artifact{
		ID: artifacts.Generation.ServingArtifactID, ServingStateID: stateID, Digest: plan.ServingArtifactDigest,
		Format: projectbundle.BundleFormat, ContentType: projectbundle.BundleContentType,
		ManifestJSON: artifacts.Generation.BundleManifestJSON, SizeBytes: int64(len(bundleBytes)),
	}
	graph := decodedArtifact.Graph()
	spec := warehouseBoundarySpec{
		root: filepath.Dir(roots["warehouse"]), compiled: model, graph: &graph, targetID: plan.TargetID,
		deliveryPlan: &plan, bundleBytes: bundleBytes, managedData: managedData,
	}
	return dbtProofBoundDelivery{
		Plan: plan, State: state, Artifact: servingArtifact, ManagedData: managedData, Spec: spec,
	}, nil
}

type dbtProofManagedDataResolver struct {
	resolution runtimehost.ManagedDataResolution
}

func (resolver dbtProofManagedDataResolver) ResolveManagedDataForIdentity(_ context.Context, identity projectgraph.ServingIdentity) (runtimehost.ManagedDataResolution, error) {
	if err := identity.Validate(); err != nil {
		return runtimehost.ManagedDataResolution{}, err
	}
	return resolver.resolution, nil
}

func prepareDBTProofServingArtifact(artifacts release.CandidateArtifactSet) (release.CandidateArtifactSet, []byte, error) {
	var packed bytes.Buffer
	manifest, servingDigest, err := projectbundle.PackCompiledProject(artifacts.Compiler.Artifact, artifacts.Compiler.Plan, &packed)
	if err != nil {
		return release.CandidateArtifactSet{}, nil, fmt.Errorf("pack production-shaped serving artifact: %w", err)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return release.CandidateArtifactSet{}, nil, fmt.Errorf("encode serving artifact manifest: %w", err)
	}
	artifacts.Artifact.ContentDigest = servingDigest
	artifacts.Generation.ArtifactDigest = servingDigest
	artifacts.Generation.ServingArtifactID = "artifact-" + strings.TrimPrefix(servingDigest, "sha256:")
	artifacts.Generation.BundleManifestJSON = string(manifestJSON)
	return artifacts, packed.Bytes(), nil
}

func validateDBTProofConnectionRequirements(artifacts release.CandidateArtifactSet) error {
	activations, err := artifacts.Compiler.Artifact.ConnectionActivations()
	if err != nil {
		return fmt.Errorf("compiled connection requirements: %w", err)
	}
	if len(activations) == 0 {
		return fmt.Errorf("compiled connection requirements are empty")
	}
	pins := make(map[string]string, len(artifacts.Generation.ManagedDataPins))
	for _, pin := range artifacts.Generation.ManagedDataPins {
		if pin.ConnectionID == "" || pin.RevisionID == "" {
			return fmt.Errorf("managed connection requirement is incomplete")
		}
		if _, duplicate := pins[pin.ConnectionID]; duplicate {
			return fmt.Errorf("duplicate managed connection requirement %q", pin.ConnectionID)
		}
		pins[pin.ConnectionID] = pin.RevisionID
	}
	requirements := make(map[string]release.CandidateConnectionRequirement, len(artifacts.Generation.Connections))
	for _, requirement := range artifacts.Generation.Connections {
		requirements[requirement.ConnectionID.String()] = requirement
	}
	authored := make(map[string]release.CandidateAuthoredConnection, len(artifacts.Generation.AuthoredConnections))
	for _, requirement := range artifacts.Generation.AuthoredConnections {
		authored[requirement.ConnectionID.String()] = requirement
	}
	for _, activation := range activations {
		switch activation.Mode {
		case projectartifact.ManagedActivation:
			if pins[activation.LogicalConnectionID] == "" {
				return fmt.Errorf("managed connection requirement %q is missing", activation.LogicalConnectionID)
			}
			delete(pins, activation.LogicalConnectionID)
		case projectartifact.TargetBindingActivation:
			requirement, ok := requirements[activation.LogicalConnectionID]
			if !ok || requirement.ConnectorKind != activation.ConnectorKind || requirement.Access != activation.Access {
				return fmt.Errorf("target connection requirement %q is missing or incompatible", activation.LogicalConnectionID)
			}
			delete(requirements, activation.LogicalConnectionID)
		case projectartifact.AuthoredActivation:
			requirement, ok := authored[activation.LogicalConnectionID]
			if !ok || requirement.ConnectorKind != activation.ConnectorKind || requirement.Access != activation.Access {
				return fmt.Errorf("authored connection requirement %q is missing or incompatible", activation.LogicalConnectionID)
			}
			delete(authored, activation.LogicalConnectionID)
		default:
			return fmt.Errorf("connection %q has unsupported activation mode %q", activation.LogicalConnectionID, activation.Mode)
		}
	}
	if len(pins) != 0 || len(requirements) != 0 || len(authored) != 0 {
		return fmt.Errorf("candidate contains connection requirements outside the compiled graph")
	}
	return nil
}

func dbtProofAuthorizationFingerprint(projectID projectgraph.ResourceID, environment string, graph projectgraph.ProjectGraph) (string, error) {
	identity, err := projectgraph.NewServingIdentity(projectID, environment, "candidate-policy")
	if err != nil {
		return "", err
	}
	snapshot, err := projectmanifest.CompileAuthorizationSnapshot(identity, graph, projectmanifest.AccessPolicy{})
	if err != nil {
		return "", err
	}
	return snapshot.Digest()
}

func validateDBTProofPortableBytes(portable []byte, forbidden ...string) error {
	for _, value := range forbidden {
		if value != "" && bytes.Contains(portable, []byte(value)) {
			return fmt.Errorf("target or producer provenance leaked into portable artifact: %q", value)
		}
	}
	return nil
}

func compileDBTProofConsumer(t *testing.T) (projectartifact.SourceBundle, projectcompiler.BundlePlan) {
	t.Helper()
	example := filepath.Join("..", "..", "examples", "dbt-warehouse-boundary")
	consumer := t.TempDir()
	if err := os.CopyFS(consumer, os.DirFS(filepath.Join(example, "leapview"))); err != nil {
		t.Fatal(err)
	}
	for _, relative := range dbtProofConsumerOverlays() {
		content, err := os.ReadFile(filepath.Join(example, "multi-source", "consumer-overlay", relative))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(consumer, relative), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	artifact, err := projectcompiler.Compile(consumer)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := projectcompiler.PlanSourceRoot(consumer)
	if err != nil {
		t.Fatal(err)
	}
	return artifact, plan
}

func dbtProofConsumerOverlays() []string {
	return []string{
		"connections/directory.yaml",
		"sources/dim_customers.yaml",
	}
}

func TestDBTProofDeliveryPlanRequiresProductionEvidence(t *testing.T) {
	artifact, compilerPlan := compileDBTProofConsumer(t)
	projectID := projectgraph.ResourceID("project:fai678-delivery-proof")
	identity := projectgraph.ServingIdentity{ProjectID: projectID, Environment: "dev", GenerationID: "00000000-0000-4000-8000-000000000001"}
	pins := []release.ManagedDataPin{
		{ConnectionID: "connection:directory", RevisionID: "sha256:" + strings.Repeat("a", 64)},
		{ConnectionID: "connection:warehouse", RevisionID: "sha256:" + strings.Repeat("b", 64)},
	}
	dataRevision, err := release.CandidateSourcesDataRevision(artifact.Digest(), pins)
	if err != nil {
		t.Fatal(err)
	}
	authorizationFingerprint, err := dbtProofAuthorizationFingerprint(projectID, identity.Environment, artifact.Graph())
	if err != nil {
		t.Fatal(err)
	}
	artifacts := release.CandidateArtifactSet{
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: artifact.Digest(), ProjectDigest: artifact.Digest(), CompilerVersion: projectartifact.CompilerVersion, SchemaVersion: projectartifact.Version},
		AuthorizationFingerprint: authorizationFingerprint,
		Generation:               release.CandidateGenerationArtifact{Identity: identity, DataMode: release.GenerationDataRefreshSources, DataRevision: dataRevision, ManagedDataPins: pins, Deterministic: true},
		Compiler:                 release.CandidateCompilerEvidence{Graph: artifact.Graph(), Manifest: artifact.Manifest(), Plan: compilerPlan, Artifact: artifact},
	}
	input := deployment.DeliveryCandidateBuildInput{
		ProjectID: projectID, OwnerID: "publisher", ArtifactDigest: artifact.Digest(),
		Candidate: deployment.Candidate{ID: "candidate-dev", TargetID: "lvinst_0123456789abcdefghijklmnopqrstuv", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "dev"}},
	}
	roots := map[string]string{"warehouse": filepath.Join(t.TempDir(), "commerce"), "directory": filepath.Join(t.TempDir(), "directory")}
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	valid, err := newDBTProofBoundDelivery(input, artifacts, "runtime:v1", now, roots, dbtProofAccessPolicy(t), dbtProofTarget)
	if err != nil {
		t.Fatalf("valid production-shaped delivery plan: %v", err)
	}
	if err := valid.Plan.Validate(); err != nil {
		t.Fatalf("persistable delivery plan failed validation: %v", err)
	}
	if valid.Plan.Governance.PolicyDigest != authorizationFingerprint || valid.Plan.Governance.AuthorizationDigest != authorizationFingerprint {
		t.Fatal("validated delivery plan lost authorization identity")
	}
	if !strings.Contains(valid.Plan.Provenance.BuildDefinition, dbtProofTarget) {
		t.Fatal("dbt target provenance is absent from non-portable delivery metadata")
	}
	if err := validateDBTProofPortableBytes(artifact.Canonical(), dbtProofTarget); err != nil {
		t.Fatal(err)
	}
	embedded := bytes.Replace(artifact.Canonical(), []byte(`{"version":3,`), []byte(`{"producerTarget":"`+dbtProofTarget+`","version":3,`), 1)
	if bytes.Equal(embedded, artifact.Canonical()) {
		t.Fatal("portable artifact wire shape changed")
	}
	if _, err := projectartifact.Decode(embedded); err == nil {
		t.Fatal("portable artifact with embedded dbt target provenance was accepted")
	}

	t.Run("missing authorization fingerprint", func(t *testing.T) {
		missing := artifacts
		missing.AuthorizationFingerprint = ""
		if _, err := newDBTProofBoundDelivery(input, missing, "runtime:v1", now, roots, dbtProofAccessPolicy(t), dbtProofTarget); err == nil {
			t.Fatal("delivery without AuthorizationFingerprint was accepted")
		}
	})
	t.Run("missing connection requirements", func(t *testing.T) {
		missing := artifacts
		missing.Generation.ManagedDataPins = nil
		if _, err := newDBTProofBoundDelivery(input, missing, "runtime:v1", now, roots, dbtProofAccessPolicy(t), dbtProofTarget); err == nil || !strings.Contains(err.Error(), "connection requirement") {
			t.Fatalf("delivery without connection requirements: %v", err)
		}
	})
	t.Run("invalid governance binding", func(t *testing.T) {
		request, err := appruntimefactory.CandidatePlanRequest(input, artifacts, "runtime:v1", now)
		if err != nil {
			t.Fatal(err)
		}
		request.Governance.AuthorizationDigest = "sha256:" + strings.Repeat("c", 64)
		if _, err := bindDBTProofDeliveryRequest(input, artifacts, request, nil, roots, dbtProofAccessPolicy(t), dbtProofTarget); err == nil || !strings.Contains(err.Error(), "not bound") {
			t.Fatalf("invalid governance binding: %v", err)
		}
	})
}
