package http

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

const adminRoleBindingEnvelopeTTL = 5 * time.Minute

// roleBindingCreateRequest is deliberately narrower than access.RoleBinding:
// permission pairs and legacy capabilities are never accepted from an HTTP
// caller. The versioned permission role is expanded by the target authority
// immediately before the repository command is invoked.
type roleBindingCreateRequest struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	SubjectType          string `json:"subjectType"`
	SubjectID            string `json:"subjectId"`
	Role                 string `json:"role"`
	GrantAdminEnvelopeID string `json:"grantAdminEnvelopeId"`
	ExpectedRevision     *int64 `json:"expectedRevision"`
}

type roleBindingDeleteRequest struct {
	ExpectedRevision *int64 `json:"expectedRevision"`
}

func (h Handler) authorizationPolicyScope(r *stdhttp.Request) (access.AuthorizationPolicyScope, error) {
	return h.authorizationPolicyScopeForProject(chi.URLParam(r, "project"))
}

func (h Handler) authorizationPolicyScopeForProject(projectID string) (access.AuthorizationPolicyScope, error) {
	if h.AuthorizationPolicyTargetID == "" || h.AuthorizationPolicyEnvironment == "" {
		return access.AuthorizationPolicyScope{}, fmt.Errorf("%w: target and environment are not configured", access.ErrAuthorizationPolicyInvalidScope)
	}
	scope := access.AuthorizationPolicyScope{
		TargetID: h.AuthorizationPolicyTargetID, ProjectID: projectID,
		Environment: h.AuthorizationPolicyEnvironment,
	}
	if err := access.ValidateAuthorizationPolicyScope(scope); err != nil {
		return access.AuthorizationPolicyScope{}, err
	}
	return scope, nil
}

// RoleBindingAdministration returns the current target-owned role policy and
// the versioned role catalog for product administration. The current project
// is server-bound; callers cannot select another policy namespace.
func (h Handler) RoleBindingAdministration(ctx context.Context) (access.RoleBindingAdministrationState, error) {
	if h.CurrentProjectID == nil {
		return access.RoleBindingAdministrationState{}, errors.New("active project identity is unavailable")
	}
	projectID, err := h.CurrentProjectID(ctx)
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	scope, err := h.authorizationPolicyScopeForProject(projectID.String())
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	repository, err := h.repository()
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	reader, ok := repository.(access.AuthorizationPolicyReader)
	if !ok {
		return access.RoleBindingAdministrationState{}, errors.New("authorization policy reader is unavailable")
	}
	policy, err := reader.AuthorizationPolicy(ctx, scope)
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	return access.RoleBindingAdministrationState{
		Scope: scope, Revision: policy.Revision, Digest: policy.Digest,
		RoleBindings: append([]access.RoleBinding(nil), policy.RoleBindings...),
		RolePresets:  access.PermissionRolePresets(),
	}, nil
}

