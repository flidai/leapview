package application

import (
	"context"
	"fmt"
	"slices"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (a *Application) prepareFilterScope(ctx context.Context, project projectgraph.ResourceID, command authoring.Command, lifecycle authoring.DashboardLifecycle) error {
	revision, err := a.validateIntentRevision(ctx, project, command, lifecycle)
	if err != nil {
		return err
	}
	model, err := a.semanticModelForRevision(ctx, revision)
	if err != nil {
		return err
	}
	if err := setCompatibleFilterScopeTargets(revision.Document, model, command.SetFilterScope); err != nil {
		return err
	}
	return validateFilterScopeCompatibility(lifecycle, revision, command, model)
}

func validateFilterScopeCompatibility(lifecycle authoring.DashboardLifecycle, revision authoring.Revision, command authoring.Command, model *semanticmodel.Model) error {
	// Compile the actual detached reduction before committing. Explicit visual
	// targets must not leave a saved draft with an invalid filter contract.
	_, candidate, err := authoring.ApplyEdit(lifecycle, revision, command, authoring.RevisionID(command.ID), revision.Number+1, revision.CreatedAt)
	if err != nil {
		return err
	}
	if _, err := compiler.CompileCanonicalDashboardBuilderFilters(revision.Document, model); err != nil {
		// Target derivation has already checked this scope. Other existing
		// invalid filters must remain repairable one at a time.
		return nil
	}
	if _, err := compiler.CompileCanonicalDashboardBuilderFilters(candidate.Document, model); err != nil {
		return fmt.Errorf("%w: filter scope is not compatible: %v", authoring.ErrInvalidPayload, err)
	}
	return nil
}

func setCompatibleFilterScopeTargets(doc document.DashboardDocument, model *semanticmodel.Model, patch *authoring.SetFilterScopePayload) error {
	dimension := ""
	for _, filter := range doc.Spec.Filters {
		if filter.ID == patch.FilterID {
			dimension = filter.Dimension
			break
		}
	}
	if dimension == "" {
		return fmt.Errorf("%w: filter %q", authoring.ErrNotFound, patch.FilterID)
	}
	if patch.Scope == "page" {
		index := slices.IndexFunc(doc.Spec.Pages, func(page document.DashboardPage) bool { return page.ID == patch.PageID })
		if index < 0 {
			return fmt.Errorf("%w: page %q", authoring.ErrNotFound, patch.PageID)
		}
		doc.Spec.Pages = doc.Spec.Pages[index : index+1]
	}
	targets, err := compiler.CompatibleDashboardFilterTargets(doc, dimension, model)
	if err != nil {
		return fmt.Errorf("%w: %v", authoring.ErrInvalidPayload, err)
	}
	allowed := make(map[string]bool)
	resolved := targets
	completeScope := len(targets) == len(doc.Spec.Visuals)
	if patch.Scope == "page" {
		resolved = nil
		completeScope = true
	}
	for _, page := range doc.Spec.Pages {
		for _, component := range page.Components {
			visual, ok := component.Value.(*document.VisualDashboardPageComponent)
			if !ok {
				continue
			}
			if !slices.Contains(targets, visual.Visual) {
				completeScope = false
				continue
			}
			base, err := component.Base()
			if err != nil {
				return err
			}
			if patch.Scope == "page" {
				allowed[base.ID] = true
				resolved = append(resolved, base.ID)
			} else {
				allowed[visual.Visual], allowed[page.ID+"/"+base.ID] = true, true
			}
		}
	}
	if len(patch.Targets) == 0 {
		// Keep whole-page/report intent when every consumer is compatible.
		// Explicit targets are only needed to exclude incompatible consumers.
		if completeScope {
			patch.Targets = nil
			return nil
		}
		patch.Targets = resolved
	} else {
		for _, target := range patch.Targets {
			if !allowed[target] {
				return fmt.Errorf("%w: filter %q does not apply to visual %q", authoring.ErrInvalidPayload, patch.FilterID, target)
			}
		}
	}
	return nil
}
