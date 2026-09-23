package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

func validateCanonicalConnectionBindingScope(
	scope analyticsmodule.ConnectionBindingScope,
	activeProjectID projectgraph.ResourceID,
	configuredEnvironment string,
) error {
	if err := scope.ProjectID.Validate(); err != nil {
		return err
	}
	if scope.ProjectID != activeProjectID {
		return fmt.Errorf("connection binding project %q is not the active project %q", scope.ProjectID, activeProjectID)
	}
	if scope.Environment == "" || scope.Environment != strings.TrimSpace(scope.Environment) {
		return errors.New("connection binding environment is required")
	}
	if scope.Environment != configuredEnvironment {
		return fmt.Errorf("connection binding environment %q is not the configured environment %q", scope.Environment, configuredEnvironment)
	}
	return nil
}

func writeProductCommandFailure(ctx context.Context, w http.ResponseWriter, r *http.Request, operationID string, cause error) {
	if contracts, ok := apiaggregate.GetAPIGenCommandFailureContracts(operationID); ok && apigenfailure.ValidateContracts(contracts) == nil {
		if contract, matched := apigenfailure.Match(contracts, cause); matched {
			apitransport.WriteAPIGenFailure(ctx, w, r, nil, apitransport.APIGenFailure{
				OperationID: operationID, Kind: contract.Kind, StatusCode: contract.StatusCode,
				Code: contract.Code, PublicDetail: contract.PublicDetail, Cause: cause,
			})
			return
		}
	}
	apitransport.WriteAPIGenFailure(ctx, w, r, nil, apitransport.APIGenFailure{
		OperationID: operationID, Kind: "handler", StatusCode: http.StatusInternalServerError,
		Code: "INTERNAL_ERROR", PublicDetail: "The request could not be completed.", Cause: cause,
	})
}

func hasActiveBootstrapServingState(
	ctx context.Context,
	runtimeHost canonicalRuntimeHost,
	states servingStateRepository,
	environment string,
	targets deliveryTargetReader,
	targetID string,
	projectID string,
) (bool, error) {
	// The delivery target pointer is authoritative for sealed serving. Once a
	// target row exists, an active generation there closes bootstrap even when
	// the legacy serving-state scope table has not been updated (or is stale).
	if targets != nil && strings.TrimSpace(targetID) != "" {
		target, err := targets.DeliveryTargetRevision(ctx, targetID)
		if err == nil {
			if target.TargetID != targetID || target.ProjectID != strings.TrimSpace(projectID) || strings.TrimSpace(target.Environment) != strings.TrimSpace(environment) {
				return false, fmt.Errorf("active delivery target scope does not match %q/%q/%q", targetID, projectID, environment)
			}
			activeGenerationID := strings.TrimSpace(target.ActiveGenerationID)
			if activeGenerationID == "" {
				return false, nil
			}
			// A legacy native generation can carry the canonical empty access
			// document because target-owned policy revisions did not yet exist. If
			// that exact generation is already loaded, retain the narrow claim- and
			// platform-admin-gated bootstrap path until an explicit project role is
			// sealed into a successor generation. An unavailable runtime remains
			// closed so a warm-up or infrastructure failure cannot broaden access.
			if runtimeHost != nil {
				lease, acquireErr := runtimeHost.Acquire(ctx)
				if acquireErr == nil {
					if lease == nil {
						return false, errors.New("runtime host returned a nil lease")
					}
					defer lease.Release()
					identity := lease.Identity()
					if identity.ProjectID.String() != target.ProjectID || identity.Environment != target.Environment || identity.GenerationID != activeGenerationID {
						return false, errors.New("active runtime identity does not match delivery target")
					}
					authorized, ok := lease.(interface {
						AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
					})
					if !ok {
						return false, errors.New("active runtime lease does not expose authorization snapshot")
					}
					snapshot := authorized.AuthorizationSnapshot()
					if snapshot.Identity() != identity {
						return false, errors.New("active runtime authorization snapshot identity does not match lease")
					}
					if err := snapshot.ValidateBound(); err != nil {
						return false, err
					}
					if len(snapshot.RoleBindings()) == 0 && len(snapshot.Grants()) == 0 && len(snapshot.DataPolicies()) == 0 {
						return false, nil
					}
				}
			}
			return true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, deployment.ErrNotFound) {
			return false, fmt.Errorf("read active delivery target: %w", err)
		}
		// A configured canonical target authority is definitive. A missing row
		// means bootstrap is still open; never resurrect stale legacy scope state.
		return false, nil
	}
	if states == nil {
		return false, errors.New("serving-state repository is unavailable")
	}
	scopes, err := states.ListActiveScopes(ctx)
	if err != nil {
		return false, fmt.Errorf("read active serving scopes: %w", err)
	}
	env := servingstatemodule.Environment(strings.TrimSpace(environment))
	activeCount := 0
	for _, scope := range scopes {
		if scope.Environment != env {
			continue
		}
		if err := scope.ProjectID.Validate(); err != nil {
			return false, fmt.Errorf("active serving project identity is invalid: %w", err)
		}
		activeCount++
		if activeCount > 1 {
			return false, fmt.Errorf("active serving scopes contain multiple projects for environment %q", env)
		}
	}
	if activeCount > 0 {
		return true, nil
	}
	// A focused test/profile assembly may omit the canonical target authority;
	// runnable application composition never reaches this fallback.
	return false, nil
}

