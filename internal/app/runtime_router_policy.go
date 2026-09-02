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
	_ canonicalRuntimeHost,
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
			return strings.TrimSpace(target.ActiveGenerationID) != "", nil
		}
		if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, deployment.ErrNotFound) {
			return false, fmt.Errorf("read active delivery target: %w", err)
		}
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
	// The legacy scope table is only a compatibility fallback when no canonical
	// target row exists. A runtime host may still be warming up (or be nil in a
	// fresh process) while the durable stores have no active generation.
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
	// Deployment status/event reads, delivery plan resolution, and candidate
	// source synchronization are control-plane operations. Their project-scoped
	// RESOURCE_READ/EDIT contracts cannot be evaluated against the project graph
	// (projects intentionally only support PROJECT_ADMIN), and the sealed delivery
	// pointer advances before the in-process runtime cutover. Keep these
	// operations on the durable, exact-claim bootstrap path through that
	// marker-to-runtime warm-up window.
	if bootstrapControlPlaneOperation(operationID) {
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
	if err := projectID.Validate(); err != nil || projectID.String() != strings.TrimSpace(projectID.String()) {
		return accessmodule.APIGenBootstrapDecision{Handled: true}, nil
	}
	if claims == nil {
		return accessmodule.APIGenBootstrapDecision{}, errors.New("project claim repository is unavailable")
	}
	claim, err := claims.GetProjectClaim(ctx)
	if errors.Is(err, deployment.ErrProjectClaimNotFound) {
		return accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: bootstrapOperationAllowedWithoutClaim(operationID)}, nil
	}
	if err != nil {
		return accessmodule.APIGenBootstrapDecision{}, fmt.Errorf("read bootstrap project claim: %w", err)
	}
	if claim.ProjectID != projectID || claim.Environment != servingstatemodule.Environment(strings.TrimSpace(environment)) {
		return accessmodule.APIGenBootstrapDecision{Handled: true}, nil
	}
	return accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: bootstrapOperationAllowed(operationID)}, nil
}

func bootstrapControlPlaneOperation(operationID string) bool {
	switch operationID {
	case "listDeployments", "getDeployment", "listDeploymentEvents",
		"planProjectCandidateSynchronization", "uploadProjectCandidateSourceBlob", "retainProjectCandidateSource", "commitProjectCandidateSynchronization",
		"getDeliveryCandidateStatus", "getDeliveryPlanPreview":
		return true
	default:
		return false
	}
}

func bootstrapOperationAllowed(operationID string) bool {
	switch operationID {
	case "startProjectCandidate", "getProjectCandidate", "replaceProjectCandidateArtifact", "retryProjectCandidate", "cancelProjectCandidate", "publishProjectCandidate", "reviewProjectCandidate", "cancelProjectCandidateByKey", "planProjectCandidateSynchronization", "uploadProjectCandidateSourceBlob", "retainProjectCandidateSource", "commitProjectCandidateSynchronization", "createDeliveryPlan", "buildDeliveryPlan", "publishDeliveryCandidate", "getDeliveryCandidateStatus", "getDeliveryPlanPreview",
		"createManagedDataUploadSession", "getManagedDataUploadSession", "cancelManagedDataUploadSession", "finalizeManagedDataUploadSession",
		"createManagedDataS3MultipartUpload", "signManagedDataS3MultipartPart", "completeManagedDataS3MultipartUpload", "abortManagedDataS3MultipartUpload",
		"listDeployments", "getDeployment", "listDeploymentEvents":
		return true
	case "managedDataTusTransport":
		return true
	default:
		return false
	}
}

func bootstrapOperationAllowedWithoutClaim(operationID string) bool {
	switch operationID {
	case "startProjectCandidate", "planProjectCandidateSynchronization",
		"createManagedDataUploadSession", "getManagedDataUploadSession", "cancelManagedDataUploadSession", "finalizeManagedDataUploadSession",
		"createManagedDataS3MultipartUpload", "signManagedDataS3MultipartPart", "completeManagedDataS3MultipartUpload", "abortManagedDataS3MultipartUpload",
		"managedDataTusTransport":
		return true
	default:
		return false
	}
}

// deliveryRoleAllows is the only project-wide escape hatch in delivery
// authorization. Explicit project role bindings are target-owned policy; a
// direct resource grant must still match every affected graph resource.
func deliveryRoleAllows(snapshot accesssnapshot.AuthorizationSnapshot, subjects []access.SubjectRef, capability access.Capability) bool {
	for _, binding := range snapshot.RoleBindings() {
		for _, subject := range subjects {
			if binding.Subject != subject {
				continue
			}
			for _, captured := range binding.Capabilities {
				if captured == capability {
					return true
				}
			}
		}
	}
	return false
}

// deliveryProjectAllows evaluates project-scoped administrative authority on
// the exact project root. Unlike the role-only fallback used for graph-wide
// resource operations, approval decisions intentionally accept either an
// explicit project role or a canonical grant on the project resource.
func deliveryProjectAllows(snapshot accesssnapshot.AuthorizationSnapshot, subjects []access.SubjectRef, projectID projectgraph.ResourceID, capability access.Capability) (bool, error) {
	project, err := access.NewResourceRef(projectID, projectgraph.KindProject)
	if err != nil {
		return false, err
	}
	for _, subject := range subjects {
		allowed, err := snapshot.Allows(subject, project, capability)
		if err != nil {
			return false, err
		}
		if allowed {
			return true, nil
		}
	}
	return false, nil
}

