package module

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/stretchr/testify/require"
)

func TestCandidateSynchronizationPlansUploadsAndCommitsOwnedCandidate(t *testing.T) {
	module := testCandidateModule(t, "principal_1")
	module.candidateAdmission = candidatePreparationAdmitterStub{}
	digest := "sha256:" + strings.Repeat("a", 64)
	blobDigest := "sha256:" + strings.Repeat("b", 64)
	sources := &candidateSourceSynchronizerStub{missing: []string{blobDigest}}
	module.candidateSources = sources
	artifacts := &candidateArtifactPreparerStub{result: release.CandidateArtifactSet{
		Artifact: release.ProjectArtifactProvenance{
			SourceDigest: digest, ProjectDigest: "sha256:" + strings.Repeat("d", 64), ContentDigest: digest,
			CompilerVersion: "compiler:test", SchemaVersion: 2,
		},
		AuthorizationFingerprint: "sha256:" + strings.Repeat("f", 64),
		Generation:               release.CandidateGenerationArtifact{Identity: testServingIdentity("state_sales"), ArtifactDigest: digest, DataRevision: "snapshot:1", DataMode: release.GenerationDataReuseSnapshot},
	}}
	module.candidateArtifacts = artifacts
	runtimes := &candidateRuntimePreparerStub{
		requireAdmission: true,
		receipt: deployment.CandidateRuntimeReceipt{
			RuntimeVersion: "runtime:test",
		},
	}
	module.candidateRuntimes = runtimes
	body := `{"projectFile":"leapview.yaml","artifactDigest":"` + digest +
		`","sourceRevision":{"revision":"commit-a","repository":"https://code.example/acme/analytics","ref":"refs/pull/42/head","changeId":"pull/42"}` +
		`,"artifacts":[{"path":"leapview.yaml","digest":"` + blobDigest + `"}]}`

	planned := callCandidateAPI(t, http.MethodPost, "/api/v1/projects/finance/candidate-sync/plan", body, func(w http.ResponseWriter, r *http.Request) {
		module.PlanProjectCandidateSynchronization(w, r, "finance")
	})
	if planned.Code != http.StatusOK || !strings.Contains(planned.Body.String(), blobDigest) {
		t.Fatalf("plan response = %d %s", planned.Code, planned.Body.String())
	}
	contentDigest := standardContentDigest(t, blobDigest)
	uploaded := callCandidateAPI(t, http.MethodPut, "/api/v1/projects/finance/candidate-sync/blobs/"+blobDigest, "blob", func(w http.ResponseWriter, r *http.Request) {
		module.UploadProjectCandidateSourceBlob(w, r, "finance", blobDigest, "application/octet-stream", contentDigest)
	})
	if uploaded.Code != http.StatusCreated || string(sources.uploaded) != "blob" {
		t.Fatalf("upload response = %d %s bytes=%q", uploaded.Code, uploaded.Body.String(), sources.uploaded)
	}
	committed := callCandidateAPI(t, http.MethodPost, "/api/v1/projects/finance/candidate-sync/commit", body, func(w http.ResponseWriter, r *http.Request) {
		module.CommitProjectCandidateSynchronization(w, r, "finance", "commit-1")
	})
	var candidate candidateAPIResponse
	decodeCandidateResponse(t, committed, &candidate)
	if committed.Code != http.StatusOK || candidate.ID == "" ||
		candidate.ArtifactDigest != digest || candidate.Status != string(deployment.CandidateReady) ||
		candidate.ProvenanceDigest == "" ||
		sources.commits != 1 || runtimes.calls != 1 || len(artifacts.retained) != 1 {
		t.Fatalf("commit response = %d candidate=%#v commits=%d", committed.Code, candidate, sources.commits)
	}
	if retained := artifacts.retained[0]; retained.Digest != candidate.ProvenanceDigest ||
		retained.Candidate.ID != candidate.ID ||
		retained.Candidate.Revision != candidate.Revision ||
		retained.SourceRevision == nil ||
		retained.SourceRevision.Revision != "commit-a" ||
		retained.Plan.RuntimeVersion != "runtime:test" {
		t.Fatalf("retained provenance = %#v, candidate = %#v", retained, candidate)
	}

	nextDigest := "sha256:" + strings.Repeat("c", 64)
	artifacts.result.Artifact.SourceDigest = nextDigest
	replacementBody := `{"projectFile":"leapview.yaml","artifactDigest":"` + nextDigest +
		`","sourceRevision":{"revision":"commit-b","repository":"https://code.example/acme/analytics","ref":"refs/pull/42/head","changeId":"pull/42"}` +
		`,"expectedCandidateId":"` + candidate.ID + `","expectedArtifactDigest":"` + digest +
		`","artifacts":[{"path":"leapview.yaml","digest":"` + blobDigest + `"}]}`
	replaced := callCandidateAPI(t, http.MethodPost, "/api/v1/projects/finance/candidate-sync/commit", replacementBody, func(w http.ResponseWriter, r *http.Request) {
		module.CommitProjectCandidateSynchronization(w, r, "finance", "commit-2")
	})
	var replacement candidateAPIResponse
	decodeCandidateResponse(t, replaced, &replacement)
	if replaced.Code != http.StatusOK || replacement.ID != candidate.ID ||
		replacement.ArtifactDigest != nextDigest ||
		replacement.Revision != candidate.Revision+2 ||
		replacement.ProvenanceDigest == candidate.ProvenanceDigest ||
		len(artifacts.retained) != 2 {
		t.Fatalf("replacement response = %d candidate=%#v", replaced.Code, replacement)
	}
	sameContentBody := `{"projectFile":"leapview.yaml","artifactDigest":"` + nextDigest +
		`","sourceRevision":{"revision":"commit-c","repository":"https://code.example/acme/analytics","ref":"refs/pull/42/head","changeId":"pull/42"}` +
		`,"expectedCandidateId":"` + replacement.ID + `","expectedArtifactDigest":"` + nextDigest +
		`","artifacts":[{"path":"leapview.yaml","digest":"` + blobDigest + `"}]}`
	advanced := callCandidateAPI(t, http.MethodPost, "/api/v1/projects/finance/candidate-sync/commit", sameContentBody, func(w http.ResponseWriter, r *http.Request) {
		module.CommitProjectCandidateSynchronization(w, r, "finance", "commit-3")
	})
	var sameContent candidateAPIResponse
	decodeCandidateResponse(t, advanced, &sameContent)
	if advanced.Code != http.StatusOK ||
		sameContent.ArtifactDigest != replacement.ArtifactDigest ||
		sameContent.Revision != replacement.Revision+2 ||
		sameContent.ProvenanceDigest == replacement.ProvenanceDigest ||
		len(artifacts.retained) != 3 ||
		artifacts.retained[2].SourceRevision == nil ||
		artifacts.retained[2].SourceRevision.Revision != "commit-c" {
		t.Fatalf("same-content source advance = %d candidate=%#v", advanced.Code, sameContent)
	}
	replayBody := `{"projectFile":"leapview.yaml","artifactDigest":"` + nextDigest +
		`","sourceRevision":{"revision":"commit-c","repository":"https://code.example/acme/analytics","ref":"refs/pull/42/head","changeId":"pull/42"}` +
		`,"artifacts":[{"path":"leapview.yaml","digest":"` + blobDigest + `"}]}`
	replayed := callCandidateAPI(t, http.MethodPost, "/api/v1/projects/finance/candidate-sync/commit", replayBody, func(w http.ResponseWriter, r *http.Request) {
		module.CommitProjectCandidateSynchronization(w, r, "finance", "commit-4")
	})
	var replay candidateAPIResponse
	decodeCandidateResponse(t, replayed, &replay)
	if replayed.Code != http.StatusOK ||
		replay.Revision != sameContent.Revision ||
		replay.ProvenanceDigest != sameContent.ProvenanceDigest ||
		runtimes.calls != 3 || len(artifacts.retained) != 3 {
		t.Fatalf("idempotent source replay = %d candidate=%#v runtimes=%d", replayed.Code, replay, runtimes.calls)
	}
}