// hasActiveBootstrapRuntime reports whether the process-local immutable
// serving generation is ready to authorize requests. The durable delivery
// pointer may advance before runtime cutover, so deployment status reads use
// this check to distinguish that marker-to-runtime warm-up window from the
// normal active snapshot path. An unavailable runtime is intentionally
// treated as not ready here; the caller then applies the exact durable claim
// bootstrap policy, which remains fail-closed for missing or mismatched
// claims.
func hasActiveBootstrapRuntime(ctx context.Context, runtimeHost canonicalRuntimeHost) (bool, error) {
	if runtimeHost == nil {
		return false, nil
	}
	lease, err := runtimeHost.Acquire(ctx)
	if err != nil {
		return false, nil
	}
	if lease == nil {
		return false, errors.New("runtime host returned a nil lease")
	}
	lease.Release()
	return true, nil
}

// bootstrapAPIGenDecision is deliberately a read-only seam. It distinguishes
// a typed empty active-generation pointer from a serving-state store failure,
// then evaluates only the durable singleton claim and the explicit candidate
// or managed-data operation allowlist. Credential role/capability evidence is
// enforced by the APIGen wrapper and by deployment's arm/worker revalidator,
// never here.
func bootstrapAPIGenDecision(
	ctx context.Context,
	runtimeHost canonicalRuntimeHost,
	states servingStateRepository,
	claims deploymentmodule.ProjectClaimReader,
	environment, operationID string,
	projectID projectgraph.ResourceID,
	targets deliveryTargetReader,
	targetID string,
) (accessmodule.APIGenBootstrapDecision, error) {
	// Deployment status/event reads and delivery plan resolution are control-plane
	// operations. Their project-scoped RESOURCE_READ contracts cannot be evaluated
	// against the project graph (projects intentionally only support PROJECT_ADMIN),
	// and the sealed delivery pointer advances before the in-process runtime cutover.
	// Candidate-source authoring normally follows the active snapshot too, except
	// for the canonical-empty legacy generation that must stage its policy-bearing
	// successor through the durable, exact-claim bootstrap path.
	if bootstrapManagedDataOperation(operationID) {
		active, err := hasActiveBootstrapServingState(ctx, runtimeHost, states, environment, targets, targetID, projectID.String())
		if err != nil {
			return accessmodule.APIGenBootstrapDecision{}, err
		}
		if active {
			allowed, claimErr := bootstrapClaimAllows(ctx, claims, environment, operationID, projectID)
			if claimErr != nil {
				return accessmodule.APIGenBootstrapDecision{}, claimErr
			}
			// Existing resources remain entirely governed by the active snapshot.
			// This bit is consumed only after that snapshot proves the exact
			// successor connection is absent.
			return accessmodule.APIGenBootstrapDecision{Handled: false, AllowMissingResource: allowed}, nil
		}
	} else if bootstrapCandidateSourceOperation(operationID) {
		// A legacy active generation with no authorization content still needs
		// these authoring operations to stage the policy-bearing successor that
		// closes bootstrap. During runtime warm-up, keep using the durable exact
		// claim as before; once ready, non-empty active snapshots are authoritative.
		runtimeReady, err := hasActiveBootstrapRuntime(ctx, runtimeHost)
		if err != nil {
			return accessmodule.APIGenBootstrapDecision{}, err
		}
		if runtimeReady {
			active, err := hasActiveBootstrapServingState(ctx, runtimeHost, states, environment, targets, targetID, projectID.String())
			if err != nil {
				return accessmodule.APIGenBootstrapDecision{}, err
			}
			if active {
				return accessmodule.APIGenBootstrapDecision{Handled: false}, nil
			}
		}
	} else if bootstrapControlPlaneOperation(operationID) {
		active, err := hasActiveBootstrapRuntime(ctx, runtimeHost)
		if err != nil {
			return accessmodule.APIGenBootstrapDecision{}, err
		}
		if active {
			return accessmodule.APIGenBootstrapDecision{Handled: false}, nil
		}
	} else {
		active, err := hasActiveBootstrapServingState(ctx, runtimeHost, states, environment, targets, targetID, projectID.String())
		if err != nil {
			return accessmodule.APIGenBootstrapDecision{}, err
		}
		if active {
			return accessmodule.APIGenBootstrapDecision{Handled: false}, nil
		}
	}
	allowed, err := bootstrapClaimAllows(ctx, claims, environment, operationID, projectID)
	if err != nil {
		return accessmodule.APIGenBootstrapDecision{}, err
	}
	return accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: allowed}, nil
}

