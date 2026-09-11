package application

import (
	"context"
	"fmt"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
)

// validateAppendExploration checks the closed candidate against the exact
// current draft, then admits it against the active model and serving
// generation. The reducer repeats structural checks immediately before the
// compare-and-swap, so this validation never becomes an alternate write path.
func (a *Application) validateAppendExploration(ctx context.Context, project projectgraph.ResourceID, command authoring.Command, lifecycle authoring.DashboardLifecycle, payload *authoring.AppendExplorationVisualPayload) error {
	if payload == nil {
		return fmt.Errorf("%w: append exploration payload is required", authoring.ErrInvalidPayload)
	}
	if a.compiler == nil {
		return fmt.Errorf("dashboard authoring compiler is required for exploration append")
	}
	if err := command.Validate(); err != nil {
		return err
	}
	if lifecycle.ProjectID != project || lifecycle.ID != command.DashboardID {
		return fmt.Errorf("dashboard intent lifecycle identity does not match request")
	}
	if err := lifecycle.Validate(); err != nil {
		return fmt.Errorf("validate dashboard intent lifecycle: %w", err)
	}
	if lifecycle.Draft == nil || lifecycle.Draft.ID != command.DraftID || !sameRevision(lifecycle.Draft.Revision, command.ExpectedRevision) {
		return fmt.Errorf("%w: intent expected revision does not match current draft", authoring.ErrStaleRevision)
	}
	revision, err := a.repository.GetRevision(ctx, project, command.DashboardID, command.ExpectedRevision.RevisionID)
	if err != nil {
		return err
	}
	if err := revision.Validate(); err != nil {
		return fmt.Errorf("validate dashboard intent revision: %w", err)
	}
	if revision.DashboardID != command.DashboardID || !sameRevision(revision.Token(), command.ExpectedRevision) {
		return fmt.Errorf("%w: intent revision identity does not match request", authoring.ErrStaleRevision)
	}
	if revision.Document.Spec.SemanticModel != lifecycle.SemanticModel.String() || payload.SemanticModel != lifecycle.SemanticModel.String() {
		return fmt.Errorf("%w: append exploration semantic model does not match dashboard", authoring.ErrConflict)
	}
	candidate, err := authoring.CandidateAppendExplorationDocument(revision.Document, *payload)
	if err != nil {
		return err
	}
	lease, err := a.acquireRuntime(ctx)
	if err != nil {
		return err
	}
	if lease == nil {
		return fmt.Errorf("dashboard intent runtime lease is empty")
	}
	defer lease.Release()
	identity := lease.Identity()
	if err := identity.Validate(); err != nil || identity.GenerationID == "" || identity.ProjectID != project {
		return fmt.Errorf("dashboard intent serving-state identity is invalid")
	}
	runtime := lease.Runtime()
	if runtime == nil {
		return fmt.Errorf("dashboard intent runtime is empty")
	}
	active, ok := runtime.(interface {
		runtimehost.Runtime
		Identity() projectgraph.ServingIdentity
		SemanticModelProjection(projectgraph.ResourceID) (*semanticmodel.Model, bool)
	})
	if !ok || active == nil {
		return fmt.Errorf("active runtime does not provide serving identity and semantic model projection")
	}
	if active.Identity() != identity {
		return fmt.Errorf("active runtime serving identity does not match lease")
	}
	semanticModelID := projectgraph.ResourceID(payload.SemanticModel)
	model, ok := active.SemanticModelProjection(semanticModelID)
	if !ok || model == nil {
		return fmt.Errorf("semantic model %q is unavailable in active runtime", semanticModelID)
	}
	compiled, err := a.compiler.Compile(ctx, project, semanticModelID, candidate)
	if err != nil {
		return fmt.Errorf("compile appended exploration visual: %w", err)
	}
	if compiled.SemanticIdentity != identity {
		return fmt.Errorf("compiled appended visual serving identity %#v does not match active generation %#v", compiled.SemanticIdentity, identity)
	}
	return nil
}