func TestCandidateSourceBlobUploadPersistsGeneratedCommandAuditExactlyOnce(t *testing.T) {
	module := testCandidateModule(t, "principal_1")
	blobDigest := "sha256:" + strings.Repeat("b", 64)
	sources := &candidateSourceSynchronizerStub{}
	module.candidateSources = sources
	var events []CandidateSourceBlobAuditEvent
	module.candidateSourceBlobAudit = func(_ context.Context, event CandidateSourceBlobAuditEvent) error {
		events = append(events, event)
		return nil
	}
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/projects/finance/candidate-sync/blobs/"+blobDigest,
		strings.NewReader("blob"),
	)
	request.Header.Set("X-Request-ID", "req-source-blob")
	request.Header.Set("X-Correlation-ID", "corr-source-blob")
	request.Header.Set("X-LeapView-Invocation-Surface", "cli")
	response := httptest.NewRecorder()

	module.UploadProjectCandidateSourceBlob(
		response,
		request,
		"finance",
		blobDigest,
		"application/octet-stream",
		standardContentDigest(t, blobDigest),
	)

	if response.Code != http.StatusCreated || string(sources.uploaded) != "blob" {
		t.Fatalf("upload response = %d %s bytes=%q", response.Code, response.Body.String(), sources.uploaded)
	}
	if len(events) != 1 {
		t.Fatalf("candidate source blob audits = %d, want 1", len(events))
	}
	event := events[0]
	if event.PrincipalID != "principal_1" || event.ProjectID != "finance" ||
		event.Digest != blobDigest || event.Action != "candidate.source_blob_uploaded" ||
		event.Privilege != "AUTHOR_PROJECT" || event.Status != "success" ||
		event.RequestID != "req-source-blob" || event.CorrelationID != "corr-source-blob" {
		t.Fatalf("candidate source blob audit = %#v", event)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.MetadataJSON), &metadata); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	if metadata["schemaVersion"] != float64(1) || metadata["retention"] != "security" ||
		metadata["payloadSchema"] != "CandidateSourceBlobAuditPayload" {
		t.Fatalf("candidate source blob audit metadata = %#v", metadata)
	}
	payload, ok := metadata["payload"].(map[string]any)
	if !ok || payload["operationId"] != "uploadProjectCandidateSourceBlob" ||
		payload["surface"] != "cli" || payload["digest"] != blobDigest ||
		payload["sizeBytes"] != float64(len("blob")) {
		t.Fatalf("candidate source blob audit payload = %#v", payload)
	}
}

