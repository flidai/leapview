package deploymentpostgres

import (
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/runtimefactory"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/google/uuid"
)

func TestNativeCreatePlanPostgresBindsPortableSourceToIndependentEnvironmentTargets(t *testing.T) {
	snapshot, artifacts := nativePlanPostgresFixture(t, createPlanTestDigest('a'), createPlanTestDigest('b'))
	// The fixture starts with a convenient synthetic source digest. Use the
	// actual canonical source-bundle digest for this cross-target contract.
	snapshot.ArtifactDigest = snapshot.ProjectDigest
	artifacts.Artifact.SourceDigest = snapshot.ProjectDigest
	artifacts.Generation.Connections = []release.CandidateConnectionRequirement{{
		ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres",
	}}

	type environmentPlan struct {
		environment string
		plan        deploymentmodule.NativeDeliveryPlan
		rich        deployment.DeliveryPlan
	}
	policies := map[string]runtimefactory.CandidateDeliveryPolicy{
		"dev":     {ApprovalPolicyRevision: 1, RollbackClass: deployment.DeliveryRollbackSafe, RetentionWindow: "1h"},
		"staging": {ApprovalPolicyRevision: 2, RollbackClass: deployment.DeliveryServingSafe, RetentionWindow: "2h"},
		"prod":    {RequiresApproval: true, ApprovalPolicyRevision: 3, RollbackClass: deployment.DeliveryNonReversible, RetentionWindow: "3h"},
	}

	planned := make([]environmentPlan, 0, 3)
	for index, environment := range []string{"dev", "staging", "prod"} {
		targetID := "target_native_" + environment
		targetRevision := int64(2 + index*2)
		policy := policies[environment]
		db, repository := nativePlanPostgresDB(t)
		if _, err := repository.ClaimProject(t.Context(), deployment.ProjectClaimInput{
			ProjectID: "project_native_plan", Environment: servingstate.Environment(environment), ClaimedBy: "bootstrap-admin",
			ClaimedAt: time.Date(2026, 8, 30, 11, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("claim %s target: %v", environment, err)
		}
		if _, err := repository.CreateTarget(t.Context(), deploymentnative.TargetInput{
			TargetID: targetID, ProjectID: "project_native_plan", Environment: environment, TargetRevision: targetRevision,
		}); err != nil {
			t.Fatalf("create %s target: %v", environment, err)
		}

		source := &nativePlanSourceReader{snap: snapshot}
		inspector := &nativePlanArtifactInspector{set: artifacts}
		seedNativePlanAuthorizationPolicy(t, db, inspector, targetID, environment)
		coord := newNativePlanCoordinator(t, db, source, inspector)
		// newNativePlanCoordinator is shared with the single-target tests and
		// intentionally defaults to prod. Keep this adaptation local to the
		// multi-target contract rather than changing that shared helper.
		coord.targetID, coord.environment = targetID, environment

		bindingEvidence := []deployment.CandidateConnectionEvidence{{
			BindingID: "binding_warehouse_" + environment, ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres",
			Revision: int64(10 + index), ProviderVersion: "provider:v" + string(rune('3'+index)), EndpointConfigHash: createPlanTestDigest(byte('7' + index)),
		}}
		bindingLeases := &nativeConnectionLeases{evidence: bindingEvidence}
		bindingResolver := &nativeConnectionLeaser{leases: bindingLeases}
		coord.bindingEvidence = bindingResolver
		policyCalls := 0
		coord.policyResolver = func(operation deployment.DeliveryOperationKind) (runtimefactory.CandidateDeliveryPolicy, error) {
			policyCalls++
			if operation != deployment.DeliveryOperationCodeChange {
				t.Fatalf("%s policy operation = %q, want code change", environment, operation)
			}
			return policy, nil
		}

		request := nativePlanRequest()
		request.TargetID, request.Environment = targetID, environment
		request.SourceDigest, request.SourceAttestationDigest = snapshot.ArtifactDigest, snapshot.SourceAttestationDigest
		request.IdempotencyKey = "native-plan-" + environment
		plan, err := coord.CreatePlan(t.Context(), request)
		if err != nil {
			t.Fatalf("create %s plan: %v", environment, err)
		}
		if source.count() != 1 || inspector.count() != 1 || policyCalls != 1 || bindingResolver.resolveCalls != 1 || bindingResolver.calls != 0 || bindingLeases.closeCalls != 0 {
			t.Fatalf("%s source/artifact/policy/resolve/acquire/close = %d/%d/%d/%d/%d/%d, want 1/1/1/1/0/0", environment, source.count(), inspector.count(), policyCalls, bindingResolver.resolveCalls, bindingResolver.calls, bindingLeases.closeCalls)
		}

		stored, err := repository.Plan(t.Context(), plan.ID.String())
		if err != nil {
			t.Fatalf("read %s plan: %v", environment, err)
		}
		rich, err := stored.RichPlan()
		if err != nil {
			t.Fatalf("decode %s plan: %v", environment, err)
		}
		target, err := repository.Target(t.Context(), targetID)
		if err != nil {
			t.Fatalf("read %s target: %v", environment, err)
		}
		if plan.ProjectID != snapshot.ProjectID || plan.SourceDigest != snapshot.ArtifactDigest || plan.TargetID != targetID || plan.Environment != environment || plan.BaseTargetRevision != targetRevision || plan.BaseGenerationID != uuid.Nil {
			t.Fatalf("%s plan scope = %+v, want project=%s source=%s target=%s environment=%s base revision=%d with no active base", environment, plan, snapshot.ProjectID, snapshot.ArtifactDigest, targetID, environment, targetRevision)
		}
		if rich.Execution.BindingDigest != mustBindingFingerprint(t, bindingEvidence) {
			t.Fatalf("%s binding digest = %q, want independently resolved evidence", environment, rich.Execution.BindingDigest)
		}
		if rich.Governance.ApprovalPolicyRevision != policy.ApprovalPolicyRevision || rich.Governance.RequiresApproval != policy.RequiresApproval || rich.Evidence.Rollback.Class != policy.RollbackClass || rich.Evidence.Rollback.RetentionWindow != policy.RetentionWindow {
			t.Fatalf("%s policy evidence = governance=%+v rollback=%+v, want %+v", environment, rich.Governance, rich.Evidence.Rollback, policy)
		}
		if target.ProjectID != plan.ProjectID.String() || target.Environment != environment || target.TargetRevision != targetRevision || target.ActiveGenerationID != "" || target.ActivePublicationID != "" {
			t.Fatalf("%s target state = %+v, want independent target revision %d without active pointer", environment, target, targetRevision)
		}
		var candidates, approvals int
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_candidate`).Scan(&candidates); err != nil {
			t.Fatalf("count %s candidates: %v", environment, err)
		}
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_approval_request`).Scan(&approvals); err != nil {
			t.Fatalf("count %s approvals: %v", environment, err)
		}
		if candidates != 0 || approvals != 0 {
			t.Fatalf("%s copied candidate/approval state = %d/%d, want 0/0", environment, candidates, approvals)
		}
		planned = append(planned, environmentPlan{environment: environment, plan: plan, rich: rich})
	}

	planDigests := make(map[string]string, len(planned))
	bindingDigests := make(map[string]string, len(planned))
	for _, item := range planned {
		if previous, exists := planDigests[item.plan.PlanDigest]; exists {
			t.Fatalf("%s reused %s plan digest %q", item.environment, previous, item.plan.PlanDigest)
		}
		planDigests[item.plan.PlanDigest] = item.environment
		if previous, exists := bindingDigests[item.rich.Execution.BindingDigest]; exists {
			t.Fatalf("%s reused %s binding digest %q", item.environment, previous, item.rich.Execution.BindingDigest)
		}
		bindingDigests[item.rich.Execution.BindingDigest] = item.environment
	}
}

func TestNativeCreatePlanPostgresRejectsForeignScopeBeforeSourceIO(t *testing.T) {
	snapshot, artifacts := nativePlanPostgresFixture(t, createPlanTestDigest('a'), createPlanTestDigest('b'))
	for name, test := range map[string]struct {
		mutate func(*deploymentmodule.NativeDeliveryPlanRequest)
		want   error
	}{
		"target":      {mutate: func(request *deploymentmodule.NativeDeliveryPlanRequest) { request.TargetID = "target_foreign" }, want: deployment.ErrDeliveryConflict},
		"environment": {mutate: func(request *deploymentmodule.NativeDeliveryPlanRequest) { request.Environment = "staging" }, want: deployment.ErrDeliveryConflict},
		"project":     {mutate: func(request *deploymentmodule.NativeDeliveryPlanRequest) { request.ProjectID = "project_foreign" }, want: deployment.ErrProjectClaimConflict},
	} {
		t.Run(name, func(t *testing.T) {
			db, _ := nativePlanPostgresDB(t)
			source := &nativePlanSourceReader{snap: snapshot}
			inspector := &nativePlanArtifactInspector{set: artifacts}
			coord := nativePlanCoordinator(t, db, source, inspector)
			bindingResolver := &nativeConnectionLeaser{leases: &nativeConnectionLeases{evidence: []deployment.CandidateConnectionEvidence{{
				BindingID: "binding_warehouse", ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres", Revision: 1, ProviderVersion: "provider:v3", EndpointConfigHash: createPlanTestDigest('7'),
			}}}}
			coord.bindingEvidence = bindingResolver
			policyCalls := 0
			coord.policyResolver = func(deployment.DeliveryOperationKind) (runtimefactory.CandidateDeliveryPolicy, error) {
				policyCalls++
				return runtimefactory.CandidateDeliveryPolicy{ApprovalPolicyRevision: runtimefactory.CurrentApprovalPolicyRevision}, nil
			}
			request := nativePlanRequest()
			test.mutate(&request)

			_, err := coord.CreatePlan(t.Context(), request)
			if !errors.Is(err, test.want) {
				t.Fatalf("CreatePlan error = %v, want %v", err, test.want)
			}
			if source.count() != 0 || inspector.count() != 0 || bindingResolver.resolveCalls != 0 || bindingResolver.calls != 0 || policyCalls != 0 {
				t.Fatalf("foreign scope reached source/artifact/resolve/acquire/policy = %d/%d/%d/%d/%d, want 0/0/0/0/0", source.count(), inspector.count(), bindingResolver.resolveCalls, bindingResolver.calls, policyCalls)
			}
			var plans int
			if err := db.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_plan`).Scan(&plans); err != nil {
				t.Fatal(err)
			}
			if plans != 0 {
				t.Fatalf("foreign %s scope persisted %d plans, want 0", name, plans)
			}
		})
	}
}

func mustBindingFingerprint(t *testing.T, evidence []deployment.CandidateConnectionEvidence) string {
	t.Helper()
	digest, err := deployment.BindingFingerprint(evidence)
	if err != nil {
		t.Fatalf("binding fingerprint: %v", err)
	}
	return digest
}
