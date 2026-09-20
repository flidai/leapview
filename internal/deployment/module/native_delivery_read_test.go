package module

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

type nativeReadFixture struct{}

func (nativeReadFixture) Plan(context.Context, string) (nativepostgres.DeliveryPlan, error) {
	return nativepostgres.DeliveryPlan{}, nativepostgres.ErrNotFound
}
func (nativeReadFixture) LoadPlan(context.Context, string) (nativepostgres.DeliveryPlan, error) {
	return nativeReadFixture{}.Plan(context.Background(), "")
}
func (nativeReadFixture) BuildAttempt(context.Context, string) (nativepostgres.DeliveryBuildAttempt, error) {
	return nativepostgres.DeliveryBuildAttempt{}, nativepostgres.ErrNotFound
}
func (nativeReadFixture) LoadBuildAttempt(context.Context, string) (nativepostgres.DeliveryBuildAttempt, error) {
	return nativeReadFixture{}.BuildAttempt(context.Background(), "")
}
func (nativeReadFixture) SnapshotSeal(context.Context, string) (nativepostgres.SnapshotSeal, error) {
	return nativepostgres.SnapshotSeal{}, nativepostgres.ErrNotFound
}
func (nativeReadFixture) LoadSnapshotSeal(context.Context, string) (nativepostgres.SnapshotSeal, error) {
	return nativeReadFixture{}.SnapshotSeal(context.Background(), "")
}
func (nativeReadFixture) Candidate(context.Context, string) (nativepostgres.DeliveryCandidate, error) {
	return nativepostgres.DeliveryCandidate{}, nativepostgres.ErrNotFound
}
func (nativeReadFixture) LoadCandidate(context.Context, string) (nativepostgres.DeliveryCandidate, error) {
	return nativeReadFixture{}.Candidate(context.Background(), "")
}
func (nativeReadFixture) ResolveCandidateGeneration(context.Context, string) (nativepostgres.CandidateGenerationResolution, error) {
	return nativepostgres.CandidateGenerationResolution{}, nativepostgres.ErrNotFound
}
func (nativeReadFixture) Generation(context.Context, string) (nativepostgres.DeliveryGeneration, error) {
	return nativepostgres.DeliveryGeneration{}, nativepostgres.ErrNotFound
}
func (nativeReadFixture) LoadGeneration(context.Context, string) (nativepostgres.DeliveryGeneration, error) {
	return nativeReadFixture{}.Generation(context.Background(), "")
}
func (nativeReadFixture) Publication(context.Context, string) (nativepostgres.DeliveryPublication, error) {
	return nativepostgres.DeliveryPublication{}, nativepostgres.ErrNotFound
}
func (nativeReadFixture) LoadPublication(context.Context, string) (nativepostgres.DeliveryPublication, error) {
	return nativeReadFixture{}.Publication(context.Background(), "")
}
func (nativeReadFixture) OperatorSnapshot(context.Context, string) (nativepostgres.DeliveryOperatorSnapshot, error) {
	return nativepostgres.DeliveryOperatorSnapshot{ProjectID: "finance", Environment: "prod", TargetID: "target", TargetRevision: 7}, nil
}

var _ NativeDeliveryReader = nativeReadFixture{}