func TestCandidateSourceBlobUploadPreservesSuccessWhenBestEffortAuditFails(t *testing.T) {
	module := testCandidateModule(t, "principal_1")
	var auditLog bytes.Buffer
	module.logger = slog.New(slog.NewTextHandler(&auditLog, nil))
	blobDigest := "sha256:" + strings.Repeat("b", 64)
	sources := &candidateSourceSynchronizerStub{}
	module.candidateSources = sources
	auditCalls := 0
	module.candidateSourceBlobAudit = func(context.Context, CandidateSourceBlobAuditEvent) error {
		auditCalls++
		return errors.New("audit store unavailable")
	}

	response := callCandidateAPI(
		t,
		http.MethodPut,
		"/api/v1/projects/finance/candidate-sync/blobs/"+blobDigest,
		"blob",
		func(w http.ResponseWriter, r *http.Request) {
			module.UploadProjectCandidateSourceBlob(
				w,
				r,
				"finance",
				blobDigest,
				"application/octet-stream",
				standardContentDigest(t, blobDigest),
			)
		},
	)

	if response.Code != http.StatusCreated {
		t.Fatalf("upload response = %d %s", response.Code, response.Body.String())
	}
	if auditCalls != 1 {
		t.Fatalf("candidate source blob audit calls = %d, want 1", auditCalls)
	}
	if string(sources.uploaded) != "blob" {
		t.Fatalf("immutable blob write was unexpectedly rolled back: %q", sources.uploaded)
	}
	if response.Header().Get("Location") == "" {
		t.Fatal("successful command did not return a location")
	}
	if !strings.Contains(auditLog.String(), "candidate source blob audit failed") ||
		!strings.Contains(auditLog.String(), "uploadProjectCandidateSourceBlob") ||
		!strings.Contains(auditLog.String(), "audit store unavailable") {
		t.Fatalf("audit failure log = %q", auditLog.String())
	}
}

func TestCandidateSynchronizationNeverMarksReadyBeforeProvenanceIsRetained(t *testing.T) {
	module := testCandidateModule(t, "principal_1")
	digest := "sha256:" + strings.Repeat("a", 64)
	module.candidateSources = &candidateSourceSynchronizerStub{}
	module.candidateArtifacts = &candidateArtifactPreparerStub{
		result:    candidateArtifactSetForTest(digest, "state_sales", release.GenerationDataReuseSnapshot),
		retainErr: release.ErrConflict,
	}
	module.candidateRuntimes = &candidateRuntimePreparerStub{
		receipt: deployment.CandidateRuntimeReceipt{
			RuntimeVersion: "runtime:test",
		},
	}
	body := `{"projectFile":"leapview.yaml","artifactDigest":"` + digest +
		`","artifacts":[{"path":"leapview.yaml","digest":"` + digest + `"}]}`
	response := callCandidateAPI(
		t,
		http.MethodPost,
		"/api/v1/projects/finance/candidate-sync/commit",
		body,
		func(w http.ResponseWriter, r *http.Request) {
			module.CommitProjectCandidateSynchronization(w, r, "finance", "commit-1")
		},
	)
	if response.Code == http.StatusOK {
		t.Fatalf("provenance failure unexpectedly succeeded: %s", response.Body.String())
	}
	current, err := module.candidates.Get(
		t.Context(),
		deployment.CandidateAccessScope{
			ProjectID: "finance", CandidateID: "cand_opaque_1",
			OwnerID: "principal_1",
		},
	)
	require.NoError(t, err)
	if current.Status == deployment.CandidateReady ||
		current.ProvenanceDigest != "" {
		t.Fatalf("candidate became ready without provenance: %#v", current)
	}
}

