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

// validateDelegatedExecutablePlan binds the only executable-closure identity
// currently exposed by the refresh contract to delegated authority. The
// pipeline-plan digest is the canonical identity over the generation-bound
// execution selection, its artifact/source provenance, and invocation-bound
// plan identity. It is therefore the provable closure fence for refresh work.
//
// Binding, destination, and trigger digests are deliberately not compared
// here: the refresh artifact/plan contracts do not expose standalone values
// for those meanings. Treating another component digest as one of them would
// silently change the authority contract.
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
