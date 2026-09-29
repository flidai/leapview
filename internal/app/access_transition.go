package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/deployment/module"
)

// AccessTransitionExecutionRequest is the private offline candidate handoff.
// Secrets are supplied separately from NativeRequest/journal files and are
// never copied to result or durable records.
type AccessTransitionExecutionRequest struct {
	Intent                     accessmodule.AccessTransitionIntent
	OperationID                string
	MaintenanceOperationDigest string
	RecoveryDigest             string
	StagedPolicyRevision       int64
	StagedPolicyDigest         string
	PublisherCredential        string
	ReviewerCredential         string
}

// AccessTransitionExecutionResult is the redacted native publication receipt
// consumed by the host's bounded activation waiter.
type AccessTransitionExecutionResult struct {
	OperationID                 string `json:"operationId"`
	IntentDigest                string `json:"intentDigest"`
	PlanID                      string `json:"planId"`
	CandidateID                 string `json:"candidateId"`
	GenerationID                string `json:"generationId"`
	PublicationID               string `json:"publicationId"`
	ApprovalRequestID           string `json:"approvalRequestId"`
	PlanPolicySnapshotDigest    string `json:"planPolicySnapshotDigest"`
	ServingPolicySnapshotDigest string `json:"servingPolicySnapshotDigest"`
	Status                      string `json:"status"`
}

// AccessTransitionRunner owns the offline candidate lifecycle separately
// from Application's HTTP and process lifecycle surface.
type AccessTransitionRunner struct {
	lifecycle *applicationLifecycleOwner
	execute   func(context.Context, AccessTransitionExecutionRequest) (AccessTransitionExecutionResult, error)
}

func (r *AccessTransitionRunner) Execute(ctx context.Context, request AccessTransitionExecutionRequest) (AccessTransitionExecutionResult, error) {
	if r == nil || r.lifecycle == nil || r.execute == nil {
		return AccessTransitionExecutionResult{}, errors.New("offline access-transition composition is unavailable")
	}
	r.lifecycle.mu.Lock()
	state := r.lifecycle.state
	r.lifecycle.mu.Unlock()
	if state != applicationIdle {
		return AccessTransitionExecutionResult{}, errors.New("offline access transition requires an unstarted candidate application")
	}
	return r.execute(ctx, request)
}

func (r *AccessTransitionRunner) Shutdown(ctx context.Context) error {
	if r == nil || r.lifecycle == nil {
		return nil
	}
	return r.lifecycle.shutdown(ctx)
}

func accessTransitionPermissionPairs(plan accessmodule.AccessTransitionPlan, principalID string) ([]access.PermissionPair, error) {
	var pairs []access.PermissionPair
	for _, binding := range plan.RoleBindings {
		if binding.Subject.Kind == access.SubjectKindPrincipal && binding.Subject.ID == principalID {
			pairs = append(pairs, access.ClonePermissionPairs(binding.Permissions)...)
		}
	}
	for _, grant := range plan.Grants {
		if grant.Subject.Kind == access.SubjectKindPrincipal && grant.Subject.ID == principalID {
			pairs = append(pairs, access.ClonePermissionPairs(grant.Permissions)...)
		}
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("transition principal %q has no typed permission scope", principalID)
	}
	byKey := make(map[string]access.PermissionPair, len(pairs))
	for _, pair := range pairs {
		if err := pair.Validate(); err != nil {
			return nil, err
		}
		byKey[pair.Key()] = pair
	}
	pairs = pairs[:0]
	for _, pair := range byKey {
		pairs = append(pairs, pair)
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Key() < pairs[j].Key() })
	if err := access.ValidatePermissionPairs(pairs); err != nil {
		return nil, err
	}
	return pairs, nil
}

