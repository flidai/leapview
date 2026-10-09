package deploymentpostgres

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNativeFirstSourcePlanRequiresExplicitAuthorityBeforeInspection(t *testing.T) {
	db, _ := nativePlanPostgresDB(t)
	snapshot, artifacts := nativePlanPostgresFixture(t, createPlanTestDigest('a'), createPlanTestDigest('b'))
	source := &nativePlanSourceReader{snap: snapshot}
	inspector := &nativePlanArtifactInspector{set: artifacts}
	coordinator := nativePlanCoordinator(t, db, source, inspector)
	request := nativePlanRequest()
	request.FirstSourcePreparationID = uuid.NewString()
	_, err := coordinator.CreatePlan(t.Context(), request)
	if !errors.Is(err, deploymentmodule.ErrDeliveryInputUnavailable) {
		t.Fatalf("first-source intent without authority: %v", err)
	}
	if source.count() != 0 || inspector.count() != 0 {
		t.Fatal("unconfigured first-source intent reached source inspection")
	}
}

type firstSourcePlanTestAuthority struct {
	t                                    *testing.T
	db                                   *pgxpool.Pool
	resolveCalls, bindCalls, replayCalls int
	bindErr                              error
}

func (a *firstSourcePlanTestAuthority) Resolve(_ context.Context, request deploymentmodule.NativeDeliveryPlanRequest, target deploymentnative.DeliveryTarget, bindings deployment.CandidateConnectionRequest) ([]deployment.CandidateConnectionEvidence, error) {
	a.resolveCalls++
	if request.FirstSourcePreparationID == "" || target.ActiveGenerationID != "" || target.ActivePublicationID != "" || bindings.Actor != request.PrincipalID {
		a.t.Fatal("incorrect first-source resolution scope")
	}
	return nil, nil
}

func (a *firstSourcePlanTestAuthority) BindTx(ctx context.Context, tx deploymentnative.Tx, request deploymentmodule.NativeDeliveryPlanRequest, target deploymentnative.DeliveryTarget, plan deployment.DeliveryPlan) error {
	a.bindCalls++
	if plan.ActorID != request.PrincipalID || plan.SourceOwnerID != request.SourceOwnerID || plan.BaseGenerationID != "" || plan.BaseTargetRevision != target.TargetRevision || !plan.Governance.RequiresApproval {
		a.t.Fatal("first-source plan lost normal publisher, source owner, target or approval identity")
	}
	// Both the native plan and its preparation link must be invisible outside
	// this caller-owned transaction until all consequences succeed.
	var inTx, outside int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM delivery.delivery_plan WHERE plan_id=$1::uuid`, plan.ID).Scan(&inTx); err != nil || inTx != 1 {
		a.t.Fatalf("native plan not in link transaction: %d %v", inTx, err)
	}
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM delivery.delivery_plan WHERE plan_id=$1::uuid`, plan.ID).Scan(&outside); err != nil || outside != 0 {
		a.t.Fatalf("native plan committed before preparation link: %d %v", outside, err)
	}
	digest, err := deploymentmodule.NativeDeliveryPlanRequestDigest(request)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO first_source_test_link(preparation,plan_id,request_digest,binding_digest) VALUES ($1,$2,$3,$4)`, request.FirstSourcePreparationID, plan.ID, digest, plan.Execution.BindingDigest)
	if err != nil {
		return err
	}
	return a.bindErr
}

func (a *firstSourcePlanTestAuthority) ValidateReplayTx(ctx context.Context, tx deploymentnative.Tx, request deploymentmodule.NativeDeliveryPlanRequest, plan deployment.DeliveryPlan) error {
	a.replayCalls++
	digest, err := deploymentmodule.NativeDeliveryPlanRequestDigest(request)
	if err != nil {
		return err
	}
	var matches bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM first_source_test_link WHERE preparation=$1 AND plan_id=$2 AND request_digest=$3 AND binding_digest=$4)`, request.FirstSourcePreparationID, plan.ID, digest, plan.Execution.BindingDigest).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return deployment.ErrDeliveryConflict
	}
	return nil
}

