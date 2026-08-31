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
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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
	return nativepostgres.DeliveryOperatorSnapshot{ProjectID: "finance", Environment: "prod", TargetID: "target", TargetRevision: 3, ActiveGenerationID: r.generation.GenerationID}, nil
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
		Governance: deployment.DeliveryGovernance{PolicyDigest: nativeReadDigest('4'), AuthorizationDigest: nativeReadDigest('5'), QualificationDigest: nativeReadDigest('6'), ExpiresAt: now.Add(time.Hour)},
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
	planRow := nativepostgres.DeliveryPlan{PlanID: plan.ID, TargetID: target, PlanDigest: plan.Digest, CompiledConfigDigest: plan.Execution.ConfigDigest, SecurityDomainFingerprint: plan.Governance.AuthorizationDigest, ArtifactDigest: plan.ServingArtifactDigest, QualificationDigest: plan.Governance.QualificationDigest, QualificationRequired: true, PlanDocument: document, Evidence: evidence, CreatedAt: now}
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

func TestNativeDeliveryReadRejectsAnotherTargetInSameProjectEnvironment(t *testing.T) {
	rows := nativeReadRowsFixture(t, "other-target")
	m := nativeReadModule(rows)
	recorder := httptest.NewRecorder()
	m.GetDeliveryPlanPreview(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", rows.plan.PlanID)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