func bootstrapClaimAllows(ctx context.Context, claims deploymentmodule.ProjectClaimReader, environment, operationID string, projectID projectgraph.ResourceID) (bool, error) {
	if err := projectID.Validate(); err != nil || projectID.String() != strings.TrimSpace(projectID.String()) {
		return false, nil
	}
	if claims == nil {
		return false, errors.New("project claim repository is unavailable")
	}
	claim, err := claims.GetProjectClaim(ctx)
	if errors.Is(err, deployment.ErrProjectClaimNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read bootstrap project claim: %w", err)
	}
	if claim.ProjectID != projectID || claim.Environment != servingstatemodule.Environment(strings.TrimSpace(environment)) {
		return false, nil
	}
	return bootstrapOperationAllowed(operationID), nil
}

func bootstrapManagedDataOperation(operationID string) bool {
	switch operationID {
	case "createManagedDataUploadSession", "getManagedDataUploadSession", "cancelManagedDataUploadSession", "finalizeManagedDataUploadSession",
		"createManagedDataS3MultipartUpload", "signManagedDataS3MultipartPart", "completeManagedDataS3MultipartUpload", "abortManagedDataS3MultipartUpload",
		"managedDataTusTransport":
		return true
	default:
		return false
	}
}

func bootstrapControlPlaneOperation(operationID string) bool {
	switch operationID {
	case "getDeliveryCandidateStatus", "getDeliveryPlanPreview":
		return true
	default:
		return false
	}
}

func bootstrapCandidateSourceOperation(operationID string) bool {
	switch operationID {
	case "planProjectCandidateSynchronization", "uploadProjectCandidateSourceBlob", "retainProjectCandidateSource":
		return true
	default:
		return false
	}
}