func TestCandidateSynchronizationRejectsReadyCandidateWithInvalidProvenance(t *testing.T) {
	module := testCandidateModule(t, "principal_1")
	digest := "sha256:" + strings.Repeat("a", 64)
	module.candidateSources = &candidateSourceSynchronizerStub{}
	artifacts := &candidateArtifactPreparerStub{
		result:    candidateArtifactSetForTest(digest, "state_sales", release.GenerationDataReuseSnapshot),
		lookupErr: release.ErrProvenanceInvalid,
	}
	module.candidateArtifacts = artifacts
	runtimes := &candidateRuntimePreparerStub{receipt: deployment.CandidateRuntimeReceipt{
		RuntimeVersion: "runtime:test",
	}}
	module.candidateRuntimes = runtimes
	started, err := module.candidates.Start(t.Context(), deployment.StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1", ArtifactDigest: digest,
	})
	require.NoError(t, err)
	ready, err := module.candidates.MarkReady(t.Context(), deployment.CandidateAccessScope{
		ProjectID: "finance", CandidateID: started.Candidate.ID, OwnerID: "principal_1",
	}, digest, "sha256:"+strings.Repeat("f", 64))
	require.NoError(t, err)
	body := `{"projectFile":"leapview.yaml","artifactDigest":"` + digest + `","artifacts":[]}`

	response := callCandidateAPI(
		t,
		http.MethodPost,
		"/api/v1/projects/finance/candidate-sync/commit",
		body,
		func(w http.ResponseWriter, r *http.Request) {
			module.CommitProjectCandidateSynchronization(w, r, "finance", "rebuild-legacy")
		},
	)
	require.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "INVALID_CANDIDATE")
	require.NotContains(t, response.Body.String(), "reset target state")
	current, err := module.candidates.Get(t.Context(), candidateScope(ready))
	require.NoError(t, err)
	require.Equal(t, ready, current)
	require.Zero(t, runtimes.calls)
	require.Empty(t, artifacts.retained)
}

func TestCandidateReleaseProvenanceRejectsMismatchedSourceIdentity(t *testing.T) {
	module := testCandidateModule(t, "principal_1")
	digest := "sha256:" + strings.Repeat("a", 64)
	started, err := module.candidates.Start(
		t.Context(),
		deployment.StartCandidateRequest{
			ProjectID: "finance", OwnerID: "principal_1",
			ArtifactDigest: digest,
		},
	)
	require.NoError(t, err)
	_, err = candidateReleaseProvenance(
		started.Candidate,
		candidateArtifactSetForTest("sha256:"+strings.Repeat("b", 64), "state_sales", release.GenerationDataReuseSnapshot),
		deployment.CandidateRuntimeReceipt{RuntimeVersion: "runtime:test"},
		nil,
	)
	if !errors.Is(err, release.ErrProvenanceInvalid) {
		t.Fatalf("candidateReleaseProvenance() error = %v", err)
	}
}

func TestCandidateReleaseProvenanceCarriesAuthoredConnections(t *testing.T) {
	module := testCandidateModule(t, "principal_1")
	digest := "sha256:" + strings.Repeat("a", 64)
	started, err := module.candidates.Start(t.Context(), deployment.StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1", ArtifactDigest: digest,
	})
	require.NoError(t, err)

	artifacts := candidateArtifactSetForTest(digest, "state_public", release.GenerationDataRefreshSources)
	connectionID, _ := projectgraph.NewResourceID("public_http")
	artifacts.Generation.AuthoredConnections = []release.CandidateAuthoredConnection{{ConnectionID: connectionID, ConnectorKind: "http"}}
	provenance, err := candidateReleaseProvenance(
		started.Candidate,
		artifacts,
		deployment.CandidateRuntimeReceipt{RuntimeVersion: "runtime:test"},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, []release.AuthoredConnectionEvidence{{ConnectionID: "public_http", ConnectorKind: "http"}}, provenance.Plan.AuthoredConnections)
}

func TestCandidateSynchronizationPreservesReadyCandidateWhenPreparationFails(t *testing.T) {
	module := testCandidateModule(t, "principal_1")
	firstDigest := "sha256:" + strings.Repeat("a", 64)
	started, err := module.candidates.Start(t.Context(), deployment.StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1", ArtifactDigest: firstDigest,
	})
	require.NoError(t, err)
	ready, err := module.candidates.MarkReady(t.Context(), deployment.CandidateAccessScope{
		ProjectID: "finance", CandidateID: started.Candidate.ID, OwnerID: "principal_1",
	}, firstDigest, "sha256:"+strings.Repeat("f", 64))
	require.NoError(t, err)
	module.candidateSources = &candidateSourceSynchronizerStub{}
	module.candidateArtifacts = &candidateArtifactPreparerStub{
		err: release.ErrCandidateArtifactUnavailable,
	}
	module.candidateRuntimes = &candidateRuntimePreparerStub{}
	nextDigest := "sha256:" + strings.Repeat("b", 64)
	body := `{"projectFile":"leapview.yaml","artifactDigest":"` + nextDigest +
		`","expectedCandidateId":"` + ready.ID + `","expectedArtifactDigest":"` + firstDigest +
		`","artifacts":[]}`
	response := callCandidateAPI(
		t,
		http.MethodPost,
		"/api/v1/projects/finance/candidate-sync/commit",
		body,
		func(w http.ResponseWriter, r *http.Request) {
			module.CommitProjectCandidateSynchronization(w, r, "finance", "commit-2")
		},
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("preparation failure = %d %s", response.Code, response.Body.String())
	}
	current, err := module.candidates.Get(t.Context(), deployment.CandidateAccessScope{
		ProjectID: "finance", CandidateID: ready.ID, OwnerID: "principal_1",
	})
	require.NoError(t, err)
	if current.Status != deployment.CandidateReady || current.ArtifactDigest != firstDigest {
		t.Fatalf("last ready candidate changed: %#v", current)
	}
}

