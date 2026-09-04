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

func TestSourceSynchronizationKeysAreStableAcrossTransportInstances(t *testing.T) {
	_, source := nativeDeliverySyncRequestForTest()
	base := projectdevloop.SynchronizationPlanRequest{
		ProjectID:      "finance",
		ProjectFile:    "leapview.yaml",
		ArtifactDigest: source,
		SourceOnly:     true,
		CandidateKey:   "native",
		Artifacts: []projectdevloop.ArtifactReference{{
			Path: "leapview.yaml", Digest: "sha256:41cf6794ba4200b839c53531555f0f3998df4cbb01a4d5cb0b94e3ca5e23947d", SizeBytes: 6,
		}},
		SourceRevision: &projectdevloop.SourceRevision{
			Revision: "rev-a", Repository: "https://code.example/finance", Ref: "refs/heads/main", ChangeID: "change-a",
		},
	}
	run := func(request projectdevloop.SynchronizationPlanRequest) (string, string) {
		t.Helper()
		stub := &nativeDeliveryTransportStub{sourceDigest: source}
		transport := newCandidateSynchronizationTransport(deploymentgen.NewGenClient(stub))
		plan, err := transport.Plan(t.Context(), request)
		require.NoError(t, err)
		request.PlanID = plan.PlanID
		if _, err := transport.RetainSource(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		var planKey, retainKey string
		for _, recorded := range stub.requests {
			switch recorded.OperationID {
			case deploymentgen.GenOperationPlanProjectCandidateSynchronization:
				planKey = recorded.Headers.Get("Idempotency-Key")
			case deploymentgen.GenOperationRetainProjectCandidateSource:
				retainKey = recorded.Headers.Get("Idempotency-Key")
			}
		}
		if planKey == "" || retainKey == "" {
			t.Fatalf("source synchronization keys were not recorded: plan=%q retain=%q", planKey, retainKey)
		}
		return planKey, retainKey
	}

	firstPlanKey, firstRetainKey := run(base)
	secondPlanKey, secondRetainKey := run(base)
	if firstPlanKey != secondPlanKey || firstRetainKey != secondRetainKey {
		t.Fatalf("source synchronization keys changed across transport instances: plan %q/%q retain %q/%q", firstPlanKey, secondPlanKey, firstRetainKey, secondRetainKey)
	}

	changedCandidate := base
	changedCandidate.CandidateKey = "other"
	changedPlanKey, changedRetainKey := run(changedCandidate)
	if changedPlanKey == firstPlanKey || changedRetainKey == firstRetainKey {
		t.Fatal("candidate key change reused source synchronization identity")
	}

	changedRequest := base
	changedRequest.ProjectFile = "other.yaml"
	changedPlanKey, changedRetainKey = run(changedRequest)
	if changedPlanKey == firstPlanKey || changedRetainKey == firstRetainKey {
		t.Fatal("source request change reused source synchronization identity")
	}

	changedRevision := base
	revision := *base.SourceRevision
	revision.Revision = "rev-b"
	changedRevision.SourceRevision = &revision
	changedPlanKey, changedRetainKey = run(changedRevision)
	if changedPlanKey == firstPlanKey || changedRetainKey == firstRetainKey {
		t.Fatal("source revision change reused source synchronization identity")
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
