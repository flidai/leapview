package run

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/permissions"
)

// captureConnectionAuthority adds only server-derived dependencies, before the
// envelope is persisted. The live revalidator checks both current resource
// authority and the initiating credential's ceiling for the complete set.
func (s Service) captureConnectionAuthority(ctx context.Context, authority jobs.AuthorityEnvelope, connectionIDs []projectgraph.ResourceID) (jobs.AuthorityEnvelope, error) {
	if authority.IsZero() {
		return authority, nil
	}
	authority.Permissions = append([]permissions.Pair(nil), authority.Permissions...)
	if authority.Mode == jobs.CallerAuthorityMode {
		// Manual capture supplies the generated operation's root pair. Any
		// dependency permissions are selected here from the compiled closure.
		if len(authority.Permissions) != 1 || authority.Permissions[0].Action != permissions.Action(access.ActionPipelineRun) {
			return jobs.AuthorityEnvelope{}, errors.New("caller refresh capture requires only its pipeline permission")
		}
		for _, id := range connectionIDs {
			pair, err := connectionPermission(authority.Target.ProjectID, id)
			if err != nil {
				return jobs.AuthorityEnvelope{}, err
			}
			authority.Permissions = append(authority.Permissions, pair)
		}
	}
	if err := validateConnectionAuthority(authority, connectionIDs); err != nil {
		return jobs.AuthorityEnvelope{}, err
	}
	if s.AuthorityRevalidator == nil {
		return jobs.AuthorityEnvelope{}, jobs.ErrAuthorityRevalidator
	}
	if err := s.AuthorityRevalidator.Revalidate(ctx, authority); err != nil {
		return jobs.AuthorityEnvelope{}, err
	}
	return authority, nil
}

func connectionPermission(projectID string, id projectgraph.ResourceID) (permissions.Pair, error) {
	return permissions.NewExactPair(access.PermissionCatalogProfile, permissions.Action(access.ActionConnectionUse), projectID, permissions.Kind(projectgraph.KindConnection), id.String())
}

// Callers capture exactly the executable connection set. Delegated grants may
// carry additional permissions, but must explicitly contain every dependency;
// their entire immutable set is separately matched against the live grant.
func validateConnectionAuthority(authority jobs.AuthorityEnvelope, connectionIDs []projectgraph.ResourceID) error {
	if err := authority.Validate(); err != nil {
		return err
	}
	expected := make(map[permissions.Pair]struct{}, len(connectionIDs))
	for _, id := range connectionIDs {
		pair, err := connectionPermission(authority.Target.ProjectID, id)
		if err != nil {
			return err
		}
		expected[pair] = struct{}{}
	}
	for _, pair := range authority.Permissions {
		if pair.Action != permissions.Action(access.ActionConnectionUse) {
			continue
		}
		if _, ok := expected[pair]; !ok && authority.Mode == jobs.CallerAuthorityMode {
			return errors.New("captured connection authority exceeds executable closure")
		}
		delete(expected, pair)
	}
	if len(expected) != 0 {
		return errors.New("executable connection authority was not captured")
	}
	return nil
}

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
	// Publication may have installed the result generation before output.
	// Output consumes no source connection: recheck live captured authority
	// above, without requiring the retired base generation to remain active.
	if boundary == "output" {
		if job.PipelinePlan == nil {
			return errors.New("output executable plan evidence is required")
		}
		return validateDelegatedExecutablePlan(job.Authority, *job.PipelinePlan)
	}
	if err := s.revalidateExecutableAuthority(ctx, job); err != nil {
		return fmt.Errorf("refresh executable authority before %s boundary: %w", boundary, err)
	}
	return nil
}