// ApplyRoleBindingAdministration performs the same access-owned authorization,
// envelope, CAS, persistence, and audit checks as the public role-binding API.
// It exists so the browser command loop does not need bearer-only API access.
func (h Handler) ApplyRoleBindingAdministration(r *stdhttp.Request, command access.RoleBindingAdministrationCommand) (access.RoleBindingAdministrationState, error) {
	if r == nil || h.CurrentProjectID == nil {
		return access.RoleBindingAdministrationState{}, errors.New("role administration is unavailable")
	}
	projectID, err := h.CurrentProjectID(r.Context())
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	scope, err := h.authorizationPolicyScopeForProject(projectID.String())
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	repository, err := h.repository()
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	idempotencyKey := strings.TrimSpace(command.IdempotencyKey)
	if idempotencyKey == "" {
		return access.RoleBindingAdministrationState{}, errors.New("idempotency key is required")
	}

	switch strings.TrimSpace(command.Action) {
	case "grant_role":
		bindingID := strings.TrimSpace(command.BindingID)
		if bindingID == "" {
			bindingID = "role-binding-" + idempotencyKey
		}
		binding, err := access.NewTypedRoleBinding(bindingID, string(command.Role), command.Subject, command.Role, projectID)
		if err != nil {
			return access.RoleBindingAdministrationState{}, err
		}
		if h.DurableGrantService == nil {
			return access.RoleBindingAdministrationState{}, errors.New("durable grant service is unavailable")
		}
		service, err := h.DurableGrantService(r)
		if err != nil {
			return access.RoleBindingAdministrationState{}, err
		}
		envelope, err := service.IssueGrantAdminEnvelope(r.Context(), access.GrantAdminEnvelopeRequest{
			TargetProjectID: projectID, Permissions: access.ClonePermissionPairs(binding.Permissions),
			RecipientSelector: string(binding.Subject.Kind) + ":" + binding.Subject.ID,
			RoleVersion:       access.PermissionRoleVersion(binding.PermissionRole), TTL: adminRoleBindingEnvelopeTTL,
			IdempotencyKey: idempotencyKey + ":envelope",
		})
		if err != nil {
			return access.RoleBindingAdministrationState{}, err
		}
		var policy access.AuthorizationPolicy
		err = executeAuditedMutation(r, repository, accessgen.GenCommandOperationCreateProjectRoleBinding(), func(tx access.Repository) (access.AuditEventInput, error) {
			writer, writeOK := tx.(access.AuthorizationPolicyWriter)
			envelopes, envelopeOK := tx.(access.CurrentGrantAdminEnvelopeReader)
			if !writeOK || !envelopeOK {
				return access.AuditEventInput{}, errors.New("transactional role administration is unavailable")
			}
			actorID := h.currentPrincipalID(r)
			currentEnvelope, readErr := envelopes.CurrentGrantAdminEnvelopeForMutation(r.Context(), envelope.ID, actorID)
			if readErr != nil {
				return access.AuditEventInput{}, readErr
			}
			if err := access.ValidateGrantAdminEnvelopeRoleBinding(currentEnvelope, actorID, projectID, binding.Subject, binding.PermissionRole, binding.Permissions); err != nil {
				return access.AuditEventInput{}, err
			}
			if err := h.authorizeProjectRoleBindingMutation(r, currentEnvelope, actorID, projectID, binding.Permissions); err != nil {
				return access.AuditEventInput{}, err
			}
			policy, err = writer.UpsertAuthorizationRoleBinding(r.Context(), access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, ExpectedRevision: command.ExpectedRevision, IdempotencyKey: idempotencyKey})
			if err != nil {
				return access.AuditEventInput{}, err
			}
			metadata, err := accessgen.EncodeGenCreateProjectRoleBindingAuditPayload(accessgen.GenSchemaRoleBindingAuditPayload{TargetId: scope.TargetID, ProjectId: scope.ProjectID, Environment: scope.Environment, BindingId: binding.ID, SubjectType: string(binding.Subject.Kind), SubjectId: binding.Subject.ID, Role: string(binding.PermissionRole), PolicyRevision: policy.Revision, PolicyDigest: policy.Digest})
			if err != nil {
				return access.AuditEventInput{}, err
			}
			event := auditInput(r, "role_binding.created", actorID, "role_binding", binding.ID, access.CapabilityProjectAdmin, "success", nil)
			event.MetadataJSON = metadata
			return event, nil
		})
		if err != nil {
			return access.RoleBindingAdministrationState{}, err
		}
	case "revoke_role":
		if err := access.ValidateAuthorizationRoleBindingID(command.BindingID); err != nil {
			return access.RoleBindingAdministrationState{}, err
		}
		manage, err := access.NewProjectPermissionPair(access.ActionProjectAccessManage, projectID)
		if err != nil {
			return access.RoleBindingAdministrationState{}, err
		}
		if h.CurrentEffectivePermissionOptions == nil {
			return access.RoleBindingAdministrationState{}, access.ErrForbidden
		}
		authority, err := h.CurrentEffectivePermissionOptions(r.Context(), h.currentPrincipalID(r))
		if err != nil || validateProjectRoleBindingPermissionCeiling(authority, []access.PermissionPair{manage}) != nil {
			return access.RoleBindingAdministrationState{}, access.ErrForbidden
		}
		if credential, found := h.currentCredential(r); found {
			if credential.Token.ID == "" || credential.Token.PrincipalID != h.currentPrincipalID(r) || credential.Token.PermissionProfile != access.PermissionCatalogProfile || credential.Token.Permissions == nil || validateProjectRoleBindingPermissionCeiling(credential.Token.Permissions, []access.PermissionPair{manage}) != nil {
				return access.RoleBindingAdministrationState{}, access.ErrForbidden
			}
		} else {
			if h.CurrentSession == nil {
				return access.RoleBindingAdministrationState{}, access.ErrForbidden
			}
			if sessionID, found := h.CurrentSession(r); !found || sessionID == "" {
				return access.RoleBindingAdministrationState{}, access.ErrForbidden
			}
		}
		var policy access.AuthorizationPolicy
		err = executeAuditedMutation(r, repository, accessgen.GenCommandOperationDeleteProjectRoleBinding(), func(tx access.Repository) (access.AuditEventInput, error) {
			reader, readOK := tx.(access.AuthorizationPolicyReader)
			writer, writeOK := tx.(access.AuthorizationPolicyWriter)
			if !readOK || !writeOK {
				return access.AuditEventInput{}, errors.New("transactional role administration is unavailable")
			}
			current, err := reader.AuthorizationPolicyRevision(r.Context(), scope, command.ExpectedRevision)
			if err != nil {
				return access.AuditEventInput{}, err
			}
			var removed access.RoleBinding
			for _, binding := range current.RoleBindings {
				if binding.ID == command.BindingID {
					removed = binding
					break
				}
			}
			if removed.ID == "" {
				return access.AuditEventInput{}, access.ErrAuthorizationPolicyNotFound
			}
			policy, err = writer.RemoveAuthorizationRoleBinding(r.Context(), access.AuthorizationRoleBindingDeleteInput{Scope: scope, BindingID: command.BindingID, ExpectedRevision: command.ExpectedRevision, IdempotencyKey: idempotencyKey})
			if err != nil {
				return access.AuditEventInput{}, err
			}
			role := string(removed.PermissionRole)
			if role == "" {
				role = string(removed.Role)
			}
			metadata, err := accessgen.EncodeGenDeleteProjectRoleBindingAuditPayload(accessgen.GenSchemaRoleBindingAuditPayload{TargetId: scope.TargetID, ProjectId: scope.ProjectID, Environment: scope.Environment, BindingId: removed.ID, SubjectType: string(removed.Subject.Kind), SubjectId: removed.Subject.ID, Role: role, PolicyRevision: policy.Revision, PolicyDigest: policy.Digest})
			if err != nil {
				return access.AuditEventInput{}, err
			}
			event := auditInput(r, "role_binding.removed", h.currentPrincipalID(r), "role_binding", removed.ID, access.CapabilityProjectAdmin, "success", nil)
			event.MetadataJSON = metadata
			return event, nil
		})
		if err != nil {
			return access.RoleBindingAdministrationState{}, err
		}
	default:
		return access.RoleBindingAdministrationState{}, errors.New("unknown role administration action")
	}
	return h.RoleBindingAdministration(r.Context())
}

