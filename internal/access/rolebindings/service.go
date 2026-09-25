package rolebindings

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	access "github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const adminEnvelopeTTL = 5 * time.Minute

// Apply runs the common browser/API grant and revoke workflow. The runner
// binds the generated operation identity where there is one and commits the
// policy mutation and its audit event as one repository transaction.
func Apply(
	ctx context.Context,
	repository access.Repository,
	mutation access.RoleBindingAdministrationMutation,
	ports access.RoleBindingAdministrationPorts,
	run access.RoleBindingAdministrationAuditRunner,
) (access.AuthorizationPolicy, error) {
	if repository == nil || run == nil {
		return access.AuthorizationPolicy{}, errors.New("transactional role administration is unavailable")
	}
	if err := access.ValidateAuthorizationPolicyScope(mutation.Scope); err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if mutation.ActorID == "" {
		return access.AuthorizationPolicy{}, access.ErrForbidden
	}
	if mutation.ExpectedRevision < 0 {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: expected revision is invalid", access.ErrAuthorizationPolicyInvalidBinding)
	}
	idempotencyKey := strings.TrimSpace(mutation.IdempotencyKey)
	if idempotencyKey == "" || idempotencyKey != mutation.IdempotencyKey {
		return access.AuthorizationPolicy{}, errors.New("idempotency key is required")
	}

	// Keep envelope issuance outside the policy/audit transaction. The browser
	// workflow historically minted the short-lived durable envelope first, so
	// the envelope writer can use its own transaction without nesting a grant
	// write inside the policy transaction. Canonical project-claim bootstrap
	// remains the only no-envelope path.
	preparedEnvelopeID := mutation.GrantAdminEnvelopeID
	if mutation.Action == access.RoleBindingAdministrationGrant && preparedEnvelopeID == "" && mutation.IssueAdminEnvelope {
		if err := access.ValidateTypedRoleBindingForProject(mutation.Binding, projectgraph.ResourceID(mutation.Scope.ProjectID)); err != nil {
			return access.AuthorizationPolicy{}, err
		}
		if ports.EnvelopeIssuer == nil {
			return access.AuthorizationPolicy{}, errors.New("durable grant service is unavailable")
		}
		envelope, err := ports.EnvelopeIssuer.IssueGrantAdminEnvelope(ctx, access.GrantAdminEnvelopeRequest{
			TargetProjectID:   projectgraph.ResourceID(mutation.Scope.ProjectID),
			Permissions:       access.ClonePermissionPairs(mutation.Binding.Permissions),
			RecipientSelector: string(mutation.Binding.Subject.Kind) + ":" + mutation.Binding.Subject.ID,
			RoleVersion:       access.PermissionRoleVersion(mutation.Binding.PermissionRole),
			TTL:               adminEnvelopeTTL,
			IdempotencyKey:    idempotencyKey + ":envelope",
		})
		if err != nil {
			return access.AuthorizationPolicy{}, err
		}
		preparedEnvelopeID = envelope.ID
	}

	var policy access.AuthorizationPolicy
	err := run(func(tx access.Repository) (access.AuditEventInput, error) {
		if tx == nil {
			return access.AuditEventInput{}, errors.New("transactional role administration is unavailable")
		}
		switch mutation.Action {
		case access.RoleBindingAdministrationGrant:
			binding := mutation.Binding
			if err := access.ValidateTypedRoleBindingForProject(binding, projectgraph.ResourceID(mutation.Scope.ProjectID)); err != nil {
				return access.AuditEventInput{}, err
			}
			writer, writeOK := tx.(access.AuthorizationPolicyWriter)
			if !writeOK {
				return access.AuditEventInput{}, errors.New("transactional authorization policy writer is unavailable")
			}
			bootstrap := false
			if mutation.GrantAdminEnvelopeID == "" && !mutation.IssueAdminEnvelope && ports.AuthorizeClaimBootstrap != nil {
				var bootstrapErr error
				bootstrap, bootstrapErr = ports.AuthorizeClaimBootstrap(ctx, mutation.Scope, binding, mutation.ActorID)
				if bootstrapErr != nil {
					return access.AuditEventInput{}, bootstrapErr
				}
			}
			if !bootstrap {
				envelopes, envelopeOK := tx.(access.CurrentGrantAdminEnvelopeReader)
				if !envelopeOK {
					return access.AuditEventInput{}, errors.New("transactional grant administration authority is unavailable")
				}
				currentEnvelope, readErr := envelopes.CurrentGrantAdminEnvelopeForMutation(ctx, preparedEnvelopeID, mutation.ActorID)
				if readErr != nil {
					return access.AuditEventInput{}, readErr
				}
				if err := access.ValidateGrantAdminEnvelopeRoleBinding(currentEnvelope, mutation.ActorID, projectgraph.ResourceID(mutation.Scope.ProjectID), binding.Subject, binding.PermissionRole, binding.Permissions); err != nil {
					return access.AuditEventInput{}, err
				}
				if ports.ResolveCurrentAuthority == nil {
					return access.AuditEventInput{}, access.ErrGrantAdminEnvelopeMismatch
				}
				authority, authorityErr := ports.ResolveCurrentAuthority(ctx, mutation.ActorID)
				if authorityErr != nil {
					return access.AuditEventInput{}, fmt.Errorf("%w: current authority: %v", access.ErrGrantAdminEnvelopeMismatch, authorityErr)
				}
				if authority.ActorID != mutation.ActorID {
					return access.AuditEventInput{}, access.ErrGrantAdminEnvelopeMismatch
				}
				if err := authorizeGrant(authority, currentEnvelope, mutation.Scope, binding.Permissions); err != nil {
					return access.AuditEventInput{}, err
				}
			}

			updated, err := writer.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{
				Scope: mutation.Scope, Binding: binding, ExpectedRevision: mutation.ExpectedRevision,
				IdempotencyKey: idempotencyKey,
			})
			if err != nil {
				return access.AuditEventInput{}, err
			}
			policy = updated
			metadata, err := accessgen.EncodeGenCreateProjectRoleBindingAuditPayload(accessgen.GenSchemaRoleBindingAuditPayload{
				TargetId: mutation.Scope.TargetID, ProjectId: mutation.Scope.ProjectID, Environment: mutation.Scope.Environment,
				BindingId: binding.ID, SubjectType: string(binding.Subject.Kind), SubjectId: binding.Subject.ID,
				Role: string(binding.PermissionRole), PolicyRevision: policy.Revision, PolicyDigest: policy.Digest,
			})
			if err != nil {
				return access.AuditEventInput{}, err
			}
			return auditInput(mutation, "role_binding.created", binding.ID, metadata), nil

		case access.RoleBindingAdministrationRevoke:
			if err := access.ValidateAuthorizationRoleBindingID(mutation.BindingID); err != nil {
				return access.AuditEventInput{}, err
			}
			if ports.ResolveCurrentAuthority == nil {
				return access.AuditEventInput{}, access.ErrForbidden
			}
			authority, authorityErr := ports.ResolveCurrentAuthority(ctx, mutation.ActorID)
			if authorityErr != nil || authority.ActorID != mutation.ActorID {
				return access.AuditEventInput{}, access.ErrForbidden
			}
			if err := authorizeRevoke(authority, mutation.Scope); err != nil {
				return access.AuditEventInput{}, err
			}
			reader, readOK := tx.(access.AuthorizationPolicyReader)
			writer, writeOK := tx.(access.AuthorizationPolicyWriter)
			if !readOK || !writeOK {
				return access.AuditEventInput{}, errors.New("transactional authorization policy reader/writer is unavailable")
			}
			// Resolve audit fields from the immutable caller-fenced revision so an
			// exact idempotent retry can still describe the removed binding.
			current, err := reader.AuthorizationPolicyRevision(ctx, mutation.Scope, mutation.ExpectedRevision)
			if err != nil {
				return access.AuditEventInput{}, err
			}
			var removed access.RoleBinding
			for _, binding := range current.RoleBindings {
				if binding.ID == mutation.BindingID {
					removed = binding
					break
				}
			}
			if removed.ID == "" {
				return access.AuditEventInput{}, fmt.Errorf("%w: role binding %q", access.ErrAuthorizationPolicyNotFound, mutation.BindingID)
			}
			updated, err := writer.RemoveAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingDeleteInput{
				Scope: mutation.Scope, BindingID: mutation.BindingID, ExpectedRevision: mutation.ExpectedRevision,
				IdempotencyKey: idempotencyKey,
			})
			if err != nil {
				return access.AuditEventInput{}, err
			}
			policy = updated
			role := string(removed.PermissionRole)
			if role == "" {
				role = string(removed.Role)
			}
			metadata, err := accessgen.EncodeGenDeleteProjectRoleBindingAuditPayload(accessgen.GenSchemaRoleBindingAuditPayload{
				TargetId: mutation.Scope.TargetID, ProjectId: mutation.Scope.ProjectID, Environment: mutation.Scope.Environment,
				BindingId: removed.ID, SubjectType: string(removed.Subject.Kind), SubjectId: removed.Subject.ID,
				Role: role, PolicyRevision: policy.Revision, PolicyDigest: policy.Digest,
			})
			if err != nil {
				return access.AuditEventInput{}, err
			}
			return auditInput(mutation, "role_binding.removed", removed.ID, metadata), nil
		default:
			return access.AuditEventInput{}, errors.New("unknown role administration action")
		}
	})
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	return policy, nil
}

