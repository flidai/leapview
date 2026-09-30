package adminpostgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
)

// AccessTransitionAuthorization is the exact maintenance fence required to
// apply legacy-to-typed authority intent. The host adapter verifies these
// values against its admitted operation and held lifecycle lock.
type AccessTransitionAuthorization struct {
	TargetID                    string
	ProjectID                   string
	Environment                 string
	MaintenanceOperationID      string
	MaintenanceOperationDigest  string
	OperationID                 string
	ExpectedPolicyRevision      int64
	ExpectedPolicyDigest        string
	ExpectedServingGeneration   string
	ExpectedServingPolicyDigest string
	PublisherPrincipalID        string
	ReviewerPrincipalID         string
	IntentDigest                string
}

// StageAccessTransition applies explicit typed roles and exact-resource
// grants to an existing target-owned policy in one audited PostgreSQL
// transaction. This only stages target policy; publication and independent
// approval must capture and activate the new policy before it is serving.
func (o Operations) StageAccessTransition(ctx context.Context, request admincli.StageAccessTransitionRequest, out io.Writer) error {
	if out == nil {
		return errors.New("access transition output is required")
	}
	plan, err := request.Plan()
	if err != nil {
		return err
	}
	deps := o.Dependencies.withDefaults()
	if request.Apply && deps.AuthorizeAccessTransition == nil {
		return errors.New("admitted maintenance access-transition verifier is unavailable")
	}
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Production {
		return errors.New("legacy access transition is available only for a production installation")
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
	if request.Intent.TargetID != instanceID || request.Intent.Environment != environment {
		return errors.New("access transition intent target or environment differs from the durable instance claim")
	}
	claim, err := bootstrap.GetProjectClaim(ctx)
	if err != nil {
		return err
	}
	if err := validateGrantClaim(claim, request.Intent.ProjectID, environment, cfg.Environment); err != nil {
		return err
	}
	scope := access.AuthorizationPolicyScope{TargetID: instanceID, ProjectID: claim.ProjectID, Environment: environment}
	if request.Apply {
		authorization := AccessTransitionAuthorization{
			TargetID: instanceID, ProjectID: scope.ProjectID, Environment: environment,
			MaintenanceOperationID:      request.MaintenanceOperationID,
			MaintenanceOperationDigest:  request.MaintenanceOperationDigest,
			OperationID:                 request.OperationID,
			ExpectedPolicyRevision:      request.Intent.ExpectedPolicyRevision,
			ExpectedPolicyDigest:        request.Intent.ExpectedPolicyDigest,
			ExpectedServingGeneration:   request.Intent.ExpectedServingGeneration,
			ExpectedServingPolicyDigest: request.Intent.ExpectedServingPolicyDigest,
			PublisherPrincipalID:        plan.PublisherPrincipalID,
			ReviewerPrincipalID:         plan.ReviewerPrincipalID,
			IntentDigest:                plan.IntentDigest,
		}
		if err := deps.AuthorizeAccessTransition(ctx, authorization); err != nil {
			return fmt.Errorf("verify admitted maintenance access transition: %w", err)
		}
	}
	key, err := accessFingerprintKey(cfg)
	if err != nil {
		return err
	}
	repo, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: key})
	if err != nil {
		return err
	}
	policy, err := stageOperatorAccessTransition(ctx, repo, scope, request, plan)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		TargetID                   string `json:"targetId"`
		ProjectID                  string `json:"projectId"`
		Environment                string `json:"environment"`
		MaintenanceOperationID     string `json:"maintenanceOperationId"`
		MaintenanceOperationDigest string `json:"maintenanceOperationDigest"`
		IntentDigest               string `json:"intentDigest"`
		PolicyRevision             int64  `json:"policyRevision"`
		PolicyDigest               string `json:"policyDigest"`
		Applied                    bool   `json:"applied"`
		RequiresPublication        bool   `json:"requiresPublication"`
	}{instanceID, scope.ProjectID, environment, request.MaintenanceOperationID, request.MaintenanceOperationDigest, plan.IntentDigest, policy.Revision, policy.Digest, request.Apply, true})
}

