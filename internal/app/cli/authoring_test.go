package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
	"github.com/stretchr/testify/require"
)

func TestProjectIdentityUsesCanonicalGraphID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leapview.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: leapview.dev/v1
kind: Project
metadata:
  id: project:canonical
  name: executable-name
spec:
  connections: {include: []}
  sources: {include: []}
  models: {include: []}
  semanticModels: {include: []}
  pipelines: {include: []}
  dashboards: {include: []}
  access: {include: []}
  publications: {include: []}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadProjectID(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "project:canonical" {
		t.Fatalf("project identity = %q, want graph ID project:canonical", got)
	}
	identity, err := (applicationProjectIdentity{}).ProjectID(path)
	if err != nil {
		t.Fatal(err)
	}
	if identity != got {
		t.Fatalf("authoring identity = %q, want %q", identity, got)
	}
}

func TestGeneratedCandidateSynchronizationTransportMapsTypedProtocol(t *testing.T) {
	generic := &candidateSyncTransportStub{}
	transport := newCandidateSynchronizationTransport(deploymentgen.NewGenClient(generic))
	request := projectdevloop.SynchronizationPlanRequest{
		ProjectID: "finance", ProjectFile: "leapview.yaml",
		ArtifactDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExpectedCandidateID:    "cand_1",
		ExpectedArtifactDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CandidateKey:           "github:pull/42",
		Artifacts: []projectdevloop.ArtifactReference{{
			Path: "leapview.yaml", Digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", SizeBytes: 6,
		}},
		SourceRevision: &projectdevloop.SourceRevision{
			Revision: "commit-a", Repository: "https://code.example/acme/analytics",
			Ref: "refs/pull/42/head", ChangeID: "pull/42",
		},
	}

	plan, err := transport.Plan(t.Context(), request)
	if err != nil || len(plan.MissingDigests) != 1 ||
		plan.MissingDigests[0] != request.Artifacts[0].Digest {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
	request.PlanID = plan.PlanID
	if err := transport.Upload(t.Context(), request, projectdevloop.Artifact{
		Path: request.Artifacts[0].Path, Digest: request.Artifacts[0].Digest, Content: []byte("source"),
	}); err != nil {
		t.Fatal(err)
	}
	candidate, err := transport.Commit(t.Context(), request)
	require.NoError(t, err)
	if candidate.ID != "cand_1" || candidate.ProjectID != "finance" ||
		candidate.ArtifactDigest != request.ArtifactDigest ||
		candidate.TargetID != "target_1" ||
		candidate.Environment != "development" ||
		candidate.ProvenanceDigest != "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" ||
		candidate.Revision != 7 {
		t.Fatalf("candidate = %#v", candidate)
	}
	if len(generic.requests) != 3 ||
		generic.requests[0].Headers.Get("Idempotency-Key") == "" ||
		generic.requests[0].Body.(deploymentgen.CandidateSynchronizationRequest).Artifacts[0].SizeBytes != 6 ||
		generic.requests[1].Headers.Get("Content-Digest") != standardCandidateContentDigest(request.Artifacts[0].Digest) ||
		generic.requests[1].Headers.Get("Source-Synchronization-Plan") != request.PlanID ||
		generic.requests[2].Headers.Get("Idempotency-Key") == "" ||
		generic.requests[2].Headers.Get("Source-Synchronization-Plan") != request.PlanID ||
		string(generic.requests[1].Body.([]byte)) != "source" {
		t.Fatalf("generated requests = %#v", generic.requests)
	}
	body := generic.requests[2].Body.(deploymentgen.CandidateSynchronizationRequest)
	if body.SourceRevision == nil ||
		body.SourceRevision.Revision != request.SourceRevision.Revision ||
		body.SourceRevision.Repository == nil ||
		*body.SourceRevision.Repository != request.SourceRevision.Repository {
		t.Fatalf("source revision request = %#v", body.SourceRevision)
	}
	if body.CandidateKey == nil || *body.CandidateKey != request.CandidateKey {
		t.Fatalf("candidate key request = %#v", body.CandidateKey)
	}
}

func TestCandidateSynchronizationIdempotencyKeysBindExpectedPredecessor(t *testing.T) {
	generic := &candidateSyncTransportStub{}
	transport := newCandidateSynchronizationTransport(
		deploymentgen.NewGenClient(generic),
	)
	request := projectdevloop.SynchronizationPlanRequest{
		ProjectID: "finance", ProjectFile: "leapview.yaml",
		ArtifactDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExpectedCandidateID:    "cand_1",
		ExpectedArtifactDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CandidateKey:           "github:pull/42",
		Artifacts: []projectdevloop.ArtifactReference{{
			Path:   "leapview.yaml",
			Digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", SizeBytes: 6,
		}},
	}

	plan, err := transport.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.PlanID = plan.PlanID
	if _, err := transport.Commit(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Plan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Commit(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.ExpectedArtifactDigest =
		"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if _, err := transport.Plan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Commit(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	if len(generic.requests) != 6 {
		t.Fatalf("generated requests = %d, want 6", len(generic.requests))
	}
	firstPlanKey := generic.requests[0].Headers.Get("Idempotency-Key")
	firstCommitKey := generic.requests[1].Headers.Get("Idempotency-Key")
	replayedPlanKey := generic.requests[2].Headers.Get("Idempotency-Key")
	replayedCommitKey := generic.requests[3].Headers.Get("Idempotency-Key")
	secondPlanKey := generic.requests[4].Headers.Get("Idempotency-Key")
	secondCommitKey := generic.requests[5].Headers.Get("Idempotency-Key")
	if firstPlanKey != replayedPlanKey {
		t.Fatalf("identical plan did not retain idempotency key: %q != %q", firstPlanKey, replayedPlanKey)
	}
	if firstCommitKey != replayedCommitKey {
		t.Fatalf("identical commit did not retain idempotency key: %q != %q", firstCommitKey, replayedCommitKey)
	}
	if firstPlanKey == secondPlanKey {
		t.Fatalf("plan idempotency key did not bind expected predecessor: %q", firstPlanKey)
	}
	if firstCommitKey == secondCommitKey {
		t.Fatalf("commit idempotency key did not bind expected predecessor: %q", firstCommitKey)
	}
}

func TestNativeSynchronizationProjectsCanonicalDeliveryCandidate(t *testing.T) {
	const (
		projectID   = "finance"
		artifact    = "sha256:41cf6794ba4200b839c53531555f0f3998df4cbb01a4d5cb0b94e3ca5e23947d"
		attestation = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		planID      = "0198f2c0-7c7a-7f00-8a11-000000000101"
		buildID     = "0198f2c0-7c7a-7f00-8a11-000000000102"
		candidateID = "0198f2c0-7c7a-7f00-8a11-000000000103"
		sealID      = "0198f2c0-7c7a-7f00-8a11-000000000104"
		planDigest  = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		provenance  = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		execution   = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		evidence    = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	)
	source := nativeCandidateSetDigestForTest(projectID, "leapview.yaml", artifact, 6)
	generic := &nativeDeliveryTransportStub{sourceDigest: source}
	transport := newCandidateSynchronizationTransport(deploymentgen.NewGenClient(generic))
	transport.principalClient = accessgen.NewGenClient(generic)
	transport.canonicalOrigin = "https://target.example"
	candidate, err := transport.SynchronizeNative(t.Context(), projectdevloop.SyncRequest{
		Snapshot: projectdevloop.Snapshot{
			ProjectID: projectID, ProjectFile: "leapview.yaml", Digest: source,
			CandidateKey: "native", Artifacts: []projectdevloop.Artifact{{
				Path: "leapview.yaml", Digest: artifact, SizeBytes: 6, Content: []byte("source"),
			}},
		},
	}, 2)
	require.NoError(t, err)
	if candidate.ID != candidateID || candidate.ProjectID != projectID ||
		candidate.OwnerID != "principal_native" || candidate.ArtifactDigest != source ||
		candidate.TargetID != "target_native" || candidate.Environment != "production" ||
		candidate.ProvenanceDigest != provenance || candidate.Revision != 17 ||
		candidate.PreviewURL != "https://target.example/candidates/"+candidateID ||
		candidate.PlanID != planID || candidate.PlanDigest != planDigest ||
		candidate.ExecutionDigest != "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" ||
		candidate.EvidenceDigest != "sha256:9999999999999999999999999999999999999999999999999999999999999999" {
		t.Fatalf("candidate = %#v", candidate)
	}
	for _, request := range generic.requests {
		if request.OperationID == deploymentgen.GenOperationCommitProjectCandidateSynchronization {
			t.Fatal("native synchronization invoked legacy candidate commit")
		}
	}
	if !generic.sawNativePlan || !generic.sawNativeBuild || !generic.sawSourceRetention {
		t.Fatalf("native requests were incomplete: %#v", generic.requests)
	}
	if generic.nativePlanKey == "" || generic.nativeBuildKey == "" {
		t.Fatal("native requests did not carry idempotency keys")
	}
	if generic.retainedSource != attestation {
		t.Fatalf("retained source attestation = %q, want %q", generic.retainedSource, attestation)
	}
}

func TestNativeDeliveryKeysAreStableAcrossTransportInstances(t *testing.T) {
	request, source := nativeDeliverySyncRequestForTest()
	firstStub := &nativeDeliveryTransportStub{sourceDigest: source}
	first := newCandidateSynchronizationTransport(deploymentgen.NewGenClient(firstStub))
	first.principalClient = accessgen.NewGenClient(firstStub)
	first.canonicalOrigin = "https://target.example"
	if _, err := first.SynchronizeNative(t.Context(), request, 2); err != nil {
		t.Fatal(err)
	}
	secondStub := &nativeDeliveryTransportStub{sourceDigest: source}
	second := newCandidateSynchronizationTransport(deploymentgen.NewGenClient(secondStub))
	second.principalClient = accessgen.NewGenClient(secondStub)
	second.canonicalOrigin = "https://target.example"
	if _, err := second.SynchronizeNative(t.Context(), request, 2); err != nil {
		t.Fatal(err)
	}
	if firstStub.nativePlanKey == "" || firstStub.nativeBuildKey == "" ||
		firstStub.nativePlanKey != secondStub.nativePlanKey ||
		firstStub.nativeBuildKey != secondStub.nativeBuildKey {
		t.Fatalf("native idempotency keys differ: first plan=%q build=%q second plan=%q build=%q", firstStub.nativePlanKey, firstStub.nativeBuildKey, secondStub.nativePlanKey, secondStub.nativeBuildKey)
	}
}

func TestRetainSourceRejectsMismatchedProjectIdentity(t *testing.T) {
	_, source := nativeDeliverySyncRequestForTest()
	stub := &nativeDeliveryTransportStub{sourceDigest: source, retainedProjectID: "other-project"}
	transport := newCandidateSynchronizationTransport(deploymentgen.NewGenClient(stub))
	request := projectdevloop.SynchronizationPlanRequest{
		ProjectID: "finance", ArtifactDigest: source, ProjectFile: "leapview.yaml",
		CandidateKey: "native", PlanID: "source-plan",
		Artifacts: []projectdevloop.ArtifactReference{{Path: "leapview.yaml", Digest: "sha256:41cf6794ba4200b839c53531555f0f3998df4cbb01a4d5cb0b94e3ca5e23947d", SizeBytes: 6}},
	}
	if _, err := transport.RetainSource(t.Context(), request); err == nil {
		t.Fatal("retained source accepted a mismatched project identity")
	}
}

func TestDevCommandExposesOneAuthenticatedRemoteWorkflow(t *testing.T) {
	command := devCommand(context.Background())
	if command.Name() != "dev" || !strings.Contains(strings.ToLower(command.Short), "private") {
		t.Fatalf("dev command = %q %q", command.Name(), command.Short)
	}
	for _, flag := range []string{
		"project", "target", "token", "upload-concurrency", "once",
		"candidate-key", "source-revision", "source-repository", "source-ref", "source-change",
	} {
		if command.Flags().Lookup(flag) == nil {
			t.Errorf("dev command is missing --%s", flag)
		}
	}
	for _, forbidden := range []string{"local-server", "production", "workspace"} {
		if command.Flags().Lookup(forbidden) != nil {
			t.Errorf("dev command exposes alternate workflow flag --%s", forbidden)
		}
	}
}

type candidateSyncTransportStub struct {
	requests []apigenclient.Request
}

type nativeDeliveryTransportStub struct {
	requests           []apigenclient.Request
	sourceDigest       string
	retainedProjectID  string
	nativePlanKey      string
	nativeBuildKey     string
	retainedSource     string
	sawNativePlan      bool
	sawNativeBuild     bool
	sawSourceRetention bool
}

func (stub *nativeDeliveryTransportStub) DoAPIGen(
	_ context.Context,
	request apigenclient.Request,
	out any,
) (apigenclient.Response, error) {
	stub.requests = append(stub.requests, request)
	status := http.StatusOK
	var response any
	const (
		attestation = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		planID      = "0198f2c0-7c7a-7f00-8a11-000000000101"
		buildID     = "0198f2c0-7c7a-7f00-8a11-000000000102"
		candidateID = "0198f2c0-7c7a-7f00-8a11-000000000103"
		sealID      = "0198f2c0-7c7a-7f00-8a11-000000000104"
		planDigest  = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		provenance  = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		execution   = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		evidence    = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	)
	source := stub.sourceDigest
	retainedProjectID := stub.retainedProjectID
	if retainedProjectID == "" {
		retainedProjectID = "finance"
	}
	switch request.OperationID {
	case deploymentgen.GenOperationPlanProjectCandidateSynchronization:
		body := request.Body.(deploymentgen.CandidateSynchronizationRequest)
		response = deploymentgen.CandidateSynchronizationPlanResponse{PlanId: "source-plan", ArtifactDigest: body.ArtifactDigest, MissingDigests: []string{body.Artifacts[0].Digest}}
	case deploymentgen.GenOperationUploadProjectCandidateSourceBlob:
		status = http.StatusCreated
		response = deploymentgen.CandidateSourceBlobResponse{Digest: request.PathParams["digest"], SizeBytes: int64(len(request.Body.([]byte)))}
	case deploymentgen.GenOperationRetainProjectCandidateSource:
		stub.sawSourceRetention = true
		response = deploymentgen.CandidateSourceSnapshotResponse{ProjectId: retainedProjectID, SourceDigest: source, ProjectDigest: source, SourceAttestationDigest: attestation, TargetId: "target_native", Environment: "production"}
	case deploymentgen.GenOperationCreateDeliveryPlan:
		stub.sawNativePlan = true
		stub.nativePlanKey = request.Headers.Get("Idempotency-Key")
		response = deploymentgen.DeliveryPlanPreviewResponse{Id: planID, ProjectId: "finance", TargetId: "target_native", Environment: "production", Operation: deploymentgen.DeliveryOperationKindCodeChange, SourceDigest: source, SourceAttestationDigest: attestation, PlanDigest: planDigest, ExecutionDigest: execution, EvidenceDigest: evidence, ProvenanceDigest: provenance, Status: deploymentgen.DeliveryPlanStatusPlanned}
	case deploymentgen.GenOperationBuildDeliveryPlan:
		stub.sawNativeBuild = true
		stub.nativeBuildKey = request.Headers.Get("Idempotency-Key")
		candidateRevision := int64(17)
		response = deploymentgen.DeliveryBuildStatusResponse{Id: buildID, PlanId: planID, PlanDigest: planDigest, SourceDigest: source, ExecutionDigest: execution, Status: deploymentgen.DeliveryBuildStatusSealed, CandidateId: testPointer(candidateID), CandidateRevision: &candidateRevision, SealId: testPointer(sealID), Revision: 9}
	case accessgen.GenOperationGetCurrentPrincipal:
		response = accessgen.CurrentPrincipalResponse{Id: "principal_native"}
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return apigenclient.Response{}, err
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		return apigenclient.Response{}, err
	}
	if request.OperationID == deploymentgen.GenOperationRetainProjectCandidateSource {
		stub.retainedSource = response.(deploymentgen.CandidateSourceSnapshotResponse).SourceAttestationDigest
	}
	return apigenclient.Response{StatusCode: status, Headers: make(http.Header), ContentType: "application/json"}, nil
}

func (stub *candidateSyncTransportStub) DoAPIGen(
	_ context.Context,
	request apigenclient.Request,
	out any,
) (apigenclient.Response, error) {
	stub.requests = append(stub.requests, request)
	var response any
	status := http.StatusOK
	switch request.OperationID {
	case deploymentgen.GenOperationPlanProjectCandidateSynchronization:
		body := request.Body.(deploymentgen.CandidateSynchronizationRequest)
		response = deploymentgen.CandidateSynchronizationPlanResponse{
			PlanId: "plan-test", ArtifactDigest: body.ArtifactDigest,
			MissingDigests: []string{body.Artifacts[0].Digest},
		}
	case deploymentgen.GenOperationUploadProjectCandidateSourceBlob:
		status = http.StatusCreated
		response = deploymentgen.CandidateSourceBlobResponse{
			Digest: request.PathParams["digest"], SizeBytes: int64(len(request.Body.([]byte))),
		}
	case deploymentgen.GenOperationCommitProjectCandidateSynchronization:
		body := request.Body.(deploymentgen.CandidateSynchronizationRequest)
		response = deploymentgen.CandidateResponse{
			Id: "cand_1", ProjectId: "finance", ArtifactDigest: body.ArtifactDigest,
			PreviewUrl: "https://target.example/candidates/cand_1",
			TargetId:   "target_1", Environment: "development", Revision: 7,
			ProvenanceDigest: testPointer("sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"),
		}
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return apigenclient.Response{}, err
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		return apigenclient.Response{}, err
	}
	return apigenclient.Response{
		StatusCode: status, Headers: make(http.Header), ContentType: "application/json",
	}, nil
}

func testPointer[T any](value T) *T {
	return &value
}

func nativeCandidateSetDigestForTest(projectID, projectFile, artifactDigest string, size int64) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%d:%s:%d:%s:", len(projectID), projectID, len(projectFile), projectFile)
	path := "leapview.yaml"
	_, _ = fmt.Fprintf(hash, "%d:%s:%d:%s:%d:", len(path), path, len(artifactDigest), artifactDigest, size)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func nativeDeliverySyncRequestForTest() (projectdevloop.SyncRequest, string) {
	const artifact = "sha256:41cf6794ba4200b839c53531555f0f3998df4cbb01a4d5cb0b94e3ca5e23947d"
	source := nativeCandidateSetDigestForTest("finance", "leapview.yaml", artifact, 6)
	return projectdevloop.SyncRequest{Snapshot: projectdevloop.Snapshot{
		ProjectID: "finance", ProjectFile: "leapview.yaml", Digest: source,
		CandidateKey: "native", Artifacts: []projectdevloop.Artifact{{
			Path: "leapview.yaml", Digest: artifact, SizeBytes: 6, Content: []byte("source"),
		}},
	}}, source
}