func TestCandidateSynchronizationRejectsBlobHeaderMismatchBeforeStorage(t *testing.T) {
	module := testCandidateModule(t, "principal_1")
	sources := &candidateSourceSynchronizerStub{}
	module.candidateSources = sources
	digest := "sha256:" + strings.Repeat("a", 64)
	response := callCandidateAPI(t, http.MethodPut, "/api/v1/projects/finance/candidate-sync/blobs/"+digest, "blob", func(w http.ResponseWriter, r *http.Request) {
		module.UploadProjectCandidateSourceBlob(w, r, "finance", digest, "application/octet-stream", "sha-256=:wrong:")
	})
	if response.Code != http.StatusUnprocessableEntity || len(sources.uploaded) != 0 {
		t.Fatalf("mismatched upload response = %d %s", response.Code, response.Body.String())
	}
}

func TestCandidateSynchronizationMapsProjectSourceErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{project.ErrCandidateSourceUnavailable, http.StatusServiceUnavailable, "CANDIDATE_SERVICE_UNAVAILABLE"},
		{project.ErrCandidateSourceConflict, http.StatusConflict, "CANDIDATE_CONFLICT"},
		{project.ErrCandidateSourceInvalid, http.StatusUnprocessableEntity, "INVALID_CANDIDATE"},
	}
	for _, test := range tests {
		module := testCandidateModule(t, "principal_1")
		module.candidateSources = &candidateSourceSynchronizerStub{planErr: test.err}
		response := callCandidateAPI(
			t,
			http.MethodPost,
			"/api/v1/projects/finance/candidate-sync/plan",
			`{"projectFile":"leapview.yaml","artifactDigest":"sha256:`+strings.Repeat("a", 64)+`","artifacts":[]}`,
			func(w http.ResponseWriter, r *http.Request) {
				module.PlanProjectCandidateSynchronization(w, r, "finance")
			},
		)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
			t.Fatalf("error %v response = %d %s", test.err, response.Code, response.Body.String())
		}
	}
}

func TestCandidateAPIStartsResumesUpdatesAndCancelsOwnedSession(t *testing.T) {
	module := testCandidateModule(t, "principal_1")
	digest := "sha256:" + strings.Repeat("a", 64)
	started := callCandidateAPI(t, http.MethodPost, "/api/v1/projects/finance/candidates", `{"artifactDigest":"`+digest+`","candidateKey":"github:pull/42"}`, func(w http.ResponseWriter, r *http.Request) {
		module.StartProjectCandidate(w, r, "finance", "start-1")
	})
	if started.Code != http.StatusCreated {
		t.Fatalf("start status = %d body=%s", started.Code, started.Body.String())
	}
	var created candidateAPIResponse
	decodeCandidateResponse(t, started, &created)
	if created.ID == "" || created.CandidateKey != "github:pull/42" ||
		created.BaseGeneration != "" ||
		created.Status != string(deployment.CandidatePreparing) {
		t.Fatalf("created candidate = %#v", created)
	}
	if want := "https://prod.leapview.example/candidates/" + created.ID; created.PreviewURL != want {
		t.Fatalf("preview URL = %q, want %q", created.PreviewURL, want)
	}
	if got := started.Header().Get("Location"); got != "/api/v1/projects/finance/candidates/"+created.ID {
		t.Fatalf("Location = %q", got)
	}

	resumed := callCandidateAPI(t, http.MethodPost, "/api/v1/projects/finance/candidates", `{"artifactDigest":"`+digest+`","candidateKey":"github:pull/42"}`, func(w http.ResponseWriter, r *http.Request) {
		module.StartProjectCandidate(w, r, "finance", "start-retry")
	})
	var replay candidateAPIResponse
	decodeCandidateResponse(t, resumed, &replay)
	if resumed.Code != http.StatusCreated || replay.ID != created.ID || !replay.Resumed {
		t.Fatalf("resumed status=%d candidate=%#v", resumed.Code, replay)
	}

	nextDigest := "sha256:" + strings.Repeat("b", 64)
	replaced := callCandidateAPI(t, http.MethodPut, "/api/v1/projects/finance/candidates/"+created.ID+"/artifact",
		`{"expectedArtifactDigest":"`+digest+`","artifactDigest":"`+nextDigest+`"}`,
		func(w http.ResponseWriter, r *http.Request) {
			module.ReplaceProjectCandidateArtifact(w, r, "finance", created.ID, "replace-1")
		})
	var updated candidateAPIResponse
	decodeCandidateResponse(t, replaced, &updated)
	if replaced.Code != http.StatusOK || updated.ArtifactDigest != nextDigest || updated.Revision != created.Revision+1 {
		t.Fatalf("replacement status=%d candidate=%#v", replaced.Code, updated)
	}

	cancelled := callCandidateAPI(t, http.MethodPost, "/api/v1/projects/finance/candidate-sessions/github:pull%2F42/cancel", "", func(w http.ResponseWriter, r *http.Request) {
		module.CancelProjectCandidateByKey(w, r, "finance", "github:pull/42", "cancel-1")
	})
	var stopped candidateAPIResponse
	decodeCandidateResponse(t, cancelled, &stopped)
	if cancelled.Code != http.StatusOK || stopped.Status != string(deployment.CandidateCancelled) {
		t.Fatalf("cancel status=%d candidate=%#v", cancelled.Code, stopped)
	}
}