func stageOperatorAccessTransition(
	ctx context.Context,
	repo access.Repository,
	scope access.AuthorizationPolicyScope,
	request admincli.StageAccessTransitionRequest,
	plan admincli.AccessTransitionPlan,
) (access.AuthorizationPolicy, error) {
	validateRecipients := func(reader access.Repository) error {
		if err := validateTransitionPrincipal(ctx, reader, plan.PublisherPrincipalID); err != nil {
			return fmt.Errorf("transition publisher: %w", err)
		}
		if err := validateTransitionPrincipal(ctx, reader, plan.ReviewerPrincipalID); err != nil {
			return fmt.Errorf("transition reviewer: %w", err)
		}
		for _, binding := range plan.RoleBindings {
			if err := validateTransitionPrincipal(ctx, reader, binding.Subject.ID); err != nil {
				return err
			}
		}
		for _, grant := range plan.Grants {
			if err := validateTransitionPrincipal(ctx, reader, grant.Subject.ID); err != nil {
				return err
			}
		}
		return nil
	}
	if !request.Apply {
		if err := validateRecipients(repo); err != nil {
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
		if policy.Revision != request.Intent.ExpectedPolicyRevision || policy.Digest != request.Intent.ExpectedPolicyDigest {
			return access.AuthorizationPolicy{}, access.ErrAuthorizationPolicyStaleRevision
		}
		if err := validateTransitionDerivedGrantIDs(request, plan, policy); err != nil {
			return access.AuthorizationPolicy{}, err
		}
		return policy, nil
	}
	audited, ok := repo.(access.AuditedMutationBatchRepository)
	if !ok {
		return access.AuthorizationPolicy{}, errors.New("atomic access transition audit is unavailable")
	}
	var result access.AuthorizationPolicy
	err := audited.RunAuditedMutationBatch(ctx, func(tx access.Repository) ([]access.AuditEventInput, error) {
		if err := validateRecipients(tx); err != nil {
			return nil, err
		}
		roleWriter, hasRoles := tx.(access.AuthorizationPolicyWriter)
		grantWriter, hasGrants := tx.(access.AuthorizationGrantWriter)
		if len(plan.RoleBindings) > 0 && (!hasRoles || roleWriter == nil) {
			return nil, errors.New("typed authorization role writer is unavailable")
		}
		if len(plan.Grants) > 0 && (!hasGrants || grantWriter == nil) {
			return nil, errors.New("typed authorization grant writer is unavailable")
		}
		reader, ok := tx.(access.AuthorizationPolicyReader)
		if !ok {
			return nil, errors.New("target policy reader is unavailable in the audited transaction")
		}
		baseline, err := reader.AuthorizationPolicy(ctx, scope)
		if err != nil {
			return nil, err
		}
		if baseline.Revision < request.Intent.ExpectedPolicyRevision {
			return nil, access.ErrAuthorizationPolicyStaleRevision
		}
		// On first apply, bind the CAS revision to the policy digest admitted by
		// the host journal. A later revision can only succeed as an exact replay
		// of the persisted row idempotency commands below.
		if baseline.Revision == request.Intent.ExpectedPolicyRevision && baseline.Digest != request.Intent.ExpectedPolicyDigest {
			return nil, access.ErrAuthorizationPolicyStaleRevision
		}
		if err := validateTransitionDerivedGrantIDs(request, plan, baseline); err != nil {
			return nil, err
		}
		expectedRevision := request.Intent.ExpectedPolicyRevision
		for _, binding := range plan.RoleBindings {
			policy, err := roleWriter.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{
				Scope: scope, Binding: binding, ExpectedRevision: expectedRevision,
				IdempotencyKey: transitionMutationKey(request.OperationID, plan.IntentDigest, "role", binding.ID),
			})
			if err != nil {
				return nil, fmt.Errorf("stage typed role %q: %w", binding.ID, err)
			}
			result = policy
			expectedRevision++
		}
		for _, grant := range plan.Grants {
			policy, err := grantWriter.UpsertAuthorizationGrant(ctx, access.AuthorizationGrantInput{
				Scope: scope, Grant: grant, ExpectedRevision: expectedRevision,
				IdempotencyKey: transitionMutationKey(request.OperationID, plan.IntentDigest, "grant", grant.ID),
			})
			if err != nil {
				return nil, fmt.Errorf("stage exact grant %q: %w", grant.ID, err)
			}
			result = policy
			expectedRevision++
		}
		current, err := reader.AuthorizationPolicy(ctx, scope)
		if err != nil {
			return nil, err
		}
		if current.Revision != result.Revision || current.Digest != result.Digest {
			return nil, fmt.Errorf("target policy advanced after transition intent: %w", access.ErrAuthorizationPolicyStaleRevision)
		}
		result = current
		metadata, err := transitionAuditMetadata(request, plan, current)
		if err != nil {
			return nil, err
		}
		return []access.AuditEventInput{{
			ProjectID: scope.ProjectID, Action: "access.transition.staged",
			ResourceKind: "authorization_policy", ResourceID: scope.ProjectID,
			Status: "success", MetadataJSON: metadata,
		}}, nil
	})
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("stage audited typed access transition: %w", err)
	}
	return result, nil
}