func authorizeGrant(authority access.RoleBindingAdministrationAuthority, envelope access.GrantAdminEnvelope, scope access.AuthorizationPolicyScope, permissions []access.PermissionPair) error {
	actorID := authority.ActorID
	if envelope.Issuer.PrincipalID != actorID {
		return access.ErrGrantAdminEnvelopeMismatch
	}
	projectID := projectgraph.ResourceID(scope.ProjectID)
	delegate, err := access.NewProjectPermissionPair(access.ActionProjectAccessDelegate, projectID)
	if err != nil {
		return fmt.Errorf("%w: delegate pair: %v", access.ErrGrantAdminEnvelopeMismatch, err)
	}
	// Check the delegation prerequisite independently from the selected role.
	// Project admin itself contains project.access.delegate, and a permission
	// set must not become invalid merely because those two requirements overlap.
	validateCeiling := func(ceiling []access.PermissionPair) error {
		if err := ValidatePermissionCeiling(ceiling, permissions); err != nil {
			return err
		}
		return ValidatePermissionCeiling(ceiling, []access.PermissionPair{delegate})
	}
	if err := validateCeiling(authority.Permissions); err != nil {
		return fmt.Errorf("%w: current authority: %v", access.ErrGrantAdminEnvelopeMismatch, err)
	}
	credential := authority.Credential
	issuer := envelope.Issuer.Credential
	if credential.AuthenticatedPrincipalID != "" && credential.AuthenticatedPrincipalID != actorID {
		return access.ErrGrantAdminEnvelopeMismatch
	}
	if credential.Class == access.GrantCredentialClassAPIToken {
		if credential.ID == "" || credential.PrincipalID != actorID || issuer.Class != credential.Class || issuer.ID != credential.ID || issuer.Fingerprint == "" || credential.Fingerprint == "" || issuer.Fingerprint != credential.Fingerprint {
			return access.ErrGrantAdminEnvelopeMismatch
		}
		if credential.PermissionProfile != access.PermissionCatalogProfile || credential.TokenPermissions == nil {
			return fmt.Errorf("%w: %v", access.ErrGrantAdminEnvelopeMismatch, access.ErrTokenPermissionAttenuationNeeded)
		}
		if err := validateCeiling(credential.TokenPermissions); err != nil {
			return fmt.Errorf("%w: %v", access.ErrGrantAdminEnvelopeMismatch, err)
		}
		return nil
	}
	if credential.Class != access.GrantCredentialClassSession || credential.ID == "" || issuer.Class != credential.Class || issuer.ID != credential.ID {
		return access.ErrGrantAdminEnvelopeMismatch
	}
	return nil
}