func deliveryAuthorizationPlan(ctx context.Context, reader deployment.DeliveryReader, operationID, objectID string) (deployment.DeliveryPlan, error) {
	if strings.TrimSpace(objectID) == "" {
		return deployment.DeliveryPlan{}, sql.ErrNoRows
	}
	loadPlan := func(planID string) (deployment.DeliveryPlan, error) {
		if strings.TrimSpace(planID) == "" {
			return deployment.DeliveryPlan{}, sql.ErrNoRows
		}
		return reader.PlanByID(ctx, planID)
	}
	switch operationID {
	case "buildDeliveryPlan", "getDeliveryPlanPreview":
		return loadPlan(objectID)
	case "publishDeliveryCandidate", "getDeliveryCandidateStatus":
		candidate, err := reader.DeliveryCandidateByID(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, err
		}
		return loadPlan(candidate.PlanID)
	case "rollbackDeliveryGeneration", "getDeliveryGenerationStatus":
		generation, err := reader.DeliveryGenerationByID(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, err
		}
		return loadPlan(generation.PlanID)
	case "getDeliveryBuildStatus":
		attempt, err := reader.DeliveryBuildAttemptByID(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, err
		}
		return loadPlan(attempt.PlanID)
	case "getDeliverySealStatus":
		seal, err := reader.DeliveryCatalogSealByID(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, err
		}
		return loadPlan(seal.PlanID)
	case "getDeliveryPublicationEvidence", "requestDeliveryPublicationApproval", "getDeliveryPublicationApproval", "approveDeliveryPublicationApproval", "denyDeliveryPublicationApproval", "revokeDeliveryPublicationApproval":
		publication, err := reader.DeliveryPublicationByID(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, err
		}
		return loadPlan(publication.PlanID)
	default:
		return deployment.DeliveryPlan{}, fmt.Errorf("unsupported delivery authorization operation %q", operationID)
	}
}

func deliveryApprovalDecisionOperation(operationID string) bool {
	switch operationID {
	case "approveDeliveryPublicationApproval", "denyDeliveryPublicationApproval", "revokeDeliveryPublicationApproval":
		return true
	default:
		return false
	}
}

func deliveryAuthorizationResources(plan deployment.DeliveryPlan) ([]access.ResourceRef, error) {
	impact := append([]deployment.DeliveryImpactResource{}, plan.Evidence.GraphImpact.Added...)
	impact = append(impact, plan.Evidence.GraphImpact.Removed...)
	impact = append(impact, plan.Evidence.GraphImpact.DirectlyModified...)
	impact = append(impact, plan.Evidence.GraphImpact.IndirectlyAffected...)
	resources := make([]access.ResourceRef, 0, len(impact))
	seen := make(map[string]struct{}, len(impact))
	for _, item := range impact {
		id, err := projectgraph.NewResourceID(strings.TrimSpace(item.ID))
		if err != nil {
			return nil, err
		}
		kind, err := projectgraph.ParseKind(strings.TrimSpace(item.Kind))
		if err != nil {
			return nil, err
		}
		resource, err := access.NewResourceRef(id, kind)
		if err != nil {
			return nil, err
		}
		key := resource.ID().String() + "\x00" + string(resource.Kind())
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		resources = append(resources, resource)
	}
	return resources, nil
}
func deliverySnapshotAllows(snapshot accesssnapshot.AuthorizationSnapshot, subjects []access.SubjectRef, resources []access.ResourceRef, capability access.Capability) (bool, error) {
	for _, resource := range resources {
		resourceCapability := deliveryResourceCapability(resource, capability)
		if handled, roleAllowed := projectRootRoleDecision(snapshot, subjects, resource, resourceCapability); handled {
			if !roleAllowed {
				return false, nil
			}
			continue
		}
		allowed := false
		for _, subject := range subjects {
			candidate, err := snapshot.Allows(subject, resource, resourceCapability)
			if err != nil {
				return false, err
			}
			if candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return false, nil
		}
	}
	return true, nil
}

// projectRootRoleDecision applies the project-root half of canonical browser
// and delivery authorization. Project roots deliberately accept only
// PROJECT_ADMIN as direct grants, so a resource capability scoped to the root
// must be satisfied by an explicit project role bundle.
func projectRootRoleDecision(snapshot accesssnapshot.AuthorizationSnapshot, subjects []access.SubjectRef, resource access.ResourceRef, capability access.Capability) (handled, allowed bool) {
	if resource.Kind() != projectgraph.KindProject || access.SupportsCapability(resource.Kind(), capability) {
		return false, false
	}
	return true, deliveryRoleAllows(snapshot, subjects, capability)
}

func deliveryResourceCapability(resource access.ResourceRef, capability access.Capability) access.Capability {
	if capability == access.CapabilityResourcePublish &&
		!access.SupportsCapability(resource.Kind(), capability) &&
		access.SupportsCapability(resource.Kind(), access.CapabilityResourceEdit) {
		// Publishing a plan requires publish authority for publishable
		// dashboards and edit authority for the non-publishable graph
		// resources changed by that same immutable plan.
		return access.CapabilityResourceEdit
	}
	return capability
}
