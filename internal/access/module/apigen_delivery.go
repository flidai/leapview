package module

import (
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func isDeliveryAPIGenOperation(contract APIGenOperationContract) bool {
	// Generated contracts carry the public API prefix (currently /api/v1),
	// while this authorizer only cares about the target-owned delivery suffix.
	return strings.Contains(contract.Path, "/projects/{project}/delivery")
}

// isBootstrapDeliveryAPIGenOperation is the exact delivery allowlist needed to
// establish and resolve a plan before the target has an active generation.
// It includes reviewer approval so its dedicated credential path can run
// before the first generation; ordinary authoring operations use the narrower
// allowlist below.
func isBootstrapDeliveryAPIGenOperation(operationID string) bool {
	switch operationID {
	case "createDeliveryPlan", "buildDeliveryPlan", "publishDeliveryCandidate", "getDeliveryCandidateStatus", "getDeliveryPlanPreview",
		"requestDeliveryPublicationApproval", "approveDeliveryPublicationApproval":
		return true
	default:
		return false
	}
}

// isAuthoringDeliveryBootstrapOperation is the exact delivery allowlist for
// scoped authoring credentials. Publication approval remains reviewer-only
// and runs through its dedicated exact-scope validator and marker; the
// downstream approval authority prevents a principal from approving its own
// publication.
func isAuthoringDeliveryBootstrapOperation(operationID string) bool {
	switch operationID {
	case "createDeliveryPlan", "buildDeliveryPlan", "publishDeliveryCandidate", "getDeliveryCandidateStatus", "getDeliveryPlanPreview", "requestDeliveryPublicationApproval":
		return true
	default:
		return false
	}
}

func (a *APIGenAuthorizer) protectDelivery(operationID string, capability access.Capability, next http.Handler) http.Handler {
	return a.module.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.authorizeDeliveryRequest(w, r, operationID, capability, next)
	}))
}

// protectDeliveryBootstrapAware admits the initial delivery commands through
// the explicit pre-activation bootstrap decision. The opaque markers bind the
// exact principal/project/capability for downstream coordinators, which
// recheck their durable active-generation and immutable-snapshot fences before
// committing state. The allowlisted delivery operations accept an exact-scope
// authoring credential through this branch; publication approval has its own
// reviewer-only marker, while all other bootstrap requests remain restricted
// to explicit REST API tokens by AuthorizeBootstrapRequest.
func (a *APIGenAuthorizer) protectDeliveryBootstrapAware(operationID string, capability access.Capability, next http.Handler) http.Handler {
	return a.module.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := a.module.CurrentPrincipal(r)
		if !ok || strings.TrimSpace(principal.ID) == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		projectID, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
		if err != nil || projectID != a.runtime.ProjectID() {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		decision, err := a.bootstrap(r.Context(), r, operationID, projectID, capability)
		if err != nil {
			a.module.logger.WarnContext(r.Context(), "generated API bootstrap authorization failed", "operation", operationID, "project", projectID, "capability", capability, "error", err)
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if decision.Handled {
			if operationID != "approveDeliveryPublicationApproval" && isAuthoringDeliveryBootstrapOperation(operationID) {
				if credential, found := a.module.requestCredential(r); found && credential.Authoring != nil {
					authorized, authErr := a.module.AuthorizeAuthoringBootstrapRequest(r.Context(), r, projectID.String(), capability)
					if authErr != nil {
						a.module.logger.WarnContext(r.Context(), "generated API authoring bootstrap credential authorization failed", "operation", operationID, "project", projectID, "capability", capability, "error", authErr)
						http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
						return
					}
					if !authorized || !decision.Allowed {
						http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
						return
					}
					marked := r.WithContext(withBootstrapAuthorization(r.Context(), projectID, principal.ID, capability))
					next.ServeHTTP(w, marked)
					return
				}
			}
			if operationID == "approveDeliveryPublicationApproval" {
				if !decision.Allowed {
					http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
					return
				}
				authorized, authErr := a.module.AuthorizePublicationApprovalBootstrapRequest(r.Context(), r, projectID.String())
				if authErr != nil {
					a.module.logger.WarnContext(r.Context(), "generated API publication approval bootstrap credential authorization failed", "operation", operationID, "project", projectID, "capability", capability, "error", authErr)
					http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
					return
				}
				if !authorized {
					http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
					return
				}
				marked := r.WithContext(withPublicationApprovalBootstrapAuthorization(r.Context(), projectID, principal.ID))
				next.ServeHTTP(w, marked)
				return
			}
			if bearerToken(r) == "" {
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}
			authorized, authErr := a.module.AuthorizeBootstrapRequest(r.Context(), r, capability)
			if authErr != nil {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			if !authorized || !decision.Allowed {
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}
			marked := r.WithContext(withBootstrapAuthorization(r.Context(), projectID, principal.ID, capability))
			next.ServeHTTP(w, marked)
			return
		}
		a.authorizeDeliveryRequest(w, r, operationID, capability, next)
	}))
}

func (a *APIGenAuthorizer) authorizeDeliveryRequest(w http.ResponseWriter, r *http.Request, operationID string, capability access.Capability, next http.Handler) {
	principal, ok := a.module.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	projectID, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
	if err != nil || projectID != a.runtime.ProjectID() {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	objectID := deliveryObjectID(operationID, r)
	allowed, err := a.delivery(r.Context(), r, operationID, objectID, projectID, capability)
	if err != nil {
		a.module.logger.WarnContext(r.Context(), "generated delivery API authorization failed", "operation", operationID, "object", objectID, "project", projectID, "capability", capability, "error", err)
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	if !allowed {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	if requirement, typedOperation := a.typedRequirement(operationID); typedOperation {
		pairs, pairErr := requirement.ResolvePairs(projectID)
		credential, hasCredential := a.module.requestCredential(r)
		typedToken := hasCredential && strings.TrimSpace(credential.Token.ID) != "" && (credential.Token.PermissionProfile != "" || credential.Token.Permissions != nil)
		if pairErr != nil || (typedToken && (credential.Token.PermissionProfile != access.PermissionCatalogProfile || !permissionPairsAllowAll(credential.Token.Permissions, pairs))) || (!typedToken && hasCredential && strings.TrimSpace(credential.Token.ID) != "") {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
	}
	next.ServeHTTP(w, r)
}

func deliveryObjectID(operationID string, r *http.Request) string {
	if r == nil {
		return ""
	}
	parameter := ""
	switch operationID {
	case "buildDeliveryPlan", "getDeliveryPlanPreview":
		parameter = "plan"
	case "publishDeliveryCandidate", "getDeliveryCandidateStatus":
		parameter = "candidate"
	case "rollbackDeliveryGeneration", "getDeliveryGenerationStatus":
		parameter = "generation"
	case "getDeliveryBuildStatus":
		parameter = "build"
	case "getDeliverySealStatus":
		parameter = "seal"
	case "getDeliveryPublicationEvidence":
		parameter = "publication"
	case "requestDeliveryPublicationApproval", "getDeliveryPublicationApproval", "approveDeliveryPublicationApproval", "denyDeliveryPublicationApproval", "revokeDeliveryPublicationApproval":
		parameter = "publication"
	}
	if parameter == "" {
		return ""
	}
	return strings.TrimSpace(chi.URLParam(r, parameter))
}
