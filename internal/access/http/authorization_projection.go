package http

import (
	"errors"
	stdhttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	platformgen "github.com/flidai/leapview/internal/platform/http/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type requestAuthorizationProjection struct {
	principalID string
	snapshot    accesssnapshot.AuthorizationSnapshot
	subjects    []access.SubjectRef
	effective   map[access.Capability]struct{}
}

// ListEffectiveCapabilities evaluates one explicit authored resource against
// the active immutable authorization projection. Both resource fields are
// required together because the response represents one resource, never an
// inferred or client-selected Project scope.
func (h Handler) ListEffectiveCapabilities(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	resource, wireKind, err := authorizationResource(r.URL.Query().Get("resourceKind"), r.URL.Query().Get("resourceId"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	projection, status, err := h.authorizationProjection(r)
	if err != nil {
		writeJSONError(w, err, status)
		return
	}
	if err := resource.ValidateAgainst(projection.snapshot.Project()); err != nil {
		writeJSONError(w, err, stdhttp.StatusNotFound)
		return
	}
	capabilities := make([]platformgen.Capability, 0)
	decisions := make([]accessgen.AuthorizationDecisionResponse, 0)
	for _, capability := range access.CanonicalCapabilities() {
		if !access.SupportsCapability(resource.Kind(), capability) {
			continue
		}
		decision, err := projection.decision(resource, wireKind, capability)
		if err != nil {
			writeJSONError(w, err, stdhttp.StatusServiceUnavailable)
			return
		}
		if decision.Allowed != nil && *decision.Allowed {
			capabilities = append(capabilities, platformgen.Capability(capability))
			decisions = append(decisions, decision)
		}
	}
	writeJSON(w, stdhttp.StatusOK, accessgen.EffectiveCapabilityListResponse{
		ResourceId: resource.ID().String(), ResourceKind: wireKind,
		Capabilities: capabilities, EffectiveGrants: decisions,
	})
}

// CheckAuthorizationBatch evaluates every request against one leased snapshot
// and one credential-attenuated capability set, preventing mixed-generation
// decisions within a batch.
func (h Handler) CheckAuthorizationBatch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var body accessgen.AuthorizationBatchCheckRequest
	if err := decodeStrictJSON(r, &body); err != nil || len(body.Checks) == 0 {
		if err == nil {
			err = errors.New("checks must not be empty")
		}
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	projection, status, err := h.authorizationProjection(r)
	if err != nil {
		writeJSONError(w, err, status)
		return
	}
	decisions := make([]accessgen.AuthorizationDecisionResponse, 0, len(body.Checks))
	for _, check := range body.Checks {
		resource, wireKind, err := authorizationResource(string(check.ResourceKind), check.ResourceId)
		if err != nil {
			writeJSONError(w, err, stdhttp.StatusBadRequest)
			return
		}
		if err := resource.ValidateAgainst(projection.snapshot.Project()); err != nil {
			writeJSONError(w, err, stdhttp.StatusNotFound)
			return
		}
		capability, err := access.ParseCapability(string(check.Capability))
		if err != nil || !access.SupportsCapability(resource.Kind(), capability) {
			if err == nil {
				err = access.ErrCapabilityNotAllowed
			}
			writeJSONError(w, err, stdhttp.StatusBadRequest)
			return
		}
		decision, err := projection.decision(resource, wireKind, capability)
		if err != nil {
			writeJSONError(w, err, stdhttp.StatusServiceUnavailable)
			return
		}
		decisions = append(decisions, decision)
	}
	writeJSON(w, stdhttp.StatusOK, accessgen.AuthorizationBatchCheckResponse{Decisions: decisions})
}

func (h Handler) authorizationProjection(r *stdhttp.Request) (requestAuthorizationProjection, int, error) {
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return requestAuthorizationProjection{}, stdhttp.StatusUnauthorized, errUnauthorized
	}
	if h.AuthorizationSnapshot == nil || h.AuthorizationSubjects == nil {
		return requestAuthorizationProjection{}, stdhttp.StatusServiceUnavailable, errors.New("authorization projection is unavailable")
	}
	snapshot, err := h.AuthorizationSnapshot(r.Context())
	if err != nil {
		return requestAuthorizationProjection{}, stdhttp.StatusServiceUnavailable, err
	}
	if err := snapshot.ValidateBound(); err != nil {
		return requestAuthorizationProjection{}, stdhttp.StatusServiceUnavailable, err
	}
	subjects, err := h.AuthorizationSubjects(r.Context(), principal.ID)
	if err != nil {
		return requestAuthorizationProjection{}, stdhttp.StatusServiceUnavailable, err
	}
	capabilities, err := snapshot.EffectiveCapabilities(subjects)
	if err == nil {
		if credential, ok := h.currentCredential(r); ok {
			capabilities, err = access.AttenuateEffectiveCapabilities(snapshot.Identity().ProjectID, capabilities, credential)
		}
	}
	if err != nil {
		status := stdhttp.StatusServiceUnavailable
		if errors.Is(err, access.ErrForbidden) || errors.Is(err, access.ErrAuthoringScopeDenied) {
			status = stdhttp.StatusForbidden
		}
		return requestAuthorizationProjection{}, status, err
	}
	effective := make(map[access.Capability]struct{}, len(capabilities))
	for _, capability := range capabilities {
		effective[capability] = struct{}{}
	}
	return requestAuthorizationProjection{principalID: principal.ID, snapshot: snapshot, subjects: subjects, effective: effective}, stdhttp.StatusOK, nil
}

func (p requestAuthorizationProjection) decision(resource access.ResourceRef, wireKind accessgen.AccessResourceKind, capability access.Capability) (accessgen.AuthorizationDecisionResponse, error) {
	allowed := false
	reason := "not_granted"
	var grantID, subjectID, subjectType *string
	if _, ok := p.effective[capability]; !ok {
		reason = "credential_attenuation"
	} else {
		for _, subject := range p.subjects {
			candidate, err := p.snapshot.Allows(subject, resource, capability)
			if err != nil {
				return accessgen.AuthorizationDecisionResponse{}, err
			}
			if candidate {
				allowed = true
				break
			}
		}
		if allowed {
			reason = "role_binding"
			for _, grant := range p.snapshot.Grants() {
				canonical := grant.Canonical
				if canonical.Resource() != resource || canonical.Capability() != capability || !containsAuthorizationSubject(p.subjects, canonical.Subject()) {
					continue
				}
				reason = "direct_grant"
				id, sid, skind := grant.ID, canonical.Subject().ID, string(canonical.Subject().Kind)
				grantID, subjectID, subjectType = &id, &sid, &skind
				break
			}
		}
	}
	resourceID := resource.ID().String()
	return accessgen.AuthorizationDecisionResponse{
		Allowed: &allowed, Capability: platformgen.Capability(capability), Reason: reason,
		ResourceKind: wireKind, ResourceId: &resourceID, GrantId: grantID,
		GrantResourceId: nil, SubjectId: subjectID, SubjectType: subjectType,
		Inherited: false, Owner: false, Platform: false,
	}, nil
}

func authorizationResource(kindValue, idValue string) (access.ResourceRef, accessgen.AccessResourceKind, error) {
	kindValue, idValue = strings.TrimSpace(kindValue), strings.TrimSpace(idValue)
	if kindValue == "" || idValue == "" {
		return access.ResourceRef{}, "", errors.New("resourceKind and resourceId are required")
	}
	wireKind := accessgen.AccessResourceKind(kindValue)
	kind := projectgraph.Kind(kindValue)
	switch wireKind {
	case accessgen.AccessResourceKindConnection, accessgen.AccessResourceKindSource,
		accessgen.AccessResourceKindModel, accessgen.AccessResourceKindSemanticModel,
		accessgen.AccessResourceKindPipeline, accessgen.AccessResourceKindDashboard:
	default:
		return access.ResourceRef{}, "", errors.New("resourceKind is invalid")
	}
	resourceID, err := projectgraph.NewResourceID(idValue)
	if err != nil {
		return access.ResourceRef{}, "", err
	}
	resource, err := access.NewResourceRef(resourceID, kind)
	return resource, wireKind, err
}

func containsAuthorizationSubject(subjects []access.SubjectRef, want access.SubjectRef) bool {
	for _, subject := range subjects {
		if subject == want {
			return true
		}
	}
	return false
}