func TestCandidateAPIConcealsForeignOwnershipAndMapsValidation(t *testing.T) {
	owner := testCandidateModule(t, "principal_1")
	digest := "sha256:" + strings.Repeat("a", 64)
	started := callCandidateAPI(t, http.MethodPost, "/api/v1/projects/finance/candidates", `{"artifactDigest":"`+digest+`"}`, func(w http.ResponseWriter, r *http.Request) {
		owner.StartProjectCandidate(w, r, "finance", "start-1")
	})
	var created candidateAPIResponse
	decodeCandidateResponse(t, started, &created)

	foreign := *owner
	foreign.handler = deploymenthttp.NewHandler(deploymenthttp.Options{
		CurrentPrincipal: func(*http.Request) (deploymenthttp.Principal, bool) {
			return deploymenthttp.Principal{ID: "principal_2"}, true
		},
		InstanceEnvironment: "prod",
	})
	hidden := callCandidateAPI(t, http.MethodGet, "/api/v1/projects/finance/candidates/"+created.ID, "", func(w http.ResponseWriter, r *http.Request) {
		foreign.GetProjectCandidate(w, r, "finance", created.ID)
	})
	if hidden.Code != http.StatusNotFound || !strings.Contains(hidden.Body.String(), "CANDIDATE_NOT_FOUND") {
		t.Fatalf("foreign response = %d %s", hidden.Code, hidden.Body.String())
	}

	invalid := callCandidateAPI(t, http.MethodPost, "/api/v1/projects/finance/candidates", `{"artifactDigest":"not-a-digest"}`, func(w http.ResponseWriter, r *http.Request) {
		owner.StartProjectCandidate(w, r, "finance", "invalid-1")
	})
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), "INVALID_CANDIDATE") {
		t.Fatalf("invalid response = %d %s", invalid.Code, invalid.Body.String())
	}
}

func TestCandidatePreviewMapsLifecycleAndConcealsRuntimeDetails(t *testing.T) {
	start := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		status deployment.CandidateStatus
		code   int
	}{
		{status: deployment.CandidatePreparing, code: http.StatusAccepted},
		{status: deployment.CandidateReady, code: http.StatusOK},
		{status: deployment.CandidateFailed, code: http.StatusConflict},
		{status: deployment.CandidateCancelled, code: http.StatusGone},
		{status: deployment.CandidateExpired, code: http.StatusGone},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			now := start
			module := testCandidateModuleWithClock(t, "principal_1", func() time.Time { return now }, time.Minute)
			lifecycle := module.candidateRuntimeLifecycle.(*candidateRuntimeLifecycleRecorder)
			digest := "sha256:" + strings.Repeat("a", 64)
			started, err := module.candidates.Start(context.Background(), deployment.StartCandidateRequest{
				ProjectID: "finance", OwnerID: "principal_1", ArtifactDigest: digest,
			})
			require.NoError(t, err)
			scope := deployment.CandidateAccessScope{
				ProjectID: "finance", CandidateID: started.Candidate.ID, OwnerID: "principal_1",
			}
			now = now.Add(30 * time.Second)
			switch test.status {
			case deployment.CandidateReady:
				_, err = module.candidates.MarkReady(
					context.Background(),
					scope,
					digest,
					"sha256:"+strings.Repeat("f", 64),
				)
			case deployment.CandidateFailed:
				_, err = module.candidates.MarkFailed(context.Background(), scope, digest, "RUNTIME_PREPARATION_FAILED")
			case deployment.CandidateCancelled:
				_, err = module.candidates.Cancel(context.Background(), scope)
			case deployment.CandidateExpired:
				now = start.Add(2 * time.Minute)
			}
			require.NoError(t, err)

			request := httptest.NewRequest(http.MethodGet, "/candidates/"+started.Candidate.ID, nil)
			response := httptest.NewRecorder()
			module.ServeCandidatePreview(response, request, started.Candidate.ID, "principal_1", nil)
			if response.Code != test.code {
				t.Fatalf("status = %d, want %d body=%s", response.Code, test.code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
			}
			if test.status == deployment.CandidateExpired && (len(lifecycle.retired) != 1 || lifecycle.retired[0] != started.Candidate.ID) {
				t.Fatalf("expired candidate runtimes retired = %#v, want %q", lifecycle.retired, started.Candidate.ID)
			}
			for _, forbidden := range []string{
				started.Candidate.ArtifactDigest, started.Candidate.OwnerID,
				started.Candidate.Scope.ProjectID.String(), started.Candidate.TargetID,
			} {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("preview leaked %q: %s", forbidden, response.Body.String())
				}
			}
		})
	}
}