func roleBindingDTO(binding access.RoleBinding, policy access.AuthorizationPolicy) map[string]any {
	if binding.TypedRoleBinding() {
		return map[string]any{
			"id": binding.ID, "name": binding.Name,
			"subjectType": string(binding.Subject.Kind), "subjectId": binding.Subject.ID,
			"role": string(binding.PermissionRole), "permissionProfile": binding.PermissionProfile,
			"permissions":    access.ClonePermissionPairs(binding.Permissions),
			"policyRevision": policy.Revision, "policyDigest": policy.Digest,
		}
	}
	capabilities := make([]string, 0, len(binding.Capabilities))
	for _, capability := range binding.Capabilities {
		capabilities = append(capabilities, string(capability))
	}
	return map[string]any{
		"id": binding.ID, "name": binding.Name,
		"subjectType": string(binding.Subject.Kind), "subjectId": binding.Subject.ID,
		"role": string(binding.Role), "capabilities": capabilities,
		"policyRevision": policy.Revision, "policyDigest": policy.Digest,
	}
}

func roleBindingPolicyMetadata(scope access.AuthorizationPolicyScope, policy access.AuthorizationPolicy) map[string]any {
	return map[string]any{
		"targetId": scope.TargetID, "projectId": scope.ProjectID, "environment": scope.Environment,
		"policyRevision": policy.Revision, "policyDigest": policy.Digest,
	}
}

