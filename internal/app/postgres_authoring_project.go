package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type postgresAuthoringServingStateReader interface {
	ActiveScopeForTarget(context.Context, string) (servingstate.ActiveScope, bool, error)
}

// postgresAuthoringProjectIDResolver binds authoring OAuth to durable
// bootstrap/serving evidence for the process-bound delivery target. A target
// with neither a project claim nor an active serving scope is genuinely fresh
// and returns an empty identity so a browser-approved login can precede the
// first claim/plan. Any active scope without a claim, a claim/scope
// disagreement, malformed evidence, or a read failure is treated as an
// authority error and fails closed.
func postgresAuthoringProjectIDResolver(
	claims deploymentmodule.ProjectClaimReader,
	serving postgresAuthoringServingStateReader,
	targetID string,
	environment servingstate.Environment,
) func(context.Context) (projectgraph.ResourceID, error) {
	return func(ctx context.Context) (projectgraph.ResourceID, error) {
		if claims == nil {
			return "", errors.New("authoring project claim reader is required")
		}
		if serving == nil {
			return "", errors.New("authoring serving-state reader is required")
		}
		if targetID == "" || targetID != strings.TrimSpace(targetID) {
			return "", errors.New("authoring serving-state target id is required")
		}

		claimed, found, err := readClaimedProject(claims, environment)(ctx)
		if err != nil {
			return "", fmt.Errorf("resolve authoring project claim: %w", err)
		}
		// Always read serving scopes, including when a durable claim exists. A
		// state read failure must not be hidden by a claim and a disagreement
		// between the two authorities is corruption, not a fresh target.
		activeScope, activeFound, err := serving.ActiveScopeForTarget(ctx, targetID)
		if err != nil {
			return "", fmt.Errorf("read active authoring serving scope for target %q: %w", targetID, err)
		}
		var active projectgraph.ResourceID
		if activeFound {
			if err := activeScope.ProjectID.Validate(); err != nil {
				return "", fmt.Errorf("active authoring serving scope project %q is invalid: %w", activeScope.ProjectID, err)
			}
			if activeScope.Environment != environment {
				return "", fmt.Errorf("active authoring serving scope environment %q does not match configured environment %q", activeScope.Environment, environment)
			}
			active = activeScope.ProjectID
		}

		if found {
			if err := claimed.Validate(); err != nil {
				return "", fmt.Errorf("durable authoring project claim is invalid: %w", err)
			}
			if active != "" && active != claimed {
				return "", fmt.Errorf("durable authoring project claim %q disagrees with active serving project %q", claimed, active)
			}
			return claimed, nil
		}
		if active != "" {
			return "", fmt.Errorf("active authoring serving project %q has no durable project claim", active)
		}
		return "", nil
	}
}