func bootstrapOperationAllowed(operationID string) bool {
	switch operationID {
	case "planProjectCandidateSynchronization", "uploadProjectCandidateSourceBlob", "retainProjectCandidateSource", "createDeliveryPlan", "buildDeliveryPlan", "publishDeliveryCandidate", "getDeliveryCandidateStatus", "getDeliveryPlanPreview", "requestDeliveryPublicationApproval", "approveDeliveryPublicationApproval",
		"exchangeProjectClaimPublisher", "acknowledgeProjectClaimPublisher",
		"createManagedDataUploadSession", "getManagedDataUploadSession", "cancelManagedDataUploadSession", "finalizeManagedDataUploadSession",
		"createManagedDataS3MultipartUpload", "signManagedDataS3MultipartPart", "completeManagedDataS3MultipartUpload", "abortManagedDataS3MultipartUpload", "createProjectRoleBinding", "listProjectRoleBindings":
		return true
	case "managedDataTusTransport":
		return true
	default:
		return false
	}
}

// deliveryProjectAllowsTypedOperation evaluates the generated delivery action
// and its prerequisite closure against the active generation's typed
// principal/group assignments. Legacy capabilities cannot stand in for a
// release-operator assignment on these qualified operations.
func deliveryProjectAllowsTypedOperation(
	snapshot accesssnapshot.AuthorizationSnapshot,
	subjects []access.SubjectRef,
	projectID projectgraph.ResourceID,
	operationID string,
	operations map[string]accessmodule.APIGenOperationContract,
) (bool, error) {
	contract, ok := operations[operationID]
	if !ok || contract.Resolver != string(access.TypedOperationResolverDelivery) {
		return false, fmt.Errorf("delivery operation %q has no typed delivery requirement", operationID)
	}
	requirement, err := access.NewTypedOperationRequirementService().New(access.Action(contract.Action), contract.Resolver)
	if err != nil {
		return false, err
	}
	required, err := requirement.ResolvePairs(projectID)
	if err != nil {
		return false, err
	}
	granted, err := snapshot.EffectiveTypedPermissions(subjects)
	if err != nil {
		return false, err
	}
	for _, pair := range required {
		if !access.PermissionSetAllows(granted, pair) {
			return false, nil
		}
	}
	return true, nil
}

func deliveryApprovalDecisionOperation(operationID string) bool {
	switch operationID {
	case "approveDeliveryPublicationApproval", "denyDeliveryPublicationApproval", "revokeDeliveryPublicationApproval":
		return true
	default:
		return false
	}
}

type deliveryAuthorizationImpact struct {
	Existing     []access.ResourceRef
	HasAdditions bool
}

func deliveryAuthorizationResources(plan deployment.DeliveryPlan) (deliveryAuthorizationImpact, error) {
	added := plan.Evidence.GraphImpact.Added
	impact := append([]deployment.DeliveryImpactResource{}, plan.Evidence.GraphImpact.Removed...)
	impact = append(impact, plan.Evidence.GraphImpact.DirectlyModified...)
	impact = append(impact, plan.Evidence.GraphImpact.IndirectlyAffected...)
	resources := make([]access.ResourceRef, 0, len(impact))
	seen := make(map[string]struct{}, len(added)+len(impact))
	parse := func(item deployment.DeliveryImpactResource) (access.ResourceRef, error) {
		id, err := projectgraph.NewResourceID(strings.TrimSpace(item.ID))
		if err != nil {
			return access.ResourceRef{}, err
		}
		kind, err := projectgraph.ParseKind(strings.TrimSpace(item.Kind))
		if err != nil {
			return access.ResourceRef{}, err
		}
		resource, err := access.NewResourceRef(id, kind)
		if err != nil {
			return access.ResourceRef{}, err
		}
		return resource, nil
	}
	for _, item := range added {
		resource, err := parse(item)
		if err != nil {
			return deliveryAuthorizationImpact{}, err
		}
		seen[resource.ID().String()+"\x00"+string(resource.Kind())] = struct{}{}
	}
	for _, item := range impact {
		resource, err := parse(item)
		if err != nil {
			return deliveryAuthorizationImpact{}, err
		}
		key := resource.ID().String() + "\x00" + string(resource.Kind())
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		resources = append(resources, resource)
	}
	return deliveryAuthorizationImpact{Existing: resources, HasAdditions: len(added) > 0}, nil
}