func TestCandidatePreviewConcealsForeignOwnership(t *testing.T) {
	module := testCandidateModule(t, "principal_1")
	digest := "sha256:" + strings.Repeat("a", 64)
	started, err := module.candidates.Start(context.Background(), deployment.StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1", ArtifactDigest: digest,
	})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodGet, "/candidates/"+started.Candidate.ID, nil)
	response := httptest.NewRecorder()
	module.ServeCandidatePreview(response, request, started.Candidate.ID, "principal_2", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("foreign preview status = %d body=%s", response.Code, response.Body.String())
	}
}

func testCandidateModule(t *testing.T, principalID string) *Module {
	t.Helper()
	return testCandidateModuleWithClock(
		t,
		principalID,
		func() time.Time { return time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC) },
		0,
	)
}

func testCandidateModuleWithClock(t *testing.T, principalID string, now func() time.Time, lifetime time.Duration) *Module {
	t.Helper()
	store, err := platform.Open(context.Background(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	for _, id := range []string{"principal_1", "principal_2"} {
		if _, err := store.SQLDB().ExecContext(context.Background(),
			`INSERT INTO principals (id, email, display_name) VALUES (?, ?, ?)`,
			id, id+"@example.test", id,
		); err != nil {
			t.Fatal(err)
		}
	}
	repository := deploymentsqlite.NewRepositoryWithHooks(store.SQLDB(), deploymentsqlite.ActivationHooks{})
	lifecycle := &candidateRuntimeLifecycleRecorder{}
	service, err := deployment.NewCandidateService(repository, deployment.CandidateServiceConfig{
		TargetID: "lvinst_prod", CanonicalOrigin: "https://prod.leapview.example", Environment: "prod",
		Lifetime: lifetime, Now: now, NewID: func() (string, error) { return "cand_opaque_1", nil },
		RuntimeLifecycle: lifecycle,
		Audit:            func(context.Context, deployment.CandidateEvent) error { return nil },
	})
	require.NoError(t, err)
	return &Module{
		candidates:                service,
		candidateRuntimeLifecycle: lifecycle,
		candidateSourceBlobAudit: func(context.Context, CandidateSourceBlobAuditEvent) error {
			return nil
		},
		handler: deploymenthttp.NewHandler(deploymenthttp.Options{
			CurrentPrincipal: func(*http.Request) (deploymenthttp.Principal, bool) {
				return deploymenthttp.Principal{ID: principalID}, true
			},
			InstanceEnvironment: "prod",
		}),
	}
}

func callCandidateAPI(t *testing.T, method, target, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler(response, request)
	return response
}

type candidateAPIResponse struct {
	ID               string `json:"id"`
	CandidateKey     string `json:"candidateKey"`
	BaseGeneration   string `json:"baseGeneration"`
	ArtifactDigest   string `json:"artifactDigest"`
	ProvenanceDigest string `json:"provenanceDigest"`
	Status           string `json:"status"`
	PreviewURL       string `json:"previewUrl"`
	Revision         int64  `json:"revision"`
	Resumed          bool   `json:"resumed"`
}

func decodeCandidateResponse(t *testing.T, response *httptest.ResponseRecorder, target *candidateAPIResponse) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode candidate response: %v body=%s", err, response.Body.String())
	}
}

type candidateSourceSynchronizerStub struct {
	missing  []string
	uploaded []byte
	commits  int
	planErr  error
}

func (stub *candidateSourceSynchronizerStub) Plan(
	context.Context,
	deployment.CandidateSourceScope,
	deployment.CandidateSynchronizationRequest,
) ([]string, error) {
	return append([]string(nil), stub.missing...), stub.planErr
}