func firstSourcePlanFixture(t *testing.T) (*NativeCreatePlanCoordinator, *firstSourcePlanTestAuthority, deploymentmodule.NativeDeliveryPlanRequest) {
	t.Helper()
	db, _ := nativePlanPostgresDB(t)
	_, err := db.Exec(t.Context(), `CREATE TABLE first_source_test_link(preparation text PRIMARY KEY,plan_id text NOT NULL,request_digest text NOT NULL,binding_digest text NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, artifacts := nativePlanPostgresFixture(t, createPlanTestDigest('a'), createPlanTestDigest('b'))
	coordinator := nativePlanCoordinator(t, db, &nativePlanSourceReader{snap: snapshot}, &nativePlanArtifactInspector{set: artifacts})
	coordinator.policy.RequiresApproval = true
	authority := &firstSourcePlanTestAuthority{t: t, db: db}
	coordinator.firstSource = authority
	request := nativePlanRequest()
	request.FirstSourcePreparationID = uuid.NewString()
	return coordinator, authority, request
}

func TestNativeFirstSourcePlanAtomicallyLinksAndChecksExactReplay(t *testing.T) {
	coordinator, authority, request := firstSourcePlanFixture(t)
	plan, err := coordinator.CreatePlan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := coordinator.CreatePlan(t.Context(), request)
	if err != nil || replayed.ID != plan.ID {
		t.Fatalf("exact replay: %v", err)
	}
	if authority.resolveCalls != 1 || authority.bindCalls != 1 || authority.replayCalls != 1 {
		t.Fatal("replay renewed or rebound the first-source preparation")
	}
	if _, err := authority.db.Exec(t.Context(), `DELETE FROM first_source_test_link`); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.CreatePlan(t.Context(), request); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("replay accepted missing preparation link: %v", err)
	}
}

func TestNativeFirstSourcePlanRollsBackLinkAndNativeConsequences(t *testing.T) {
	for _, failure := range []string{"link-authority", "later-audit"} {
		t.Run(failure, func(t *testing.T) {
			coordinator, authority, request := firstSourcePlanFixture(t)
			if failure == "link-authority" {
				authority.bindErr = errors.New("operator grant revoked")
			} else if _, err := authority.db.Exec(t.Context(), `ALTER TABLE audit.audit_event ADD CONSTRAINT reject_first_source_native_audit CHECK (action <> 'delivery.plan.created')`); err != nil {
				t.Fatal(err)
			}
			if _, err := coordinator.CreatePlan(t.Context(), request); err == nil {
				t.Fatal("failed authority/audit accepted first-source plan")
			}
			if authority.bindCalls != 1 {
				t.Fatal("fault did not reach link transaction")
			}
			for _, query := range []string{`SELECT count(*) FROM first_source_test_link`, `SELECT count(*) FROM delivery.delivery_plan`, `SELECT count(*) FROM delivery.delivery_target`} {
				var count int
				if err := authority.db.QueryRow(t.Context(), query).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial commit: %d %v", count, err)
				}
			}
		})
	}
}

func TestNativeFirstSourcePlanRejectsNonInitialIntent(t *testing.T) {
	for _, id := range []string{"not-a-uuid", uuid.Nil.String(), " " + uuid.NewString()} {
		request := nativePlanRequest()
		request.FirstSourcePreparationID = id
		if err := validateNativeCreatePlanRequest(request); !errors.Is(err, deployment.ErrDeliveryInvalid) {
			t.Fatalf("invalid preparation identity: %v", err)
		}
	}
	request := nativePlanRequest()
	request.FirstSourcePreparationID = uuid.NewString()
	request.Operation = string(deployment.DeliveryOperationBindingChange)
	if err := validateNativeCreatePlanRequest(request); !errors.Is(err, deployment.ErrDeliveryInvalid) {
		t.Fatalf("binding-change preparation: %v", err)
	}
	request.Operation = string(deployment.DeliveryOperationCodeChange)
	request.PipelinePlan = &projectpipelineplan.Plan{}
	if err := validateNativeCreatePlanRequest(request); !errors.Is(err, deployment.ErrDeliveryInvalid) {
		t.Fatalf("pipeline preparation: %v", err)
	}
	request.PipelinePlan = nil
	authority := &firstSourcePlanTestAuthority{t: t}
	coordinator := &NativeCreatePlanCoordinator{firstSource: authority}
	for _, target := range []deploymentnative.DeliveryTarget{{ActiveGenerationID: "generation"}, {ActivePublicationID: "publication"}} {
		if _, _, err := coordinator.resolvePlanBindings(t.Context(), request, target, deployment.CandidateConnectionRequest{}); !errors.Is(err, deployment.ErrDeliveryConflict) {
			t.Fatalf("active target preparation: %v", err)
		}
	}
	if authority.resolveCalls != 0 {
		t.Fatal("active target reached first-source authority")
	}
}
