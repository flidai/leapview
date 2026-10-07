package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	"github.com/flidai/leapview/internal/release"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// candidateApprovalSnapshotDigest recomputes the typed authorization digest
// from one exact immutable serving artifact. Offline transition approval uses
// it to prove the approved candidate still contains the policy captured by
// the transition plan.
func candidateApprovalSnapshotDigest(
	ctx context.Context,
	states candidateApprovalServingStateReader,
	objects bundle.ArtifactObjectReader,
	generationID string,
) (string, error) {
	digests, err := candidateApprovalSnapshotDigests(ctx, states, objects, generationID)
	if err != nil {
		return "", err
	}
	return digests.ServingPolicySnapshotDigest, nil
}

// candidateApprovalSnapshotDigests computes both identities for the exact
// immutable candidate artifact. Plan authorization uses the stable
// candidate-policy identity; serving authorization is bound to the concrete
// generation ID. Snapshot.Digest includes its identity, so these fingerprints
// are expected to differ even when every policy entry is identical.
func candidateApprovalSnapshotDigests(
	ctx context.Context,
	states candidateApprovalServingStateReader,
	objects bundle.ArtifactObjectReader,
	generationID string,
) (module.AccessTransitionSnapshotDigests, error) {
	var empty module.AccessTransitionSnapshotDigests
	if states == nil || objects == nil || strings.TrimSpace(generationID) == "" {
		return empty, errors.New("candidate authorization snapshot dependencies are unavailable")
	}
	id := servingstate.ID(strings.TrimSpace(generationID))
	state, err := states.ByID(ctx, id)
	if err != nil {
		return empty, fmt.Errorf("read candidate approval generation: %w", err)
	}
	if state.ID != id || state.ProjectID.Validate() != nil || strings.TrimSpace(string(state.Environment)) == "" || (state.Status != servingstate.StatusValidated && state.Status != servingstate.StatusActive) {
		return empty, errors.New("candidate approval generation identity is invalid")
	}
	artifact, err := states.ArtifactByServingState(ctx, id)
	if err != nil {
		return empty, fmt.Errorf("read candidate approval artifact: %w", err)
	}
	if artifact.ServingStateID != id || artifact.Digest != state.Digest {
		return empty, errors.New("candidate approval artifact differs from its generation")
	}
	compiled, err := (bundle.ServingArtifactLoader{Objects: objects}).LoadCompiled(ctx, artifact, "")
	if err != nil {
		return empty, fmt.Errorf("load candidate approval artifact: %w", err)
	}
	if compiled.BundleDigest != state.ProjectDigest {
		return empty, errors.New("candidate approval compiled source bundle differs from its generation")
	}
	servingIdentity, err := projectgraph.NewServingIdentity(state.ProjectID, string(state.Environment), string(state.ID))
	if err != nil {
		return empty, fmt.Errorf("bind candidate approval identity: %w", err)
	}
	servingSnapshot, err := projectmodule.CompileAuthorizationSnapshotJSON(servingIdentity, compiled.Graph, state.AccessPolicyJSON)
	if err != nil {
		return empty, fmt.Errorf("compile candidate approval policy: %w", err)
	}
	servingDigest, err := servingSnapshot.Digest()
	if err != nil {
		return empty, fmt.Errorf("digest candidate serving approval policy: %w", err)
	}
	planningIdentity, err := projectgraph.NewServingIdentity(state.ProjectID, string(state.Environment), release.CandidatePolicyGenerationID)
	if err != nil {
		return empty, fmt.Errorf("bind candidate policy planning identity: %w", err)
	}
	planningSnapshot, err := projectmodule.CompileAuthorizationSnapshotJSON(planningIdentity, compiled.Graph, state.AccessPolicyJSON)
	if err != nil {
		return empty, fmt.Errorf("compile candidate policy planning snapshot: %w", err)
	}
	planningDigest, err := planningSnapshot.Digest()
	if err != nil {
		return empty, fmt.Errorf("digest candidate policy planning snapshot: %w", err)
	}
	return module.AccessTransitionSnapshotDigests{
		PlanPolicySnapshotDigest: planningDigest, ServingPolicySnapshotDigest: servingDigest,
	}, nil
}