func validateAccessTransitionOperators(plan accessmodule.AccessTransitionPlan) error {
	roleCounts := map[string]int{}
	for _, binding := range plan.RoleBindings {
		if binding.Subject.Kind != access.SubjectKindPrincipal {
			continue
		}
		switch binding.Subject.ID {
		case plan.PublisherPrincipalID:
			if binding.PermissionRole != access.PermissionRoleReleaseOperator || !binding.TypedRoleBinding() {
				return errors.New("transition publisher may receive only the typed release_operator role")
			}
			roleCounts[plan.PublisherPrincipalID]++
		case plan.ReviewerPrincipalID:
			if binding.PermissionRole != access.PermissionRoleReleaseApprover || !binding.TypedRoleBinding() {
				return errors.New("transition reviewer may receive only the typed release_approver role")
			}
			roleCounts[plan.ReviewerPrincipalID]++
		}
	}
	if roleCounts[plan.PublisherPrincipalID] != 1 || roleCounts[plan.ReviewerPrincipalID] != 1 {
		return errors.New("transition requires one explicit release_operator and one distinct release_approver assignment")
	}
	for _, grant := range plan.Grants {
		if grant.Subject.Kind == access.SubjectKindPrincipal && grant.Subject.ID == plan.ReviewerPrincipalID {
			return errors.New("transition reviewer may not receive extra resource grants")
		}
	}
	publisher, err := accessTransitionPermissionPairs(plan, plan.PublisherPrincipalID)
	if err != nil {
		return err
	}
	reviewer, err := accessTransitionPermissionPairs(plan, plan.ReviewerPrincipalID)
	if err != nil {
		return err
	}
	projectID := plan.ProjectID
	projectPermission := func(action access.Action) (access.PermissionPair, error) {
		return access.NewProjectPermissionPair(action, projectID)
	}
	publisherApproval, err := projectPermission(access.ActionDeliveryApprove)
	if err != nil {
		return err
	}
	reviewerPublish, err := projectPermission(access.ActionDeliveryPublish)
	if err != nil {
		return err
	}
	reviewerPlan, err := projectPermission(access.ActionDeliveryPlan)
	if err != nil {
		return err
	}
	reviewerBuild, err := projectPermission(access.ActionDeliveryBuild)
	if err != nil {
		return err
	}
	if access.PermissionSetAllows(publisher, publisherApproval) || access.PermissionSetAllows(reviewer, reviewerPublish) || access.PermissionSetAllows(reviewer, reviewerPlan) || access.PermissionSetAllows(reviewer, reviewerBuild) {
		return errors.New("transition publisher and reviewer role scopes violate separation of duties")
	}
	return nil
}

func accessTransitionDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range value[len("sha256:"):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

type immutableAuthorizationSnapshotDigestReader interface {
	AuthorizationSnapshotDigest(context.Context, string, string, string) (string, bool, error)
}

// resolveAccessTransitionSnapshotDigests preserves the immutable digest of the
// active legacy predecessor verbatim. A candidate has no snapshot row until
// activation in the normal lifecycle, so it is recompiled from its sealed
// artifact and compared with a stored serving digest only when one exists.
func resolveAccessTransitionSnapshotDigests(
	ctx context.Context,
	targetID, projectID, environment string,
	expectedGenerationID, activeGenerationID string,
	generationID, generationTargetID string,
	snapshots immutableAuthorizationSnapshotDigestReader,
	compileCandidate func(context.Context, string) (module.AccessTransitionSnapshotDigests, error),
) (module.AccessTransitionSnapshotDigests, error) {
	var empty module.AccessTransitionSnapshotDigests
	if ctx == nil || strings.TrimSpace(targetID) == "" || strings.TrimSpace(projectID) == "" || strings.TrimSpace(environment) == "" ||
		strings.TrimSpace(expectedGenerationID) == "" || strings.TrimSpace(activeGenerationID) == "" || strings.TrimSpace(generationID) == "" ||
		generationID != strings.TrimSpace(generationID) || generationTargetID != targetID || snapshots == nil || compileCandidate == nil {
		return empty, errors.New("access-transition snapshot identity is incomplete")
	}
	if activeGenerationID != expectedGenerationID && activeGenerationID != generationID {
		return empty, errors.New("access-transition snapshot target or active generation changed")
	}
	storedDigest, found, err := snapshots.AuthorizationSnapshotDigest(ctx, projectID, environment, generationID)
	if err != nil {
		return empty, err
	}
	if generationID == expectedGenerationID {
		if activeGenerationID != generationID || !found || !accessTransitionDigest(storedDigest) {
			return empty, errors.New("historical serving authorization snapshot identity is unavailable")
		}
		return module.AccessTransitionSnapshotDigests{ServingPolicySnapshotDigest: storedDigest}, nil
	}
	compiled, err := compileCandidate(ctx, generationID)
	if err != nil {
		return empty, err
	}
	if !accessTransitionDigest(compiled.PlanPolicySnapshotDigest) || !accessTransitionDigest(compiled.ServingPolicySnapshotDigest) || (found && storedDigest != compiled.ServingPolicySnapshotDigest) {
		return empty, errors.New("candidate authorization snapshot differs from its persisted immutable digest")
	}
	return compiled, nil
}

func accessTransitionExecutionFromModules(
	ctx context.Context,
	request AccessTransitionExecutionRequest,
	instanceID string,
	environment string,
	accessModule *accessmodule.Module,
	policyReader access.AuthorizationPolicyReader,
	deploymentModule *module.Module,
	candidateDigests func(context.Context, string) (module.AccessTransitionSnapshotDigests, error),
) (AccessTransitionExecutionResult, error) {
	var empty AccessTransitionExecutionResult
	if ctx == nil || accessModule == nil || policyReader == nil || deploymentModule == nil || candidateDigests == nil ||
		request.Intent.TargetID != instanceID || request.Intent.Environment != environment || request.StagedPolicyRevision <= request.Intent.ExpectedPolicyRevision ||
		!accessTransitionDigest(request.StagedPolicyDigest) || !accessTransitionDigest(request.RecoveryDigest) || !accessTransitionDigest(request.MaintenanceOperationDigest) ||
		request.OperationID != "access-transition:"+strings.TrimPrefix(request.MaintenanceOperationDigest, "sha256:") || request.PublisherCredential == "" || request.ReviewerCredential == "" {
		return empty, errors.New("offline access-transition execution fence is incomplete")
	}
	intentPlan, err := request.Intent.Plan()
	if err != nil {
		return empty, err
	}
	if err := validateAccessTransitionOperators(intentPlan); err != nil {
		return empty, err
	}
	scope := access.AuthorizationPolicyScope{TargetID: instanceID, ProjectID: intentPlan.ProjectID.String(), Environment: environment}
	currentPolicy, err := policyReader.AuthorizationPolicy(ctx, scope)
	if err != nil {
		return empty, err
	}
	if currentPolicy.Revision != request.StagedPolicyRevision || currentPolicy.Digest != request.StagedPolicyDigest {
		return empty, access.ErrAuthorizationPolicyStaleRevision
	}
	servingSnapshotDigests, err := candidateDigests(ctx, request.Intent.ExpectedServingGeneration)
	if err != nil {
		return empty, err
	}
	if servingSnapshotDigests.ServingPolicySnapshotDigest != request.Intent.ExpectedServingPolicyDigest {
		return empty, errors.New("legacy serving policy snapshot differs from the admitted transition baseline")
	}
	publisherPairs, err := accessTransitionPermissionPairs(intentPlan, intentPlan.PublisherPrincipalID)
	if err != nil {
		return empty, err
	}
	reviewerPairs, err := accessTransitionPermissionPairs(intentPlan, intentPlan.ReviewerPrincipalID)
	if err != nil {
		return empty, err
	}
	const credentialLifetime = 5 * time.Minute
	publisherLease, err := accessModule.AcquireWorkloadCredential(ctx, intentPlan.PublisherPrincipalID, request.PublisherCredential, instanceID, intentPlan.ProjectID.String(), publisherPairs, credentialLifetime)
	if err != nil {
		return empty, fmt.Errorf("authenticate admitted transition publisher: %w", err)
	}
	defer func() { _ = publisherLease.Revoke(context.Background()) }()
	reviewerLease, err := accessModule.AcquireWorkloadCredential(ctx, intentPlan.ReviewerPrincipalID, request.ReviewerCredential, instanceID, intentPlan.ProjectID.String(), reviewerPairs, credentialLifetime)
	if err != nil {
		return empty, fmt.Errorf("authenticate admitted transition reviewer: %w", err)
	}
	defer func() { _ = reviewerLease.Revoke(context.Background()) }()
	publisherEvidence, reviewerEvidence := publisherLease.Evidence(), reviewerLease.Evidence()
	if publisherEvidence.PrincipalID != intentPlan.PublisherPrincipalID || reviewerEvidence.PrincipalID != intentPlan.ReviewerPrincipalID || publisherEvidence.ID == reviewerEvidence.ID {
		return empty, access.ErrInvalidAuthoringCredential
	}
	publisherActor := module.ApprovalActor{PrincipalID: publisherEvidence.PrincipalID, CredentialClass: module.CredentialClass(publisherEvidence.Class), CredentialID: publisherEvidence.ID, CredentialExpiresAt: publisherEvidence.ExpiresAt}
	reviewerActor := module.ApprovalActor{PrincipalID: reviewerEvidence.PrincipalID, CredentialClass: module.CredentialClass(reviewerEvidence.Class), CredentialID: reviewerEvidence.ID, CredentialExpiresAt: reviewerEvidence.ExpiresAt}
	approvalContext := func(base context.Context, candidate module.AccessTransitionApprovalScope) (context.Context, module.AccessTransitionSnapshotDigests, error) {
		if candidate.TargetID != instanceID || candidate.ProjectID != intentPlan.ProjectID || candidate.Environment != environment ||
			candidate.ExpectedActiveGenerationID != request.Intent.ExpectedServingGeneration || candidate.IntentDigest != intentPlan.IntentDigest ||
			candidate.PublisherPrincipalID != intentPlan.PublisherPrincipalID || candidate.ReviewerPrincipalID != intentPlan.ReviewerPrincipalID {
			return base, module.AccessTransitionSnapshotDigests{}, errors.New("native approval scope differs from the admitted access transition")
		}
		latest, err := policyReader.AuthorizationPolicy(base, scope)
		if err != nil || latest.Revision != request.StagedPolicyRevision || latest.Digest != request.StagedPolicyDigest {
			return base, module.AccessTransitionSnapshotDigests{}, access.ErrAuthorizationPolicyStaleRevision
		}
		actualDigests, err := candidateDigests(base, candidate.CandidateGenerationID)
		if err != nil {
			return base, module.AccessTransitionSnapshotDigests{}, err
		}
		if actualDigests.PlanPolicySnapshotDigest != candidate.PlanPolicySnapshotDigest {
			return base, module.AccessTransitionSnapshotDigests{}, errors.New("candidate typed authorization policy differs from the persisted transition plan")
		}
		approvalContext, err := accessmodule.WithAccessTransitionApprovalAuthorization(base, accessmodule.AccessTransitionApprovalAuthorization{
			TargetID: candidate.TargetID, ProjectID: candidate.ProjectID, Environment: candidate.Environment,
			ExpectedActiveGenerationID: candidate.ExpectedActiveGenerationID, CandidateID: candidate.CandidateID,
			CandidateGenerationID: candidate.CandidateGenerationID, PublicationID: candidate.PublicationID,
			PublisherPrincipalID: candidate.PublisherPrincipalID, ReviewerPrincipalID: candidate.ReviewerPrincipalID,
			IntentDigest: candidate.IntentDigest, CandidateSnapshotDigest: actualDigests.ServingPolicySnapshotDigest,
		})
		if err != nil {
			return base, module.AccessTransitionSnapshotDigests{}, err
		}
		return approvalContext, actualDigests, nil
	}
	native, err := deploymentModule.ExecuteNativeAccessTransition(ctx, module.NativeAccessTransitionRequest{
		TargetID: instanceID, Environment: environment, ProjectID: intentPlan.ProjectID, IntentDigest: intentPlan.IntentDigest,
		ExpectedActiveGenerationID: request.Intent.ExpectedServingGeneration, PublisherPrincipalID: intentPlan.PublisherPrincipalID,
		ReviewerPrincipalID: intentPlan.ReviewerPrincipalID, OperationID: request.OperationID,
		PublisherActor: publisherActor, ReviewerActor: reviewerActor, ApprovalContext: approvalContext,
	})
	if err != nil {
		return empty, err
	}
	return AccessTransitionExecutionResult{
		OperationID: request.OperationID, IntentDigest: intentPlan.IntentDigest,
		PlanID: native.PlanID, CandidateID: native.CandidateID, GenerationID: native.GenerationID,
		PublicationID: native.PublicationID, ApprovalRequestID: native.ApprovalRequestID,
		PlanPolicySnapshotDigest: native.PlanPolicySnapshotDigest, ServingPolicySnapshotDigest: native.ServingPolicySnapshotDigest, Status: native.Status,
	}, nil
}
