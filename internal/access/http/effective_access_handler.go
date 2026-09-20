package http

import (
	"errors"
	stdhttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

// ListEffectiveCapabilities exposes the explanation projection owned by the
// active serving snapshot. Query filters are presentation-only; they cannot
// select a different project or authorization generation.
func (h Handler) ListEffectiveCapabilities(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return
	}
	if h.EffectiveAccess == nil {
		writeJSONError(w, errors.New("active authorization explanation is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	decisions, err := h.EffectiveAccess(r.Context(), principal.ID)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusServiceUnavailable)
		return
	}
	resourceKind, resourceID, err := effectiveResourceFilter(r)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	filtered := filterEffectiveDecisions(decisions, resourceKind, resourceID)
	filtered = h.attenuateEffectiveDecisions(r, principal.ID, filtered)
	capabilities := make([]access.Capability, 0)
	seen := make(map[access.Capability]struct{})
	for _, decision := range filtered {
		if !decision.Allowed {
			continue
		}
		if _, exists := seen[decision.Capability]; exists {
			continue
		}
		seen[decision.Capability] = struct{}{}
		capabilities = append(capabilities, decision.Capability)
	}
	for i := range capabilities {
		for j := i + 1; j < len(capabilities); j++ {
			if capabilityOrder(capabilities[j]) < capabilityOrder(capabilities[i]) {
				capabilities[i], capabilities[j] = capabilities[j], capabilities[i]
			}
		}
	}
	projectID := chi.URLParam(r, "project")
	responseResourceKind, responseResourceID := resourceKind, resourceID
	if responseResourceKind == "" {
		responseResourceKind = string(projectgraph.KindProjectNamespace)
	}
	if responseResourceID == "" {
		responseResourceID = projectID
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{
		"resourceId": responseResourceID, "resourceKind": responseResourceKind,
		"capabilities": capabilityStrings(capabilities), "effectiveGrants": authorizationDecisionDTOs(filtered),
	})
}

// CheckAuthorizationBatch evaluates requested checks against the same
// explanation authority used by the effective-capability listing. A denied
// decision is retained so the caller can explain exactly why access was not
// granted.
func (h Handler) CheckAuthorizationBatch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return
	}
	if h.EffectiveAccess == nil {
		writeJSONError(w, errors.New("active authorization explanation is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	var request accessgen.AuthorizationBatchCheckRequest
	if err := decodeStrictJSON(r, &request); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	decisions, err := h.EffectiveAccess(r.Context(), principal.ID)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusServiceUnavailable)
		return
	}
	result := make([]map[string]any, 0, len(request.Checks))
	for _, check := range request.Checks {
		kind, parseErr := projectgraph.ParseKind(string(check.ResourceKind))
		if parseErr != nil {
			writeJSONError(w, parseErr, stdhttp.StatusBadRequest)
			return
		}
		resourceID, idErr := projectgraph.NewResourceID(check.ResourceId)
		if idErr != nil {
			writeJSONError(w, idErr, stdhttp.StatusBadRequest)
			return
		}
		capability, capabilityErr := access.ParseCapability(string(check.Capability))
		if capabilityErr != nil {
			writeJSONError(w, capabilityErr, stdhttp.StatusBadRequest)
			return
		}
		found := false
		for _, decision := range decisions {
			if decision.ResourceKind == string(kind) && decision.ResourceID == resourceID.String() && decision.Capability == capability {
				result = append(result, authorizationDecisionDTO(h.attenuateEffectiveDecision(r, principal.ID, decision)))
				found = true
				break
			}
		}
		if !found {
			result = append(result, authorizationDecisionDTO(access.AuthorizationDecision{
				Allowed: false, Capability: capability, Reason: "no direct, inherited, owner, or platform authority",
				ResourceKind: string(kind), ResourceID: resourceID.String(),
			}))
		}
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"decisions": result})
}

