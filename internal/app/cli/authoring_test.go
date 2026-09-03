package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
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

func TestProjectDevTransportNegotiatesNativeAndLegacyModes(t *testing.T) {
	base := newCandidateSynchronizationTransport(deploymentgen.NewGenClient(&candidateSyncTransportStub{}))
	if _, ok := any(newProjectDevSynchronizationTransport(cliapi.DeliveryModeNativePostgres, base)).(projectdevloop.NativeSynchronizationTransport); !ok {
		t.Fatal("native delivery mode did not expose native synchronization transport")
	}
	legacy := newProjectDevSynchronizationTransport(cliapi.DeliveryModeLegacySQLite, base)
	if _, ok := any(legacy).(projectdevloop.NativeSynchronizationTransport); ok {
		t.Fatal("legacy delivery mode exposed native synchronization transport")
	}
	if _, ok := any(newProjectDevSynchronizationTransport("", base)).(projectdevloop.NativeSynchronizationTransport); ok {
		t.Fatal("unspecified delivery mode exposed native synchronization transport")
	}
}

func TestRunDevLegacySQLiteAcceptsOpaqueCandidateID(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "leapview.yaml")
	if err := os.WriteFile(projectPath, []byte(`apiVersion: leapview.dev/v1
kind: Project
metadata:
  id: project:sqlite-regression
  name: sqlite-regression
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
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.URL.Path == "/api/v1/instance":
			_, _ = fmt.Fprintf(w, `{"id":"lvinst_sqlite","canonicalOrigin":%q,"environment":"evaluation"}`, server.URL)
		case r.URL.Path == "/api/v1/capabilities":
			_, _ = fmt.Fprint(w, `{"apiVersion":"v1","buildVersion":"test","buildRevision":"test","buildTime":"2026-08-01T00:00:00Z","buildDirty":false,"buildDevelopment":true,"environment":"evaluation","deliveryMode":"legacy_sqlite","authentication":["bearer"],"queryFormats":["application/json"],"uploadProtocols":[],"visualization":{"schemaVersion":7,"renderers":[]}}`)
		case strings.HasSuffix(r.URL.Path, "/candidate-sync/plan"):
			var request deploymentgen.CandidateSynchronizationRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode plan request: %v", err)
			}
			missing := make([]string, 0, len(request.Artifacts))
			for _, artifact := range request.Artifacts {
				missing = append(missing, artifact.Digest)
			}
			_ = json.NewEncoder(w).Encode(deploymentgen.CandidateSynchronizationPlanResponse{PlanId: "sqlite-plan", ArtifactDigest: request.ArtifactDigest, MissingDigests: missing})
		case strings.Contains(r.URL.Path, "/candidate-sync/blobs/"):
			pathDigest := strings.TrimPrefix(r.URL.Path[strings.Index(r.URL.Path, "/candidate-sync/blobs/")+len("/candidate-sync/blobs/"):], "/")
			content, _ := io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(deploymentgen.CandidateSourceBlobResponse{Digest: pathDigest, SizeBytes: int64(len(content))})
		case strings.HasSuffix(r.URL.Path, "/candidate-sync/commit"):
			var request deploymentgen.CandidateSynchronizationRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode commit request: %v", err)
			}
			provenance := "sha256:" + strings.Repeat("b", 64)
			_ = json.NewEncoder(w).Encode(deploymentgen.CandidateResponse{
				ArtifactDigest: request.ArtifactDigest, BaseGeneration: "", CandidateKey: "default",
				CreatedAt: "2026-08-01T00:00:00Z", Environment: "evaluation", ExpiresAt: "2026-08-02T00:00:00Z",
				Id: "cand_sqlite_1", OwnerId: "principal_sqlite", PreviewUrl: server.URL + "/candidates/cand_sqlite_1",
				ProjectId: "project:sqlite-regression", ProvenanceDigest: &provenance, Revision: 1,
				Status: deploymentgen.CandidateStatusReady, TargetId: "target_sqlite", UpdatedAt: "2026-08-01T00:00:00Z",
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	var out, errOut bytes.Buffer
	err := projectcli.RunDev(
		t.Context(),
		capabilityAPIClient{httpClient: server.Client(), validateAuthoring: true},
		projectcli.NewCandidateCheckpointStore(filepath.Join(t.TempDir(), "authoring.json")),
		projectDevRemoteFactory{client: capabilityAPIClient{httpClient: server.Client(), validateAuthoring: true}},
		projectcli.DevOptions{
			ProjectPath: projectPath, Credentials: cliapi.Credentials{Target: server.URL, Token: "token"},
			UploadConcurrency: 1, Once: true, NoBrowser: true, CandidateKey: "default", Format: "json",
		},
		nil, &out, &errOut,
	)
	if err != nil {
		t.Fatalf("RunDev returned %v (stderr=%s)", err, errOut.String())
	}
	var result projectcli.DevResult
	if err := json.NewDecoder(&out).Decode(&result); err != nil {
		t.Fatalf("decode dev result: %v output=%s", err, out.String())
	}
	if result.CandidateID != "cand_sqlite_1" {
		t.Fatalf("candidate ID = %q, want opaque SQLite candidate", result.CandidateID)
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
	baselineRequest := request
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
	secondGeneric := &candidateSyncTransportStub{}
	secondTransport := newCandidateSynchronizationTransport(
		deploymentgen.NewGenClient(secondGeneric),
	)
	secondPlan, err := secondTransport.Plan(t.Context(), baselineRequest)
	if err != nil {
		t.Fatal(err)
	}
	baselineRequest.PlanID = secondPlan.PlanID
	if _, err := secondTransport.Commit(t.Context(), baselineRequest); err != nil {
		t.Fatal(err)
	}
	if got := secondGeneric.requests[1].Headers.Get("Idempotency-Key"); got != firstCommitKey {
		t.Fatalf("identical commit changed idempotency key across transport instances: %q != %q", got, firstCommitKey)
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