func TestNativeDeliveryOperatorReadUsesNativePort(t *testing.T) {
	m := &Module{
		nativeDeliveryReader: nativeReadFixture{}, instanceID: "target",
		handler: deploymenthttp.NewHandler(deploymenthttp.Options{
			InstanceEnvironment: "prod",
			CurrentPrincipal: func(*http.Request) (deploymenthttp.Principal, bool) {
				return deploymenthttp.Principal{ID: "operator"}, true
			},
		}),
	}
	recorder := httptest.NewRecorder()
	m.GetDeliveryOperatorSnapshot(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response deploymentgen.DeliveryOperatorSnapshotResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Degraded || len(response.DegradedReasons) != 1 || response.DegradedReasons[0] != "detailed_evidence_unavailable" || response.ActiveGeneration != nil {
		t.Fatalf("operator response = %#v, want bounded degraded snapshot", response)
	}
}

func TestNativeDeliveryOperatorReadPreservesOwnedActivePointerWhenPartial(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	m := nativeReadModule(rows)
	recorder := httptest.NewRecorder()
	m.GetDeliveryOperatorSnapshot(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response deploymentgen.DeliveryOperatorSnapshotResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ActiveGeneration == nil || *response.ActiveGeneration != rows.generation.GenerationID || !response.Degraded || len(response.DegradedReasons) != 1 || response.DegradedReasons[0] != "detailed_evidence_unavailable" {
		t.Fatalf("operator response = %#v, want active pointer plus explicit partial-evidence degradation", response)
	}
}

func TestNativeDeliveryReadMapsMissingRowsToNotFound(t *testing.T) {
	m := &Module{
		nativeDeliveryReader: nativeReadFixture{},
		handler: deploymenthttp.NewHandler(deploymenthttp.Options{
			InstanceEnvironment: "prod",
			CurrentPrincipal: func(*http.Request) (deploymenthttp.Principal, bool) {
				return deploymenthttp.Principal{ID: "operator"}, true
			},
		}),
	}
	recorder := httptest.NewRecorder()
	m.GetDeliveryPlanPreview(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", "plan")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

type nativeReadRows struct {
	plan        nativepostgres.DeliveryPlan
	attempt     nativepostgres.DeliveryBuildAttempt
	seal        nativepostgres.SnapshotSeal
	candidate   nativepostgres.DeliveryCandidate
	generation  nativepostgres.DeliveryGeneration
	publication nativepostgres.DeliveryPublication
}

type nativeReadRowsWithGeneration struct {
	nativeReadRows
	generationOverride nativepostgres.DeliveryGeneration
}

type nativeReadRowsWithLifecycle struct {
	nativeReadRows
	root          nativepostgres.DeliveryRetentionRoot
	rollbackUntil time.Time
}

type nativeReadRowsWithoutGeneration struct{ nativeReadRows }

func (nativeReadRowsWithoutGeneration) ResolveCandidateGeneration(context.Context, string) (nativepostgres.CandidateGenerationResolution, error) {
	return nativepostgres.CandidateGenerationResolution{}, nativepostgres.ErrNotFound
}

func (r nativeReadRowsWithGeneration) Generation(context.Context, string) (nativepostgres.DeliveryGeneration, error) {
	return r.generationOverride, nil
}

func (r nativeReadRowsWithGeneration) LoadGeneration(ctx context.Context, id string) (nativepostgres.DeliveryGeneration, error) {
	return r.Generation(ctx, id)
}

func (r nativeReadRowsWithLifecycle) OperatorSnapshot(context.Context, string) (nativepostgres.DeliveryOperatorSnapshot, error) {
	return nativepostgres.DeliveryOperatorSnapshot{ProjectID: "finance", Environment: "prod", TargetID: "target", TargetRevision: 3}, nil
}

func (r nativeReadRowsWithLifecycle) HistoricalCommittedPublication(context.Context, string) (nativepostgres.DeliveryPublication, error) {
	return r.publication, nil
}

func (r nativeReadRowsWithLifecycle) GenerationRetentionRoot(context.Context, string) (nativepostgres.DeliveryRetentionRoot, error) {
	return r.root, nil
}

func (r nativeReadRowsWithLifecycle) GenerationRollbackUntil(context.Context, string) (time.Time, error) {
	return r.rollbackUntil, nil
}

func (r nativeReadRows) Plan(context.Context, string) (nativepostgres.DeliveryPlan, error) {
	return r.plan, nil
}
func (r nativeReadRows) LoadPlan(ctx context.Context, id string) (nativepostgres.DeliveryPlan, error) {
	return r.Plan(ctx, id)
}
func (r nativeReadRows) BuildAttempt(context.Context, string) (nativepostgres.DeliveryBuildAttempt, error) {
	return r.attempt, nil
}
func (r nativeReadRows) LoadBuildAttempt(ctx context.Context, id string) (nativepostgres.DeliveryBuildAttempt, error) {
	return r.BuildAttempt(ctx, id)
}
func (r nativeReadRows) SnapshotSeal(context.Context, string) (nativepostgres.SnapshotSeal, error) {
	return r.seal, nil
}
func (r nativeReadRows) LoadSnapshotSeal(ctx context.Context, id string) (nativepostgres.SnapshotSeal, error) {
	return r.SnapshotSeal(ctx, id)
}
func (r nativeReadRows) Candidate(context.Context, string) (nativepostgres.DeliveryCandidate, error) {
	return r.candidate, nil
}
func (r nativeReadRows) LoadCandidate(ctx context.Context, id string) (nativepostgres.DeliveryCandidate, error) {
	return r.Candidate(ctx, id)
}
func (r nativeReadRows) Generation(context.Context, string) (nativepostgres.DeliveryGeneration, error) {
	return r.generation, nil
}
func (r nativeReadRows) LoadGeneration(ctx context.Context, id string) (nativepostgres.DeliveryGeneration, error) {
	return r.Generation(ctx, id)
}
func (r nativeReadRows) Publication(context.Context, string) (nativepostgres.DeliveryPublication, error) {
	return r.publication, nil
}
func (r nativeReadRows) LoadPublication(ctx context.Context, id string) (nativepostgres.DeliveryPublication, error) {
	return r.Publication(ctx, id)
}
func (r nativeReadRows) OperatorSnapshot(context.Context, string) (nativepostgres.DeliveryOperatorSnapshot, error) {
	return nativepostgres.DeliveryOperatorSnapshot{ProjectID: "finance", Environment: "prod", TargetID: "target", TargetRevision: 3, ActiveGenerationID: r.generation.GenerationID, ActivePublicationID: r.publication.PublicationID}, nil
}
func (r nativeReadRows) ResolveCandidateGeneration(context.Context, string) (nativepostgres.CandidateGenerationResolution, error) {
	return nativepostgres.CandidateGenerationResolution{
		CandidateID: r.candidate.CandidateID, TargetID: r.candidate.TargetID,
		PlanID: r.candidate.PlanID, SnapshotSealID: r.candidate.SnapshotSealID,
		Status: r.candidate.Status, CandidateRevision: r.candidate.CandidateRevision,
		ArtifactDigest: r.candidate.ArtifactDigest, ProjectID: "finance", Environment: "prod",
		GenerationCount: 1, GenerationID: r.generation.GenerationID,
	}, nil
}

func nativeReadDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func nativeReadRowsFixture(t *testing.T, target string) nativeReadRows {
	t.Helper()
	project, err := projectgraph.NewResourceID("finance")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	plan, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID: "0198f2c0-7c7a-7f00-8a11-000000000201", TargetID: target, ProjectID: project, Environment: "prod", Operation: deployment.DeliveryOperationCodeChange,
		ActorID: "actor", SourceOwnerID: "owner", SourceDigest: nativeReadDigest('a'), ServingArtifactDigest: nativeReadDigest('b'), CreatedAt: now,
		Execution:  deployment.DeliveryExecutionInputs{SourceArtifactDigest: nativeReadDigest('a'), CompilerDigest: nativeReadDigest('c'), ExecutableDigest: nativeReadDigest('d'), DependencyDigest: nativeReadDigest('e'), ConfigDigest: nativeReadDigest('f'), BindingDigest: nativeReadDigest('1'), RuntimeDigest: nativeReadDigest('2'), CapabilityDigest: nativeReadDigest('3')},
		Provenance: deployment.DeliveryProvenance{Builder: "native-test"},
		Governance: deployment.DeliveryGovernance{PolicyDigest: nativeReadDigest('4'), AuthorizationDigest: nativeReadDigest('5'), QualificationDigest: nativeReadDigest('6'), ApprovalPolicyRevision: 1, ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)},
		Evidence: deployment.DeliveryPlanEvidence{
			ImpactStatement:       "native read fixture impact",
			PhysicalWorkStatement: "native read fixture physical work",
			ReuseStatement:        "native read fixture has no reuse",
			Qualification:         deployment.DeliveryQualificationEvidence{Policy: "required", Steps: []deployment.DeliveryQualificationStep{{ID: "snapshot", Kind: "contract", Description: "qualify", Required: true, Blocking: true}}},
			StalePolicy:           deployment.DeliveryStalePolicy{Mode: "reject"},
			Rollback:              deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryServingSafe},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := json.Marshal(plan.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	planRow := nativepostgres.DeliveryPlan{PlanID: plan.ID, TargetID: target, PlanDigest: plan.Digest, CompiledConfigDigest: plan.Execution.ConfigDigest, SecurityDomainFingerprint: plan.Governance.AuthorizationDigest, ArtifactDigest: plan.ServingArtifactDigest, QualificationDigest: plan.Governance.QualificationDigest, QualificationRequired: true, ApprovalPolicyRevision: plan.Governance.ApprovalPolicyRevision, PlanDocument: document, Evidence: evidence, CreatedAt: now}
	attemptID, candidateID, sealID, generationID := "0198f2c0-7c7a-7f00-8a11-000000000202", "0198f2c0-7c7a-7f00-8a11-000000000203", "0198f2c0-7c7a-7f00-8a11-000000000204", "0198f2c0-7c7a-7f00-8a11-000000000205"
	return nativeReadRows{plan: planRow,
		attempt:     nativepostgres.DeliveryBuildAttempt{AttemptID: attemptID, PlanID: plan.ID, CandidateID: candidateID, OwnerID: "owner", PhysicalPoolID: "pool", FencingEpoch: 1, PlanDigest: plan.Digest, RequestDigest: nativeReadDigest('7'), State: nativepostgres.AttemptCommitted, CreatedAt: now, UpdatedAt: now, FinishedAt: now},
		seal:        nativepostgres.SnapshotSeal{SealID: sealID, AttemptID: attemptID, CandidateID: candidateID, PhysicalPoolID: "pool", PlanDigest: plan.Digest, ClosureDigest: nativeReadDigest('8'), RelationManifestDigest: nativeReadDigest('9'), CompatibilityDigest: nativeReadDigest('0'), ServingArtifactID: "artifact", ServingArtifactDigest: plan.ServingArtifactDigest, QualifiedAt: now},
		candidate:   nativepostgres.DeliveryCandidate{CandidateID: candidateID, TargetID: target, PlanID: plan.ID, SnapshotSealID: sealID, Status: "qualified", CandidateRevision: 1, ArtifactDigest: plan.ServingArtifactDigest, QualificationDigest: plan.Governance.QualificationDigest, CreatedAt: now, QualifiedAt: now},
		generation:  nativepostgres.DeliveryGeneration{GenerationID: generationID, TargetID: target, CandidateID: candidateID, SnapshotSealID: sealID, PlanID: plan.ID, PlanDigest: plan.Digest, ServingArtifactDigest: plan.ServingArtifactDigest, GenerationRevision: 1, CreatedAt: now},
		publication: nativepostgres.DeliveryPublication{PublicationID: "0198f2c0-7c7a-7f00-8a11-000000000206", TargetID: target, GenerationID: generationID, CandidateID: candidateID, SnapshotSealID: sealID, ExpectedTargetRevision: 1, ResultTargetRevision: 2, State: "committed", RequestDigest: nativeReadDigest('a'), CreatedAt: now, CommittedAt: now},
	}
}

func nativeReadModule(reader NativeDeliveryReader) *Module {
	return &Module{
		nativeDeliveryReader: reader,
		instanceID:           "target",
		handler: deploymenthttp.NewHandler(deploymenthttp.Options{
			InstanceEnvironment: "prod",
			CurrentPrincipal: func(*http.Request) (deploymenthttp.Principal, bool) {
				return deploymenthttp.Principal{ID: "operator"}, true
			},
		}),
	}
}

func TestNativeDeliveryObjectReadsUseNativePort(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	m := nativeReadModule(rows)
	tests := []struct {
		name string
		id   string
		get  func(http.ResponseWriter, *http.Request)
	}{
		{name: "plan", id: rows.plan.PlanID, get: func(w http.ResponseWriter, r *http.Request) {
			m.GetDeliveryPlanPreview(w, r, "finance", rows.plan.PlanID)
		}},
		{name: "build", id: rows.attempt.AttemptID, get: func(w http.ResponseWriter, r *http.Request) {
			m.GetDeliveryBuildStatus(w, r, "finance", rows.attempt.AttemptID)
		}},
		{name: "seal", id: rows.seal.SealID, get: func(w http.ResponseWriter, r *http.Request) {
			m.GetDeliverySealStatus(w, r, "finance", rows.seal.SealID)
		}},
		{name: "candidate", id: rows.candidate.CandidateID, get: func(w http.ResponseWriter, r *http.Request) {
			m.GetDeliveryCandidateStatus(w, r, "finance", rows.candidate.CandidateID)
		}},
		{name: "generation", id: rows.generation.GenerationID, get: func(w http.ResponseWriter, r *http.Request) {
			m.GetDeliveryGenerationStatus(w, r, "finance", rows.generation.GenerationID)
		}},
		{name: "publication", id: rows.publication.PublicationID, get: func(w http.ResponseWriter, r *http.Request) {
			m.GetDeliveryPublicationEvidence(w, r, "finance", rows.publication.PublicationID)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.get(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if !bytes.Contains(recorder.Body.Bytes(), []byte(test.id)) {
				t.Fatalf("response does not contain %q: %s", test.id, recorder.Body.String())
			}
		})
	}
}

func TestNativeDeliveryBuildReadProjectsCandidateRevisionSeparately(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	m := nativeReadModule(rows)
	recorder := httptest.NewRecorder()
	m.GetDeliveryBuildStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.attempt.AttemptID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response deploymentgen.DeliveryBuildStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Revision != rows.attempt.FencingEpoch || response.CandidateRevision == nil || *response.CandidateRevision != rows.candidate.CandidateRevision {
		t.Fatalf("build revisions = attempt %d candidate %v, want attempt %d candidate %d", response.Revision, response.CandidateRevision, rows.attempt.FencingEpoch, rows.candidate.CandidateRevision)
	}
}

func TestNativeDeliveryBuildReadPreservesTerminalFailureClassification(t *testing.T) {
	for _, test := range []struct {
		state  nativepostgres.BuildAttemptState
		status deploymentgen.DeliveryBuildStatus
		code   string
	}{
		{state: nativepostgres.AttemptAborted, status: deploymentgen.DeliveryBuildStatusFailed, code: "deterministic_no_commit"},
		{state: nativepostgres.AttemptIndeterminate, status: deploymentgen.DeliveryBuildStatusAbandoned, code: "indeterminate"},
	} {
		t.Run(string(test.state), func(t *testing.T) {
			rows := nativeReadRowsFixture(t, "target")
			rows.attempt.State = test.state
			rows.attempt.TerminationEvidence = json.RawMessage(`{"classification":"` + test.code + `"}`)
			m := nativeReadModule(rows)
			recorder := httptest.NewRecorder()
			m.GetDeliveryBuildStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.attempt.AttemptID)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var response deploymentgen.DeliveryBuildStatusResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Status != test.status || response.FailureCode == nil || *response.FailureCode != test.code || response.TerminalAt == nil {
				t.Fatalf("terminal build response = %#v, want status %q and failure %q", response, test.status, test.code)
			}
		})
	}
}

func TestNativeDeliverySealReadPreservesQualificationEvidence(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	rows.seal.CreatedAt = rows.seal.QualifiedAt.Add(-time.Minute)
	m := nativeReadModule(rows)
	recorder := httptest.NewRecorder()
	m.GetDeliverySealStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.seal.SealID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response deploymentgen.DeliverySealStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != deploymentgen.DeliverySealStatusVerified || response.QualificationDigest == nil || *response.QualificationDigest != rows.candidate.QualificationDigest || response.CreatedAt != isoTime(rows.seal.CreatedAt) || response.VerifiedAt == nil || *response.VerifiedAt != isoTime(rows.seal.QualifiedAt) {
		t.Fatalf("seal response = %#v, want verified qualification and durable creation timestamp", response)
	}
}

func TestNativeDeliverySealReadPreservesAttemptFailure(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	rows.attempt.State = nativepostgres.AttemptAborted
	rows.attempt.TerminationEvidence = json.RawMessage(`{"classification":"deterministic_no_commit"}`)
	m := nativeReadModule(rows)
	recorder := httptest.NewRecorder()
	m.GetDeliverySealStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.seal.SealID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response deploymentgen.DeliverySealStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != deploymentgen.DeliverySealStatusFailed || response.FailureCode == nil || *response.FailureCode != "deterministic_no_commit" {
		t.Fatalf("seal response = %#v, want failed seal with terminal classification", response)
	}
}

func TestNativeDeliveryCandidateReadDoesNotInventResolvedInputs(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	m := nativeReadModule(rows)
	recorder := httptest.NewRecorder()
	m.GetDeliveryCandidateStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.candidate.CandidateID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response deploymentgen.DeliveryCandidateStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ResolvedInputs == nil || len(response.ResolvedInputs) != 0 || response.ResolvedInputsDigest != nil {
		t.Fatalf("candidate response = %#v, want no unowned resolved-input evidence", response)
	}
}

func TestNativeDeliveryCandidateReadProjectsResolvedInputs(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	plan, err := rows.plan.RichPlan()
	if err != nil {
		t.Fatal(err)
	}
	gate, err := (release.GateEvidence{
		Version: 1, CandidateID: rows.candidate.CandidateID, SourceDigest: plan.SourceDigest,
		BindingGeneration: nativeReadDigest('a'), RuntimeVersion: "runtime-v1", DuckDBVersion: "duckdb-v1",
		Bounds: release.GateBounds{MaxRows: 1, MaxQueries: 1, MaxMillis: 1}, Outcome: release.GateSuccess,
		EvaluatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
	}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	qualification := nativeQualificationEvidenceEnvelope{
		SchemaVersion: 1, CandidateID: rows.candidate.CandidateID, AttemptID: rows.attempt.AttemptID,
		PhysicalPoolID: rows.seal.PhysicalPoolID, CatalogID: "catalog", SnapshotID: 42,
		ObjectRoot: "objects/root", RelationNamespace: "candidate/relation",
		RelationManifestDigest: rows.seal.RelationManifestDigest, ClosureDigest: rows.seal.ClosureDigest,
		Runtime: nativePreviewRuntimeEvidence{SnapshotID: 42, CatalogType: "postgres", DataPath: rows.seal.ObjectRoot, MetadataSchema: "metadata", DuckDBRuntime: "duckdb-v1", DuckLakeExtension: rows.seal.DuckLakeExtensionVersion, CatalogFormat: rows.seal.DuckLakeSpecVersion, CompatibilityDigest: rows.seal.CompatibilityDigest, CatalogSchemaVersion: rows.seal.CatalogSchemaVersion},
		Gates:   gate,
	}
	rows.seal.QualificationEvidence = marshalNativePreviewEnvelope(t, qualification)
	qualificationDecoded, err := decodeNativePreviewGateEvidence(rows.seal.QualificationEvidence)
	if err != nil {
		t.Fatal(err)
	}
	rows.candidate.QualificationDigest = qualificationDecoded.Digest
	resolved, err := deployment.ValidateDeliveryResolvedBuildInputs(plan, deployment.DeliveryResolvedBuildInputs{PolicyDigest: plan.Governance.PolicyDigest, GateEvidence: &gate})
	if err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(nativeResolvedInputsRecord{Inputs: resolved.Inputs, PolicyDigest: resolved.PolicyDigest, EvidenceDigest: resolved.EvidenceDigest})
	if err != nil {
		t.Fatal(err)
	}
	rows.seal.ResolvedInputs, rows.seal.ResolvedInputsDigest = record, resolved.EvidenceDigest
	rows.candidate.ResolvedInputs, rows.candidate.ResolvedInputsDigest = record, resolved.EvidenceDigest
	if _, err := nativeCandidateResolvedInputs(rows.candidate, plan, rows.seal); err != nil {
		t.Fatalf("resolve candidate inputs: %v", err)
	}
	m := nativeReadModule(rows)
	recorder := httptest.NewRecorder()
	m.GetDeliveryCandidateStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.candidate.CandidateID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response deploymentgen.DeliveryCandidateStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ResolvedInputs == nil || len(response.ResolvedInputs) != 0 || response.ResolvedInputsDigest == nil || *response.ResolvedInputsDigest != resolved.EvidenceDigest {
		t.Fatalf("candidate response = %#v, want resolved-input digest", response)
	}
}

func TestNativeDeliveryGenerationReadPreservesActivationTimestamp(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	m := nativeReadModule(rows)
	recorder := httptest.NewRecorder()
	m.GetDeliveryGenerationStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.generation.GenerationID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response deploymentgen.DeliveryGenerationStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != deploymentgen.DeliveryGenerationStatusActive || response.ActivatedAt == nil || *response.ActivatedAt != isoTime(rows.publication.CommittedAt) {
		t.Fatalf("generation response = %#v, want active timestamp %q", response, isoTime(rows.publication.CommittedAt))
	}
}

func TestNativeDeliveryHistoricalGenerationReadPreservesRecoveryWindows(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	retiredAt := rows.publication.CommittedAt.Add(time.Hour)
	rollbackUntil := retiredAt.Add(24 * time.Hour)
	reader := nativeReadRowsWithLifecycle{
		nativeReadRows: rows,
		root: nativepostgres.DeliveryRetentionRoot{
			TargetID: rows.generation.TargetID, GenerationID: rows.generation.GenerationID,
			RootKind: "generation", State: "retiring", RetiredAt: retiredAt,
		},
		rollbackUntil: rollbackUntil,
	}
	m := nativeReadModule(reader)
	recorder := httptest.NewRecorder()
	m.GetDeliveryGenerationStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.generation.GenerationID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response deploymentgen.DeliveryGenerationStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != deploymentgen.DeliveryGenerationStatusRetired || response.ActivatedAt == nil || *response.ActivatedAt != isoTime(rows.publication.CommittedAt) || response.RetiredAt == nil || *response.RetiredAt != isoTime(retiredAt) || response.RollbackUntil == nil || *response.RollbackUntil != isoTime(rollbackUntil) {
		t.Fatalf("historical generation response = %#v, want retired activation/retirement/rollback windows", response)
	}
}

func TestNativeDeliveryCandidateReadProjectsResolvedServingState(t *testing.T) {
	for _, status := range []string{"qualified", "admitted"} {
		t.Run(status, func(t *testing.T) {
			rows := nativeReadRowsFixture(t, "target")
			rows.candidate.Status = status
			m := nativeReadModule(rows)
			recorder := httptest.NewRecorder()
			m.GetDeliveryCandidateStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.candidate.CandidateID)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var response deploymentgen.DeliveryCandidateStatusResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Status != deploymentgen.DeliveryCandidateStatusReady || response.ServingStateId != rows.generation.GenerationID {
				t.Fatalf("candidate status = %q, serving state id = %q, want ready and %q", response.Status, response.ServingStateId, rows.generation.GenerationID)
			}
		})
	}
}

func TestNativeDeliveryPreparingCandidateReadDoesNotRequireGeneration(t *testing.T) {
	for _, status := range []string{"building", "ready"} {
		t.Run(status, func(t *testing.T) {
			rows := nativeReadRowsFixture(t, "target")
			rows.candidate.Status = status
			rows.candidate.SnapshotSealID = ""
			rows.seal = nativepostgres.SnapshotSeal{}
			rows.generation = nativepostgres.DeliveryGeneration{}
			m := nativeReadModule(rows)
			recorder := httptest.NewRecorder()
			m.GetDeliveryCandidateStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.candidate.CandidateID)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var response deploymentgen.DeliveryCandidateStatusResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Status != deploymentgen.DeliveryCandidateStatusPreparing || response.ServingStateId != "" {
				t.Fatalf("preparing candidate status = %q, serving state = %q", response.Status, response.ServingStateId)
			}
		})
	}
}

func TestNativeDeliveryCandidateReadRejectsMismatchedGenerationIdentity(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	generation := rows.generation
	generation.SnapshotSealID = "0198f2c0-7c7a-7f00-8a11-000000000299"
	m := nativeReadModule(nativeReadRowsWithGeneration{nativeReadRows: rows, generationOverride: generation})
	recorder := httptest.NewRecorder()
	m.GetDeliveryCandidateStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.candidate.CandidateID)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestNativeDeliveryRetiredUnpublishedCandidateHasNoServingState(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	rows.candidate.Status = "retired"
	rows.candidate.RetiredAt = rows.candidate.QualifiedAt.Add(time.Minute)
	m := nativeReadModule(nativeReadRowsWithoutGeneration{nativeReadRows: rows})
	recorder := httptest.NewRecorder()
	m.GetDeliveryCandidateStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.candidate.CandidateID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response deploymentgen.DeliveryCandidateStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != deploymentgen.DeliveryCandidateStatusRetired || response.ServingStateId != "" {
		t.Fatalf("retired candidate status = %q, serving state = %q", response.Status, response.ServingStateId)
	}
}

func TestNativeDeliveryReadRejectsAnotherTargetInSameProjectEnvironment(t *testing.T) {
	rows := nativeReadRowsFixture(t, "other-target")
	m := nativeReadModule(rows)
	recorder := httptest.NewRecorder()
	m.GetDeliveryPlanPreview(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.plan.PlanID)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

type nativeCollectionRows struct {
	nativeReadRows
	publications  nativepostgres.DeliveryPublicationPage
	generations   nativepostgres.DeliveryGenerationPage
	publicationIn struct {
		project, target, environment, token string
		limit                               int32
	}
	generationIn struct {
		project, target, environment, token string
		limit                               int32
	}
}

func (r *nativeCollectionRows) ListPublications(_ context.Context, project, target, environment string, limit int32, token string) (nativepostgres.DeliveryPublicationPage, error) {
	r.publicationIn.project, r.publicationIn.target, r.publicationIn.environment, r.publicationIn.limit, r.publicationIn.token = project, target, environment, limit, token
	return r.publications, nil
}

func (r *nativeCollectionRows) ListRetainedGenerations(_ context.Context, project, target, environment string, limit int32, token string) (nativepostgres.DeliveryGenerationPage, error) {
	r.generationIn.project, r.generationIn.target, r.generationIn.environment, r.generationIn.limit, r.generationIn.token = project, target, environment, limit, token
	return r.generations, nil
}

func TestNativeDeliveryCollectionsUseScopedReaderAndExistingDetailProjections(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	token := "k1.next"
	reader := &nativeCollectionRows{
		nativeReadRows: rows,
		publications:   nativepostgres.DeliveryPublicationPage{Items: []nativepostgres.DeliveryPublication{rows.publication}, NextCursor: &token},
		generations:    nativepostgres.DeliveryGenerationPage{Items: []nativepostgres.DeliveryGeneration{rows.generation}, NextCursor: &token},
	}
	m := nativeReadModule(reader)
	limit := int32(13)
	pageToken := "k1.after"

	publicationRecorder := httptest.NewRecorder()
	m.ListDeliveryPublications(publicationRecorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", &limit, &pageToken)
	if publicationRecorder.Code != http.StatusOK {
		t.Fatalf("publication status = %d, body = %s", publicationRecorder.Code, publicationRecorder.Body.String())
	}
	var publicationResponse deploymentgen.DeliveryPublicationListResponse
	if err := json.Unmarshal(publicationRecorder.Body.Bytes(), &publicationResponse); err != nil {
		t.Fatal(err)
	}
	if len(publicationResponse.Items) != 1 || publicationResponse.Items[0].Id != rows.publication.PublicationID || publicationResponse.Page.NextCursor == nil || *publicationResponse.Page.NextCursor != token {
		t.Fatalf("publication response = %#v, want existing publication evidence and cursor", publicationResponse)
	}
	if reader.publicationIn.project != "finance" || reader.publicationIn.target != "target" || reader.publicationIn.environment != "prod" || reader.publicationIn.limit != limit || reader.publicationIn.token != pageToken {
		t.Fatalf("publication reader scope = %#v, want project/target/environment/page inputs", reader.publicationIn)
	}

	generationRecorder := httptest.NewRecorder()
	m.ListRetainedDeliveryGenerations(generationRecorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", &limit, &pageToken)
	if generationRecorder.Code != http.StatusOK {
		t.Fatalf("generation status = %d, body = %s", generationRecorder.Code, generationRecorder.Body.String())
	}
	var generationResponse deploymentgen.DeliveryRetainedGenerationListResponse
	if err := json.Unmarshal(generationRecorder.Body.Bytes(), &generationResponse); err != nil {
		t.Fatal(err)
	}
	if len(generationResponse.Items) != 1 || generationResponse.Items[0].Id != rows.generation.GenerationID || generationResponse.Page.NextCursor == nil || *generationResponse.Page.NextCursor != token {
		t.Fatalf("generation response = %#v, want existing generation status and cursor", generationResponse)
	}
	if reader.generationIn.project != "finance" || reader.generationIn.target != "target" || reader.generationIn.environment != "prod" || reader.generationIn.limit != limit || reader.generationIn.token != pageToken {
		t.Fatalf("generation reader scope = %#v, want project/target/environment/page inputs", reader.generationIn)
	}
}