func deliverySnapshotAllows(
	snapshot accesssnapshot.AuthorizationSnapshot,
	subjects []access.SubjectRef,
	projectID projectgraph.ResourceID,
	resources []access.ResourceRef,
	actionFor func(access.ResourceRef) (access.Action, bool),
) (bool, error) {
	if len(resources) == 0 || actionFor == nil {
		return false, nil
	}
	granted, err := snapshot.EffectiveTypedPermissions(subjects)
	if err != nil {
		return false, err
	}
	for _, resource := range resources {
		action, mapped := actionFor(resource)
		if !mapped {
			return false, nil
		}
		pair, err := typedPermissionPair(action, projectID, resource)
		if err != nil || !access.PermissionSetAllows(granted, pair) {
			return false, nil
		}
	}
	return true, nil
}

func deliveryAuthorizationImpactAllows(
	snapshot accesssnapshot.AuthorizationSnapshot,
	subjects []access.SubjectRef,
	projectID projectgraph.ResourceID,
	impact deliveryAuthorizationImpact,
	addedAction access.Action,
	resourceAction func(access.ResourceRef) (access.Action, bool),
) (bool, error) {
	if impact.HasAdditions {
		project, err := access.NewResourceRef(projectID, projectgraph.KindProjectNamespace)
		if err != nil {
			return false, err
		}
		if allowed, err := deliverySnapshotAllows(snapshot, subjects, projectID, []access.ResourceRef{project}, func(access.ResourceRef) (access.Action, bool) {
			return addedAction, addedAction != ""
		}); err != nil || !allowed {
			return false, err
		}
	}
	if len(impact.Existing) == 0 {
		return impact.HasAdditions, nil
	}
	return deliverySnapshotAllows(snapshot, subjects, projectID, impact.Existing, resourceAction)
}

func publicationTypedAction(resource access.ResourceRef, capability access.Capability) (access.Action, bool) {
	var action access.Action
	switch resource.Kind() {
	case projectgraph.KindProjectNamespace:
		switch capability {
		case access.CapabilityProjectAdmin:
			action = access.ActionProjectAccessManage
		case access.CapabilityResourceRead:
			action = access.ActionDeliveryRead
		case access.CapabilityResourceUse:
			action = access.ActionDeliveryBuild
		case access.CapabilityResourceEdit:
			action = access.ActionDeliveryPlan
		case access.CapabilityResourcePublish:
			action = access.ActionDeliveryPublish
		case access.CapabilityResourceManage:
			action = access.ActionDeliveryApprove
		}
	case projectgraph.KindDashboard:
		switch capability {
		case access.CapabilityResourceRead, access.CapabilityResourceUse:
			action = access.ActionDashboardRead
		case access.CapabilityResourceEdit:
			action = access.ActionDashboardUpdate
		case access.CapabilityResourceManage:
			action = access.ActionDashboardDelete
		case access.CapabilityResourcePublish:
			action = access.ActionDashboardPublish
		}
	case projectgraph.KindConnection:
		switch capability {
		case access.CapabilityResourceRead:
			action = access.ActionConnectionRead
		case access.CapabilityResourceUse:
			action = access.ActionConnectionUse
		case access.CapabilityResourceEdit, access.CapabilityResourceManage, access.CapabilityResourcePublish:
			action = access.ActionConnectionManage
		}
	case projectgraph.KindPipeline:
		switch capability {
		case access.CapabilityResourceRead:
			action = access.ActionPipelineRead
		case access.CapabilityResourceUse:
			action = access.ActionPipelineRun
		case access.CapabilityResourceEdit:
			action = access.ActionPipelineUpdate
		case access.CapabilityResourceManage:
			action = access.ActionPipelineDelete
		case access.CapabilityResourcePublish:
			action = access.ActionPipelineUpdate
		}
	case projectgraph.KindSemanticModel:
		switch capability {
		case access.CapabilityResourceRead, access.CapabilityResourceUse:
			action = access.ActionSemanticRead
		case access.CapabilityResourceEdit:
			action = access.ActionSemanticUpdate
		case access.CapabilityResourceManage:
			action = access.ActionSemanticDelete
		case access.CapabilityResourcePublish:
			action = access.ActionSemanticUpdate
		}
	case projectgraph.KindSource:
		switch capability {
		case access.CapabilityResourceRead, access.CapabilityResourceUse:
			action = access.ActionSourceRead
		case access.CapabilityResourceEdit:
			action = access.ActionSourceUpdate
		case access.CapabilityResourceManage:
			action = access.ActionSourceDelete
		case access.CapabilityResourcePublish:
			action = access.ActionSourceUpdate
		}
	case projectgraph.KindModel:
		switch capability {
		case access.CapabilityResourceRead, access.CapabilityResourceUse:
			action = access.ActionModelRead
		case access.CapabilityResourceEdit:
			action = access.ActionModelUpdate
		case access.CapabilityResourceManage:
			action = access.ActionModelDelete
		case access.CapabilityResourcePublish:
			action = access.ActionModelUpdate
		}
	}
	if action == "" || access.ValidateActionForKind(action, resource.Kind()) != nil {
		return "", false
	}
	return action, true
}