func validateProjectRoleBindingPermissionCeiling(ceiling, requested []access.PermissionPair) error {
	if err := access.ValidatePermissionPairs(ceiling); err != nil {
		return fmt.Errorf("credential permission ceiling: %w", err)
	}
	if err := access.ValidatePermissionPairs(requested); err != nil {
		return err
	}
	for index, pair := range requested {
		required, err := access.RequiredPermissionPairs(pair)
		if err != nil {
			return err
		}
		for _, requirement := range required {
			covered := false
			for _, granted := range ceiling {
				// A future-resource selector is allowed to cover an exact
				// resource, while an exact selector must never widen into a
				// future-resource role. Equal future selectors are direct
				// authority and therefore match by their complete pair key.
				if granted.Key() == requirement.Key() || access.PermissionPairAllows(granted, requirement) {
					covered = true
					break
				}
			}
			if !covered {
				return fmt.Errorf("%w: permission %d (%s)", access.ErrTokenPermissionNotAllowed, index, pair.Action)
			}
		}
	}
	return nil
}

// authorizeProjectRoleBindingMutation applies the mutation-boundary checks
// that cannot be represented by the generated operation's single manage
// privilege. A role binding is an authority issuance: the caller must hold
// delegate and every pair captured by the role, using the same credential that
// issued the administration envelope.
func (h Handler) authorizeProjectRoleBindingMutation(r *stdhttp.Request, envelope access.GrantAdminEnvelope, actorID string, projectID projectgraph.ResourceID, permissions []access.PermissionPair) error {
	if envelope.Issuer.PrincipalID != actorID {
		return access.ErrGrantAdminEnvelopeMismatch
	}
	delegate, err := access.NewProjectPermissionPair(access.ActionProjectAccessDelegate, projectID)
	if err != nil {
		return fmt.Errorf("%w: delegate pair: %v", access.ErrGrantAdminEnvelopeMismatch, err)
	}
	required := make([]access.PermissionPair, 0, len(permissions)+1)
	required = append(required, delegate)
	required = append(required, permissions...)
	if h.CurrentEffectivePermissionOptions == nil {
		return access.ErrGrantAdminEnvelopeMismatch
	}
	authority, err := h.CurrentEffectivePermissionOptions(r.Context(), actorID)
	if err != nil {
		return fmt.Errorf("%w: current authority: %v", access.ErrGrantAdminEnvelopeMismatch, err)
	}
	if err := validateProjectRoleBindingPermissionCeiling(authority, required); err != nil {
		return fmt.Errorf("%w: %v", access.ErrGrantAdminEnvelopeMismatch, err)
	}

	if credential, found := h.currentCredential(r); found {
		if credential.Token.ID == "" {
			return access.ErrGrantAdminEnvelopeMismatch
		}
		if (credential.Principal.ID != "" && credential.Principal.ID != actorID) || credential.Token.PrincipalID != actorID {
			return access.ErrGrantAdminEnvelopeMismatch
		}
		issuer := envelope.Issuer.Credential
		if issuer.Class != access.GrantCredentialClassAPIToken || issuer.ID != credential.Token.ID || issuer.Fingerprint == "" || credential.Token.TokenFingerprint == "" || issuer.Fingerprint != credential.Token.TokenFingerprint {
			return access.ErrGrantAdminEnvelopeMismatch
		}
		if credential.Token.PermissionProfile != access.PermissionCatalogProfile || credential.Token.Permissions == nil {
			return fmt.Errorf("%w: %v", access.ErrGrantAdminEnvelopeMismatch, access.ErrTokenPermissionAttenuationNeeded)
		}
		if err := validateProjectRoleBindingPermissionCeiling(credential.Token.Permissions, required); err != nil {
			return fmt.Errorf("%w: %v", access.ErrGrantAdminEnvelopeMismatch, err)
		}
		return nil
	}

	// Browser sessions intentionally do not populate APICredential. Their
	// durable session ID is still an exact credential identity, while the
	// active typed snapshot supplies the current (non-attenuated) authority.
	if h.CurrentSession == nil {
		return access.ErrGrantAdminEnvelopeMismatch
	}
	sessionID, found := h.CurrentSession(r)
	issuer := envelope.Issuer.Credential
	if !found || sessionID == "" || issuer.Class != access.GrantCredentialClassSession || issuer.ID != sessionID {
		return access.ErrGrantAdminEnvelopeMismatch
	}
	return nil
}

