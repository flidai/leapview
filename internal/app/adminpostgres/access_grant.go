package adminpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
)

// StageAccessGrant is a trusted installation-operator boundary, not an HTTP
// delegation path. It only stages target policy. Normal graph admission,
// independent approval, and activation are required before it grants access.
// No token or synthetic runtime authority is created by this operation.
func (o Operations) StageAccessGrant(ctx context.Context, request admincli.StageAccessGrantRequest, out io.Writer) error {
	if out == nil {
		return errors.New("access grant output is required")
	}
	grant, err := request.Grant()
	if err != nil {
		return err
	}
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	configuration, err := accessConfigForAdmin(cfg)
	if err != nil {
		return err
	}
	pool, err := deps.OpenAccess(ctx, configuration)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := deps.VerifyBaseline(ctx, pool); err != nil {
		return err
	}
	bootstrap := platformbootstrap.New(pool)
	instanceID, err := bootstrap.InstanceID(ctx)
	if err != nil {
		return err
	}
	environment, err := bootstrap.InstanceEnvironment(ctx)
	if err != nil {
		return err
	}
	claim, err := bootstrap.GetProjectClaim(ctx)
	if err != nil {
		return err
	}
	if err := validateGrantClaim(claim, request.ProjectID, environment, cfg.Environment); err != nil {
		return err
	}
	scope := access.AuthorizationPolicyScope{TargetID: instanceID, ProjectID: claim.ProjectID, Environment: environment}
	key, err := accessFingerprintKey(cfg)
	if err != nil {
		return err
	}
	repo, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: key})
	if err != nil {
		return err
	}
	policy, err := stageOperatorGrant(ctx, repo, scope, grant, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		TargetID            string `json:"targetId"`
		ProjectID           string `json:"projectId"`
		Environment         string `json:"environment"`
		PolicyRevision      int64  `json:"policyRevision"`
		PolicyDigest        string `json:"policyDigest"`
		Applied             bool   `json:"applied"`
		RequiresPublication bool   `json:"requiresPublication"`
	}{instanceID, scope.ProjectID, environment, policy.Revision, policy.Digest, request.Apply, true})
}

func validateGrantClaim(claim platformbootstrap.ProjectClaim, projectID, environment, configuredEnvironment string) error {
	if claim.Validate() != nil || claim.ProjectID != projectID || claim.Environment != environment || environment != configuredEnvironment {
		return errors.New("grant scope differs from the immutable Project claim or instance environment")
	}
	return nil
}

func stageOperatorGrant(ctx context.Context, repo access.Repository, scope access.AuthorizationPolicyScope, grant access.AuthorizationGrant, request admincli.StageAccessGrantRequest) (access.AuthorizationPolicy, error) {
	validateRecipient := func(reader access.Repository) error {
		principal, err := reader.PrincipalByID(ctx, grant.Subject.ID)
		if err != nil {
			return err
		}
		if principal.AccessDisabled() {
			return access.ErrForbidden
		}
		return nil
	}
	if !request.Apply {
		if err := validateRecipient(repo); err != nil {
			return access.AuthorizationPolicy{}, err
		}
		reader, ok := repo.(access.AuthorizationPolicyReader)
		if !ok {
			return access.AuthorizationPolicy{}, errors.New("target policy reader is unavailable")
		}
		policy, err := reader.AuthorizationPolicy(ctx, scope)
		if err != nil {
			return access.AuthorizationPolicy{}, err
		}
		if policy.Revision != request.ExpectedRevision {
			return access.AuthorizationPolicy{}, access.ErrAuthorizationPolicyStaleRevision
		}
		return policy, nil
	}
	audited, ok := repo.(access.AuditedMutationRepository)
	if !ok {
		return access.AuthorizationPolicy{}, errors.New("atomic access audit is unavailable")
	}
	var policy access.AuthorizationPolicy
	err := audited.RunAuditedMutation(ctx, func(tx access.Repository) (access.AuditEventInput, error) {
		if err := validateRecipient(tx); err != nil {
			return access.AuditEventInput{}, err
		}
		writer, ok := tx.(access.AuthorizationGrantWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("target policy grant writer is unavailable")
		}
		var err error
		policy, err = writer.UpsertAuthorizationGrant(ctx, access.AuthorizationGrantInput{Scope: scope, Grant: grant, ExpectedRevision: request.ExpectedRevision, IdempotencyKey: request.OperationID})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		metadata, err := json.Marshal(map[string]any{"surface": "offline_operator", "operationId": request.OperationID, "recipientId": grant.Subject.ID, "permissions": grant.Permissions, "policyRevision": policy.Revision, "policyDigest": policy.Digest})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		// The trusted host operator is not impersonated as the recipient or an
		// authenticated application user; the audit surface records that boundary.
		return access.AuditEventInput{ProjectID: scope.ProjectID, Action: "grant.staged", ResourceKind: "grant", ResourceID: grant.ID, Status: "success", MetadataJSON: string(metadata)}, nil
	})
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("stage exact operator grant: %w", err)
	}
	return policy, nil
}