// nativeDeliveryAuthorizationPlan resolves the canonical PostgreSQL delivery
// graph used by production authorization.
func nativeDeliveryAuthorizationPlan(ctx context.Context, reader deploymentmodule.NativeDeliveryReader, operationID, objectID string) (deployment.DeliveryPlan, error) {
	if strings.TrimSpace(objectID) == "" {
		return deployment.DeliveryPlan{}, sql.ErrNoRows
	}
	loadPlan := func(planID string) (deployment.DeliveryPlan, error) {
		if strings.TrimSpace(planID) == "" {
			return deployment.DeliveryPlan{}, sql.ErrNoRows
		}
		plan, err := reader.Plan(ctx, planID)
		if err != nil {
			return deployment.DeliveryPlan{}, nativeDeliveryAuthorizationError(err)
		}
		return plan.RichPlan()
	}
	switch operationID {
	case "buildDeliveryPlan", "getDeliveryPlanPreview":
		return loadPlan(objectID)
	case "publishDeliveryCandidate", "getDeliveryCandidateStatus":
		candidate, err := reader.Candidate(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, nativeDeliveryAuthorizationError(err)
		}
		return loadPlan(candidate.PlanID)
	case "rollbackDeliveryGeneration", "getDeliveryGenerationStatus":
		generation, err := reader.Generation(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, nativeDeliveryAuthorizationError(err)
		}
		return loadPlan(generation.PlanID)
	case "getDeliveryBuildStatus":
		attempt, err := reader.BuildAttempt(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, nativeDeliveryAuthorizationError(err)
		}
		return loadPlan(attempt.PlanID)
	case "getDeliverySealStatus":
		seal, err := reader.SnapshotSeal(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, nativeDeliveryAuthorizationError(err)
		}
		attempt, err := reader.BuildAttempt(ctx, seal.AttemptID)
		if err != nil {
			return deployment.DeliveryPlan{}, nativeDeliveryAuthorizationError(err)
		}
		return loadPlan(attempt.PlanID)
	case "getDeliveryPublicationEvidence", "requestDeliveryPublicationApproval", "getDeliveryPublicationApproval", "approveDeliveryPublicationApproval", "denyDeliveryPublicationApproval", "revokeDeliveryPublicationApproval":
		publication, err := reader.Publication(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, nativeDeliveryAuthorizationError(err)
		}
		generation, err := reader.Generation(ctx, publication.GenerationID)
		if err != nil {
			return deployment.DeliveryPlan{}, nativeDeliveryAuthorizationError(err)
		}
		return loadPlan(generation.PlanID)
	default:
		return deployment.DeliveryPlan{}, fmt.Errorf("unsupported delivery authorization operation %q", operationID)
	}
}

func nativeDeliveryAuthorizationError(err error) error {
	if deploymentmodule.IsNativeDeliveryMissing(err) {
		return sql.ErrNoRows
	}
	return err
}
