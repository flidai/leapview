package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
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