// Generated row IDs must not silently take ownership of an unrelated grant.
// Explicit single-action IDs retain the operator's existing upsert semantics.
func validateTransitionDerivedGrantIDs(request admincli.StageAccessTransitionRequest, plan admincli.AccessTransitionPlan, baseline access.AuthorizationPolicy) error {
	explicitIDs := make(map[string]bool, len(request.Intent.Grants))
	for _, intent := range request.Intent.Grants {
		if len(intent.Actions) == 1 {
			explicitIDs[intent.GrantID] = true
		}
	}
	existing := make(map[string]access.AuthorizationGrant, len(baseline.Grants))
	for _, grant := range baseline.Grants {
		existing[grant.ID] = grant
	}
	for _, grant := range plan.Grants {
		prior, present := existing[grant.ID]
		if explicitIDs[grant.ID] || !present {
			continue
		}
		if prior.Subject != grant.Subject || prior.Resource.ID() != grant.Resource.ID() || prior.Resource.Kind() != grant.Resource.Kind() ||
			prior.PermissionProfile != grant.PermissionProfile || prior.Capability != grant.Capability ||
			len(prior.Permissions) != 1 || len(grant.Permissions) != 1 || prior.Permissions[0] != grant.Permissions[0] {
			return fmt.Errorf("derived transition grant %q conflicts with existing policy authority", grant.ID)
		}
	}
	return nil
}

func validateTransitionPrincipal(ctx context.Context, repo access.Repository, principalID string) error {
	principal, err := repo.PrincipalByID(ctx, principalID)
	if err != nil {
		return err
	}
	if principal.AccessDisabled() {
		return access.ErrForbidden
	}
	return nil
}

func transitionMutationKey(operationID, intentDigest, kind, assignmentID string) string {
	payload := "leapview/access-transition-row/v1\x00" + operationID + "\x00" + intentDigest + "\x00" + kind + "\x00" + assignmentID
	digest := sha256.Sum256([]byte(payload))
	return "access-transition-v1-" + hex.EncodeToString(digest[:])
}

func transitionAuditMetadata(request admincli.StageAccessTransitionRequest, plan admincli.AccessTransitionPlan, policy access.AuthorizationPolicy) (string, error) {
	roles := make([]map[string]string, 0, len(plan.RoleBindings))
	for _, binding := range plan.RoleBindings {
		roles = append(roles, map[string]string{"bindingId": binding.ID, "principalId": binding.Subject.ID, "role": string(binding.PermissionRole)})
	}
	grants := make([]map[string]any, 0, len(plan.Grants))
	for _, grant := range plan.Grants {
		actions := make([]string, 0, len(grant.Permissions))
		for _, pair := range grant.Permissions {
			actions = append(actions, string(pair.Action))
		}
		grants = append(grants, map[string]any{"grantId": grant.ID, "principalId": grant.Subject.ID, "resourceId": grant.Resource.ID(), "resourceKind": grant.Resource.Kind(), "actions": actions})
	}
	metadata, err := json.Marshal(map[string]any{
		"surface": "admitted_maintenance_operator", "maintenanceOperationId": request.MaintenanceOperationID,
		"maintenanceOperationDigest": request.MaintenanceOperationDigest, "operationId": request.OperationID,
		"targetId": plan.TargetID, "environment": plan.Environment,
		"intentDigest": plan.IntentDigest, "expectedPolicyRevision": request.Intent.ExpectedPolicyRevision,
		"expectedPolicyDigest":        request.Intent.ExpectedPolicyDigest,
		"expectedServingGeneration":   request.Intent.ExpectedServingGeneration,
		"expectedServingPolicyDigest": request.Intent.ExpectedServingPolicyDigest,
		"policyRevision":              policy.Revision, "policyDigest": policy.Digest,
		"publisherPrincipalId": plan.PublisherPrincipalID, "reviewerPrincipalId": plan.ReviewerPrincipalID,
		"roleBindings": roles, "grants": grants,
	})
	if err != nil {
		return "", err
	}
	return string(metadata), nil
}
