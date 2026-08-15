package resolver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
)

// PublishedCompilationReader is the minimal authoring capability needed by
// the runtime resolver. Implementations may be backed by SQLite or another
// repository; resolution does not depend on the repository implementation.
type PublishedCompilationReader interface {
	GetPublishedCompilation(context.Context, string, authoring.DashboardID) (authoring.CompiledRevision, error)
}

// SemanticModelProvider supplies models from the same active runtime lease as
// the project resolver. Keeping this interface small prevents a published
// dashboard from accidentally resolving its model from another generation.
type SemanticModelProvider interface {
	SemanticModel(modelID string) (*semanticmodel.Model, bool)
}

// PublishedCompilationResolver resolves the latest published compilation for
// one workspace, pinned to one active semantic serving state.
type PublishedCompilationResolver struct {
	workspaceID    string
	activeStateID  string
	reader         PublishedCompilationReader
	semanticModels SemanticModelProvider
}

// NewPublishedCompilationResolver constructs a resolver pinned to the active
// runtime lease. The reader is intentionally optional for project-only test
// composition; a nil reader resolves as ErrNotFound.
func NewPublishedCompilationResolver(workspaceID, activeServingStateID string, reader PublishedCompilationReader, semanticModels SemanticModelProvider) Resolver {
	return PublishedCompilationResolver{
		workspaceID:    strings.TrimSpace(workspaceID),
		activeStateID:  strings.TrimSpace(activeServingStateID),
		reader:         reader,
		semanticModels: semanticModels,
	}
}

func (r PublishedCompilationResolver) Resolve(dashboardID string) (Resolved, error) {
	// Resolver is intentionally synchronous today; use Background only at this
	// boundary. A future context-aware resolver contract can thread request
	// cancellation into repository reads without changing source pinning.
	workspaceID := strings.TrimSpace(r.workspaceID)
	id := strings.TrimSpace(dashboardID)
	if workspaceID == "" || id == "" || r.reader == nil {
		return Resolved{}, ErrNotFound
	}
	parsedID := authoring.DashboardID(id)
	if err := parsedID.Validate(); err != nil {
		return Resolved{}, fmt.Errorf("%w: invalid dashboard id %q", ErrNotFound, id)
	}
	compiled, err := r.reader.GetPublishedCompilation(context.Background(), workspaceID, parsedID)
	if err != nil {
		if errors.Is(err, authoring.ErrNotFound) {
			return Resolved{}, fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		return Resolved{}, err
	}
	if err := compiled.Validate(); err != nil {
		return Resolved{}, fmt.Errorf("%w: invalid published dashboard %q: %v", ErrNotFound, id, err)
	}
	if strings.TrimSpace(compiled.WorkspaceID) != workspaceID || compiled.DashboardID.String() != id || strings.TrimSpace(compiled.Definition.ID) != id {
		return Resolved{}, fmt.Errorf("%w: published dashboard %q is outside resolver scope", ErrScopeMismatch, id)
	}
	compiledStateID := strings.TrimSpace(compiled.SemanticServingStateID)
	if compiledStateID == "" || compiledStateID != r.activeStateID {
		return Resolved{}, fmt.Errorf("%w: dashboard %q compiled for %q, active %q", ErrStaleSemanticState, id, compiledStateID, r.activeStateID)
	}
	modelID := strings.TrimSpace(compiled.Definition.SemanticModel)
	if modelID == "" || r.semanticModels == nil {
		return Resolved{}, fmt.Errorf("%w: published dashboard %q has no semantic model", ErrNotFound, id)
	}
	model, ok := r.semanticModels.SemanticModel(modelID)
	if !ok || model == nil || strings.TrimSpace(model.Name) != modelID {
		return Resolved{}, fmt.Errorf("%w: published dashboard %q references semantic model %q", ErrNotFound, id, modelID)
	}
	return Resolved{
		Definition: compiled.Definition,
		Model:      model,
		Source: SourceMetadata{
			Kind:                   SourceWorkspace,
			WorkspaceID:            workspaceID,
			SemanticServingStateID: compiledStateID,
			AuthoredRevision: AuthoredRevisionEvidence{
				ID:          string(compiled.AuthoredRevision.RevisionID),
				Number:      compiled.AuthoredRevision.Number,
				ContentHash: compiled.AuthoredRevision.ContentHash,
			},
		},
	}, nil
}
