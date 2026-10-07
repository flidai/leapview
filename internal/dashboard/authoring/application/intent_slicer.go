package application

import (
	"context"
	"fmt"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (a *Application) prepareSlicerTargets(ctx context.Context, project projectgraph.ResourceID, command authoring.Command, lifecycle authoring.DashboardLifecycle, slicer *authoring.AddSlicerPayload) error {
	revision, err := a.validateIntentRevision(ctx, project, command, lifecycle)
	if err != nil {
		return err
	}
	model, err := a.semanticModelForRevision(ctx, revision)
	if err != nil {
		return err
	}
	return setCompatibleSlicerTargets(revision.Document, model, slicer)
}

func setCompatibleSlicerTargets(doc document.DashboardDocument, model *semanticmodel.Model, slicer *authoring.AddSlicerPayload) error {
	targets, err := compiler.CanonicalCompatibleFilterVisualTargets(doc, model, slicer.Dimension)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("%w: slicer dimension %q has no compatible visual targets", authoring.ErrInvalidPayload, slicer.Dimension)
	}
	slicer.Targets = targets
	return nil
}
