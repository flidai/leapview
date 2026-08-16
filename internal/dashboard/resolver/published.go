package resolver

import (
	"context"
	"errors"
	"fmt"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// PublishedCompilationReader is the minimal authoring capability needed by
// the runtime resolver. Implementations may be backed by SQLite or another
// repository; resolution does not depend on the repository implementation.
type PublishedCompilationReader interface {
	GetPublishedCompilation(context.Context, projectgraph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error)
}

// SemanticModelProvider supplies models from the same active runtime lease as
// the project resolver. Keeping this interface small prevents a published
// dashboard from accidentally resolving its model from another generation.
type SemanticModelProvider interface {
	SemanticModelByID(projectgraph.ResourceID) (*semanticmodel.Model, bool)
}

// PublishedCompilationResolver resolves the latest instance compilation for
// one project, pinned to one active serving identity.
type PublishedCompilationResolver struct {
	identity       projectgraph.ServingIdentity
	reader         PublishedCompilationReader
	semanticModels SemanticModelProvider
}

// NewPublishedCompilationResolver constructs a resolver pinned to the active
// runtime lease. Reader and model provider are required capabilities.

func NewPublishedCompilationResolver(identity projectgraph.ServingIdentity, reader PublishedCompilationReader, semanticModels SemanticModelProvider) (Resolver, error) {
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("published resolver serving identity: %w", err)
	}
	if reader == nil || semanticModels == nil {
		return nil, fmt.Errorf("published resolver reader and semantic model provider are required")
	}
	return PublishedCompilationResolver{identity: identity, reader: reader, semanticModels: semanticModels}, nil
}

func (r PublishedCompilationResolver) Resolve(dashboardID projectgraph.ResourceID) (Resolved, error) {
	// Resolver is intentionally synchronous today; use Background only at this
	// boundary. A future context-aware resolver contract can thread request
	// cancellation into repository reads without changing source pinning.
	projectID := r.identity.ProjectID
	if !projectID.Valid() || !dashboardID.Valid() {
		return Resolved{}, ErrNotFound
	}
	parsedID := authoring.DashboardID(dashboardID.String())
	if err := parsedID.Validate(); err != nil {
		return Resolved{}, fmt.Errorf("%w: invalid dashboard id %q", ErrNotFound, dashboardID)
	}
	compiled, err := r.reader.GetPublishedCompilation(context.Background(), projectID, parsedID)
	if err != nil {
		if errors.Is(err, authoring.ErrNotFound) {
			return Resolved{}, fmt.Errorf("%w: %q", ErrNotFound, dashboardID)
		}
		return Resolved{}, err
	}
	if err := compiled.Validate(); err != nil {
		return Resolved{}, fmt.Errorf("%w: invalid published dashboard %q: %v", ErrNotFound, dashboardID, err)
	}
	if compiled.ProjectID != projectID || compiled.DashboardID != parsedID || compiled.Definition.ID != dashboardID.String() {
		return Resolved{}, fmt.Errorf("%w: published dashboard %q is outside resolver scope", ErrScopeMismatch, dashboardID)
	}
	if compiled.SemanticIdentity != r.identity {
		return Resolved{}, fmt.Errorf("%w: dashboard %q compiled for serving identity %#v, active %#v", ErrStaleSemanticState, dashboardID, compiled.SemanticIdentity, r.identity)
	}
	modelID := compiled.Definition.SemanticModel
	if modelID == "" {
		return Resolved{}, fmt.Errorf("%w: published dashboard %q has no semantic model", ErrNotFound, dashboardID)
	}
	modelIDValue, err := projectgraph.NewResourceID(modelID)
	if err != nil {
		return Resolved{}, fmt.Errorf("%w: published dashboard %q references invalid semantic model %q", ErrNotFound, dashboardID, modelID)
	}
	model, ok := r.semanticModels.SemanticModelByID(modelIDValue)
	if !ok || model == nil {
		return Resolved{}, fmt.Errorf("%w: published dashboard %q references semantic model %q", ErrNotFound, dashboardID, modelID)
	}
	return Resolved{
		Definition:      compiled.Definition,
		Model:           model,
		SemanticModelID: modelIDValue,
		Source: SourceMetadata{
			Kind:     SourceInstance,
			Identity: r.identity,
			AuthoredRevision: AuthoredRevisionEvidence{
				ID:          string(compiled.AuthoredRevision.RevisionID),
				Number:      compiled.AuthoredRevision.Number,
				ContentHash: compiled.AuthoredRevision.ContentHash,
			},
		},
	}, nil
}