// attenuateEffectiveDecision applies the request credential's least-privilege
// boundary to immutable RBAC evidence. A stored token scope can remove
// authority, never add it; a dynamic (nil) API-token scope inherits the
// principal's current decision. Authoring credentials additionally bind the
// request to their project scope.
func (h Handler) attenuateEffectiveDecision(r *stdhttp.Request, principalID string, decision access.AuthorizationDecision) access.AuthorizationDecision {
	if !decision.Allowed {
		return decision
	}
	credential, ok := h.currentCredential(r)
	if !ok {
		return decision
	}
	if credential.Principal.ID != "" && credential.Principal.ID != principalID {
		decision.Allowed = false
		decision.Reason = "credential principal does not match request principal"
		return decision
	}
	if credential.Authoring != nil {
		projectID := strings.TrimSpace(chi.URLParam(r, "project"))
		if projectID != "" && credential.Authoring.Scope.ProjectID.String() != projectID {
			decision.Allowed = false
			decision.Reason = "credential is scoped to a different project"
			return decision
		}
		if !containsCapability(credential.Authoring.Scope.Capabilities, decision.Capability) {
			decision.Allowed = false
			decision.Reason = "credential does not grant requested capability"
			return decision
		}
	}
	if credential.Token.ID != "" && credential.Token.Capabilities != nil && !containsCapability(credential.Token.Capabilities, decision.Capability) {
		decision.Allowed = false
		decision.Reason = "credential does not grant requested capability"
	}
	return decision
}

func (h Handler) attenuateEffectiveDecisions(r *stdhttp.Request, principalID string, decisions []access.AuthorizationDecision) []access.AuthorizationDecision {
	result := make([]access.AuthorizationDecision, len(decisions))
	for index, decision := range decisions {
		result[index] = h.attenuateEffectiveDecision(r, principalID, decision)
	}
	return result
}

func effectiveResourceFilter(r *stdhttp.Request) (string, string, error) {
	kind := strings.TrimSpace(r.URL.Query().Get("resourceKind"))
	id := strings.TrimSpace(r.URL.Query().Get("resourceId"))
	if kind == "" && id == "" {
		return "", "", nil
	}
	if kind == "" || id == "" {
		return "", "", errors.New("resourceKind and resourceId must be supplied together")
	}
	parsedKind, err := projectgraph.ParseKind(kind)
	if err != nil {
		return "", "", err
	}
	parsedID, err := projectgraph.NewResourceID(id)
	if err != nil {
		return "", "", err
	}
	return string(parsedKind), parsedID.String(), nil
}

func filterEffectiveDecisions(decisions []access.AuthorizationDecision, resourceKind, resourceID string) []access.AuthorizationDecision {
	filtered := make([]access.AuthorizationDecision, 0, len(decisions))
	for _, decision := range decisions {
		if resourceKind != "" && decision.ResourceKind != resourceKind {
			continue
		}
		if resourceID != "" && decision.ResourceID != resourceID {
			continue
		}
		filtered = append(filtered, decision)
	}
	return filtered
}

func authorizationDecisionDTOs(decisions []access.AuthorizationDecision) []map[string]any {
	result := make([]map[string]any, 0, len(decisions))
	for _, decision := range decisions {
		result = append(result, authorizationDecisionDTO(decision))
	}
	return result
}

func authorizationDecisionDTO(decision access.AuthorizationDecision) map[string]any {
	result := map[string]any{
		"allowed": decision.Allowed, "capability": string(decision.Capability), "reason": decision.Reason,
		"resourceKind": decision.ResourceKind, "inherited": decision.Inherited,
		"owner": decision.Owner, "platform": decision.Platform,
	}
	if decision.ResourceID != "" {
		result["resourceId"] = decision.ResourceID
	}
	if decision.GrantID != "" {
		result["grantId"] = decision.GrantID
	}
	if decision.GrantResourceID != "" {
		result["grantResourceId"] = decision.GrantResourceID
	}
	if decision.SubjectType != "" {
		result["subjectType"] = decision.SubjectType
	}
	if decision.SubjectID != "" {
		result["subjectId"] = decision.SubjectID
	}
	return result
}

func capabilityStrings(capabilities []access.Capability) []string {
	result := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		result = append(result, string(capability))
	}
	return result
}

func capabilityOrder(capability access.Capability) int {
	for index, candidate := range access.CanonicalCapabilities() {
		if candidate == capability {
			return index
		}
	}
	return 100
}