func authorizeRevoke(authority access.RoleBindingAdministrationAuthority, scope access.AuthorizationPolicyScope) error {
	manage, err := access.NewProjectPermissionPair(access.ActionProjectAccessManage, projectgraph.ResourceID(scope.ProjectID))
	if err != nil {
		return access.ErrForbidden
	}
	if ValidatePermissionCeiling(authority.Permissions, []access.PermissionPair{manage}) != nil {
		return access.ErrForbidden
	}
	credential := authority.Credential
	switch credential.Class {
	case access.GrantCredentialClassAPIToken:
		if credential.ID == "" || credential.PrincipalID != authority.ActorID || (credential.AuthenticatedPrincipalID != "" && credential.AuthenticatedPrincipalID != authority.ActorID) || credential.PermissionProfile != access.PermissionCatalogProfile || credential.TokenPermissions == nil || ValidatePermissionCeiling(credential.TokenPermissions, []access.PermissionPair{manage}) != nil {
			return access.ErrForbidden
		}
	case access.GrantCredentialClassSession:
		if credential.ID == "" || (credential.PrincipalID != "" && credential.PrincipalID != authority.ActorID) {
			return access.ErrForbidden
		}
	default:
		return access.ErrForbidden
	}
	return nil
}

// ValidatePermissionCeiling exposes the pair-level check for neighboring
// access handlers that must enforce the same exact/future-resource rules.
func ValidatePermissionCeiling(ceiling, requested []access.PermissionPair) error {
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

func auditInput(mutation access.RoleBindingAdministrationMutation, action, bindingID, metadata string) access.AuditEventInput {
	return access.AuditEventInput{
		PrincipalID: mutation.ActorID, Action: action, ResourceKind: "role_binding", ResourceID: bindingID,
		Capability: access.CapabilityProjectAdmin, Status: "success", RequestID: mutation.RequestID,
		CorrelationID: mutation.CorrelationID, MetadataJSON: metadata,
	}
}