func (stub *candidateSourceSynchronizerStub) Upload(
	_ context.Context,
	_ deployment.CandidateSourceScope,
	_ string,
	source io.Reader,
) error {
	bytes, err := io.ReadAll(source)
	stub.uploaded = bytes
	return err
}

func (stub *candidateSourceSynchronizerStub) Commit(
	_ context.Context,
	_ deployment.CandidateSourceScope,
	request deployment.CandidateSynchronizationRequest,
) (project.CandidateSourceSnapshot, error) {
	stub.commits++
	return project.CandidateSourceSnapshot{
		ProjectID: "finance", ArtifactDigest: request.ArtifactDigest,
		ProjectPath:    "/target/snapshots/project/leapview.yaml",
		SourceRevision: request.SourceRevision,
	}, nil
}

type candidateArtifactPreparerStub struct {
	result    release.CandidateArtifactSet
	err       error
	retained  []release.Provenance
	retainErr error
	lookupErr error
}

func (stub *candidateArtifactPreparerStub) PrepareCandidateArtifacts(
	context.Context,
	release.CandidateArtifactRequest,
) (release.CandidateArtifactSet, error) {
	return stub.result, stub.err
}

func (stub *candidateArtifactPreparerStub) RetainCandidateProvenance(
	_ context.Context,
	_ projectgraph.ResourceID,
	provenance release.Provenance,
) (release.Provenance, error) {
	if stub.retainErr != nil {
		return release.Provenance{}, stub.retainErr
	}
	stub.retained = append(stub.retained, provenance)
	return provenance, nil
}

func (stub *candidateArtifactPreparerStub) CandidateProvenance(
	_ context.Context,
	_ projectgraph.ResourceID,
	_ string,
	revision int64,
) (release.Provenance, error) {
	if stub.lookupErr != nil {
		return release.Provenance{}, stub.lookupErr
	}
	for _, provenance := range stub.retained {
		if provenance.Candidate.Revision == revision {
			return provenance, nil
		}
	}
	return release.Provenance{}, release.ErrNotFound
}

type candidateRuntimePreparerStub struct {
	calls            int
	err              error
	receipt          deployment.CandidateRuntimeReceipt
	requireAdmission bool
}

type candidateRuntimeLifecycleRecorder struct {
	retired []string
	reaped  int
}

func (recorder *candidateRuntimeLifecycleRecorder) RetireCandidate(id string) int {
	recorder.retired = append(recorder.retired, id)
	return 1
}

func (recorder *candidateRuntimeLifecycleRecorder) ReapExpiredCandidates(time.Time) int {
	recorder.reaped++
	return 0
}

func (stub *candidateRuntimePreparerStub) Prepare(
	ctx context.Context,
	_ deployment.CandidateRuntimeRequest,
) (deployment.CandidateRuntimeReceipt, error) {
	stub.calls++
	if stub.requireAdmission {
		if admitted, _ := ctx.Value(candidatePreparationAdmissionKey{}).(bool); !admitted {
			return deployment.CandidateRuntimeReceipt{}, errors.New(
				"candidate runtime preparation was not admitted as control work",
			)
		}
	}
	return stub.receipt, stub.err
}

type candidatePreparationAdmissionKey struct{}

type candidatePreparationAdmitterStub struct{}

func (candidatePreparationAdmitterStub) AcquireCandidatePreparation(
	ctx context.Context,
) (CandidatePreparationLease, error) {
	return candidatePreparationLeaseStub{
		ctx: context.WithValue(ctx, candidatePreparationAdmissionKey{}, true),
	}, nil
}

type candidatePreparationLeaseStub struct {
	ctx context.Context
}

func (lease candidatePreparationLeaseStub) Context() context.Context { return lease.ctx }
func (candidatePreparationLeaseStub) Release()                       {}

func standardContentDigest(t *testing.T, identity string) string {
	t.Helper()
	decoded, err := hex.DecodeString(strings.TrimPrefix(identity, "sha256:"))
	require.NoError(t, err)
	return "sha-256=:" + base64.StdEncoding.EncodeToString(decoded) + ":"
}

func testServingIdentity(generation string) projectgraph.ServingIdentity {
	identity, err := projectgraph.NewServingIdentity("finance", "prod", generation)
	if err != nil {
		panic(err)
	}
	return identity
}

func candidateArtifactSetForTest(sourceDigest, generation string, mode release.GenerationDataMode) release.CandidateArtifactSet {
	return release.CandidateArtifactSet{
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: sourceDigest, ProjectDigest: sourceDigest, ContentDigest: sourceDigest, CompilerVersion: "compiler:test", SchemaVersion: 2},
		AuthorizationFingerprint: sourceDigest,
		Generation:               release.CandidateGenerationArtifact{Identity: testServingIdentity(generation), ArtifactDigest: sourceDigest, DataRevision: "snapshot:1", DataMode: mode},
	}
}