func (h Handler) ListProjectRoleBindings(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	reader, ok := repo.(access.AuthorizationPolicyReader)
	if !ok {
		writeJSONError(w, errors.New("authorization policy reader is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	scope, err := h.authorizationPolicyScope(r)
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	policy, err := reader.AuthorizationPolicy(r.Context(), scope)
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(policy.RoleBindings))
	for _, binding := range policy.RoleBindings {
		items = append(items, roleBindingDTO(binding, policy))
	}
	page, next, ok := pageSliceForRequest(w, r, items)
	if !ok {
		return
	}
	response := roleBindingPolicyMetadata(scope, policy)
	response["items"] = page
	response["page"] = map[string]any{"nextCursor": next}
	writeJSON(w, stdhttp.StatusOK, response)
}

func (h Handler) CreateProjectRoleBinding(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var input roleBindingCreateRequest
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if input.ExpectedRevision == nil {
		writeJSONError(w, errors.New("expectedRevision is required"), stdhttp.StatusBadRequest)
		return
	}
	scope, err := h.authorizationPolicyScope(r)
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	subject, err := access.NewSubjectRef(access.SubjectKind(input.SubjectType), input.SubjectID)
	if err != nil {
		writeAuthorizationPolicyError(w, fmt.Errorf("%w: subject: %w", access.ErrAuthorizationPolicyInvalidBinding, err))
		return
	}
	binding, err := access.NewTypedRoleBinding(input.ID, input.Name, subject, access.PermissionRole(input.Role), projectgraph.ResourceID(scope.ProjectID))
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	if _, ok := repo.(access.AuthorizationPolicyWriter); !ok {
		writeJSONError(w, errors.New("authorization policy writer is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	var policy access.AuthorizationPolicy
	operation := accessgen.GenCommandOperationCreateProjectRoleBinding()
	err = executeAuditedMutation(r, repo, operation, func(tx access.Repository) (access.AuditEventInput, error) {
		writer, ok := tx.(access.AuthorizationPolicyWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional authorization policy writer is unavailable")
		}
		actorID := h.currentPrincipalID(r)
		bootstrap := false
		if input.GrantAdminEnvelopeID == "" && h.AuthorizeClaimBootstrapBinding != nil {
			var bootstrapErr error
			bootstrap, bootstrapErr = h.AuthorizeClaimBootstrapBinding(r, scope, binding, actorID)
			if bootstrapErr != nil {
				return access.AuditEventInput{}, bootstrapErr
			}
		}
		if !bootstrap {
			envelopes, ok := tx.(access.CurrentGrantAdminEnvelopeReader)
			if !ok {
				return access.AuditEventInput{}, errors.New("transactional grant administration authority is unavailable")
			}
			envelope, envelopeErr := envelopes.CurrentGrantAdminEnvelopeForMutation(r.Context(), input.GrantAdminEnvelopeID, actorID)
			if envelopeErr != nil {
				return access.AuditEventInput{}, envelopeErr
			}
			if envelopeErr := access.ValidateGrantAdminEnvelopeRoleBinding(envelope, actorID, projectgraph.ResourceID(scope.ProjectID), binding.Subject, binding.PermissionRole, binding.Permissions); envelopeErr != nil {
				return access.AuditEventInput{}, envelopeErr
			}
			if envelopeErr := h.authorizeProjectRoleBindingMutation(r, envelope, actorID, projectgraph.ResourceID(scope.ProjectID), binding.Permissions); envelopeErr != nil {
				return access.AuditEventInput{}, envelopeErr
			}
		}
		policy, err = writer.UpsertAuthorizationRoleBinding(r.Context(), access.AuthorizationRoleBindingInput{
			Scope: scope, Binding: binding, ExpectedRevision: *input.ExpectedRevision,
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		metadata, encodeErr := accessgen.EncodeGenCreateProjectRoleBindingAuditPayload(accessgen.GenSchemaRoleBindingAuditPayload{
			TargetId: scope.TargetID, ProjectId: scope.ProjectID, Environment: scope.Environment,
			BindingId: binding.ID, SubjectType: string(binding.Subject.Kind), SubjectId: binding.Subject.ID,
			Role: string(binding.PermissionRole), PolicyRevision: policy.Revision, PolicyDigest: policy.Digest,
		})
		if encodeErr != nil {
			return access.AuditEventInput{}, encodeErr
		}
		event := auditInput(r, "role_binding.created", h.currentPrincipalID(r), "role_binding", binding.ID, access.CapabilityProjectAdmin, "success", nil)
		event.MetadataJSON = metadata
		return event, nil
	})
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	for _, row := range policy.RoleBindings {
		if row.ID == binding.ID {
			writeJSON(w, stdhttp.StatusCreated, roleBindingDTO(row, policy))
			return
		}
	}
	writeJSONError(w, errors.New("created authorization role binding is missing from policy"), stdhttp.StatusInternalServerError)
}

func (h Handler) DeleteProjectRoleBinding(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var input roleBindingDeleteRequest
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if input.ExpectedRevision == nil {
		writeJSONError(w, errors.New("expectedRevision is required"), stdhttp.StatusBadRequest)
		return
	}
	bindingID := chi.URLParam(r, "binding")
	if err := access.ValidateAuthorizationRoleBindingID(bindingID); err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	scope, err := h.authorizationPolicyScope(r)
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	if _, ok := repo.(access.AuthorizationPolicyWriter); !ok {
		writeJSONError(w, errors.New("authorization policy writer is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	var policy access.AuthorizationPolicy
	operation := accessgen.GenCommandOperationDeleteProjectRoleBinding()
	err = executeAuditedMutation(r, repo, operation, func(tx access.Repository) (access.AuditEventInput, error) {
		reader, readOK := tx.(access.AuthorizationPolicyReader)
		writer, writeOK := tx.(access.AuthorizationPolicyWriter)
		if !readOK || !writeOK {
			return access.AuditEventInput{}, errors.New("transactional authorization policy reader/writer is unavailable")
		}
		// Read the caller-fenced immutable revision, not the mutable head. This
		// preserves the removed binding's audit fields for an exact idempotent
		// replay after the head has already advanced.
		current, readErr := reader.AuthorizationPolicyRevision(r.Context(), scope, *input.ExpectedRevision)
		if readErr != nil {
			return access.AuditEventInput{}, readErr
		}
		var removed access.RoleBinding
		found := false
		for _, row := range current.RoleBindings {
			if row.ID == bindingID {
				removed, found = row, true
				break
			}
		}
		if !found {
			return access.AuditEventInput{}, fmt.Errorf("%w: role binding %q", access.ErrAuthorizationPolicyNotFound, bindingID)
		}
		policy, err = writer.RemoveAuthorizationRoleBinding(r.Context(), access.AuthorizationRoleBindingDeleteInput{
			Scope: scope, BindingID: bindingID, ExpectedRevision: *input.ExpectedRevision,
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		role := string(removed.PermissionRole)
		if role == "" {
			role = string(removed.Role)
		}
		metadata, encodeErr := accessgen.EncodeGenDeleteProjectRoleBindingAuditPayload(accessgen.GenSchemaRoleBindingAuditPayload{
			TargetId: scope.TargetID, ProjectId: scope.ProjectID, Environment: scope.Environment,
			BindingId: removed.ID, SubjectType: string(removed.Subject.Kind), SubjectId: removed.Subject.ID,
			Role: role, PolicyRevision: policy.Revision, PolicyDigest: policy.Digest,
		})
		if encodeErr != nil {
			return access.AuditEventInput{}, encodeErr
		}
		event := auditInput(r, "role_binding.removed", h.currentPrincipalID(r), "role_binding", removed.ID, access.CapabilityProjectAdmin, "success", nil)
		event.MetadataJSON = metadata
		return event, nil
	})
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, roleBindingPolicyMetadata(scope, policy))
}

func writeAuthorizationPolicyError(w stdhttp.ResponseWriter, err error) {
	status := stdhttp.StatusInternalServerError
	switch {
	case errors.Is(err, access.ErrAuthorizationPolicyInvalidBinding):
		status = stdhttp.StatusBadRequest
	case errors.Is(err, access.ErrAuthorizationPolicyInvalidScope):
		status = stdhttp.StatusServiceUnavailable
	case errors.Is(err, access.ErrAuthorizationPolicyNotFound):
		status = stdhttp.StatusNotFound
	case errors.Is(err, access.ErrGrantNotFound), errors.Is(err, access.ErrGrantRevoked), errors.Is(err, access.ErrGrantExpired), errors.Is(err, access.ErrGrantPrincipalInactive), errors.Is(err, access.ErrGrantAdminEnvelopeMismatch):
		status = stdhttp.StatusForbidden
	case errors.Is(err, access.ErrAuthorizationPolicyConflict), errors.Is(err, access.ErrAuthorizationPolicyStaleRevision), errors.Is(err, access.ErrAuthorizationPolicyIdempotency):
		status = stdhttp.StatusConflict
	}
	writeJSONError(w, err, status)
}
