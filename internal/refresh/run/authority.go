package run

import (
	"context"
	"errors"
	"fmt"
	"strings"

	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/jobs"
)

func validatePipelineAuthorityTarget(authority jobs.AuthorityEnvelope, identity projectgraph.ServingIdentity, pipelineID projectgraph.ResourceID, principalID string) error {
	if authority.IsZero() {
		return nil
	}
	if err := authority.Validate(); err != nil {
		return err
	}
	if authority.Target.ProjectID != identity.ProjectID.String() || authority.Target.Environment != identity.Environment ||
		authority.Target.ResourceKind != string(projectgraph.KindPipeline) || authority.Target.ResourceID != pipelineID.String() {
		return errors.New("refresh authority target does not match pipeline identity")
	}
	if authority.ExecutionPrincipalID != strings.TrimSpace(principalID) {
		return errors.New("refresh authority execution principal does not match run principal")
	}
	return nil
}

// validateDelegatedExecutablePlan binds the complete canonical execution
// evidence to delegated authority. The pipeline-plan digest remains the
// generation-bound closure fence, while the explicit evidence digests prevent
// a caller from presenting a plan whose parameters, bindings, destination, or
// trigger meaning differs from the durable grant.
func validateDelegatedExecutablePlan(authority jobs.AuthorityEnvelope, plan projectpipelineplan.Plan) error {
	if authority.Mode != jobs.DelegatedWorkloadMode {
		return nil
	}
	if authority.ExecutionGrant == nil {
		return fmt.Errorf("delegated executable plan requires execution grant evidence")
	}
	if plan.Digest == "" {
		return fmt.Errorf("delegated executable plan digest is required")
	}
	if authority.ExecutionGrant.ClosureDigest != plan.Digest {
		return fmt.Errorf("delegated execution closure does not match executable pipeline plan")
	}
	if err := validateDelegatedClosureEvidence(authority, plan); err != nil {
		return err
	}
	return nil
}

func validateDelegatedClosureEvidence(authority jobs.AuthorityEnvelope, plan projectpipelineplan.Plan) error {
	evidence := authority.ExecutionGrant
	if evidence == nil {
		return fmt.Errorf("delegated execution grant evidence is required")
	}
	if plan.ParameterDigest == "" || plan.BindingDigest == "" || plan.DestinationDigest == "" || plan.TriggerDigest == "" {
		return fmt.Errorf("delegated executable plan closure evidence is incomplete")
	}
	if evidence.ParameterDigest == "" {
		return fmt.Errorf("delegated execution parameter evidence is unavailable")
	}
	if evidence.ParameterDigest != plan.ParameterDigest {
		return fmt.Errorf("delegated execution parameter evidence does not match executable plan")
	}
	if evidence.BindingDigest != plan.BindingDigest {
		return fmt.Errorf("delegated execution binding evidence does not match executable plan")
	}
	if evidence.DestinationDigest != plan.DestinationDigest {
		return fmt.Errorf("delegated execution destination evidence does not match executable plan")
	}
	if evidence.TriggerDigest != plan.TriggerDigest {
		return fmt.Errorf("delegated execution trigger evidence does not match executable plan")
	}
	if evidence.RunAsPrincipalID == "" || evidence.RunAsPrincipalID != plan.RunAsPrincipalID || plan.RunAsPrincipalID != authority.ExecutionPrincipalID {
		return fmt.Errorf("delegated execution run-as evidence does not match executable plan")
	}
	if evidence.Environment == "" || evidence.Environment != plan.Environment || evidence.Environment != authority.Target.Environment {
		return fmt.Errorf("delegated execution environment evidence does not match executable plan")
	}
	return nil
}

// revalidateBoundary turns an immutable queue envelope into a fresh product
// authorization decision immediately before each protected refresh unit.
func (s Service) revalidateBoundary(ctx context.Context, job JobRecord, boundary string) error {
	if job.Authority.IsZero() {
		if s.RequireAuthority {
			return fmt.Errorf("refresh authority is required before %s boundary", boundary)
		}
		return nil
	}
	if err := job.Authority.Validate(); err != nil {
		return fmt.Errorf("refresh authority before %s boundary: %w", boundary, err)
	}
	if s.AuthorityRevalidator == nil {
		return fmt.Errorf("refresh authority revalidator is required before %s boundary", boundary)
	}
	if err := s.AuthorityRevalidator.Revalidate(ctx, job.Authority); err != nil {
		return fmt.Errorf("refresh authority before %s boundary: %w", boundary, err)
	}
	if err := s.revalidateExecutableAuthority(ctx, job); err != nil {
		return fmt.Errorf("refresh executable authority before %s boundary: %w", boundary, err)
	}
	return nil
}
