// Package compileradapter bridges dashboard authoring to the active serving
// runtime. It compiles authored documents in memory against the semantic model
// owned by exactly one leased runtime generation; it never persists, publishes,
// activates, or creates runtime state.
package compileradapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

var (
	// ErrSemanticMismatch identifies an authored document or runtime model
	// crossing the requested semantic-model identity boundary.
	ErrSemanticMismatch = errors.New("dashboard authoring compiler semantic identity mismatch")
)

// Runtime is the only capability needed from an active runtime generation.
// SemanticModelProjection must return a detached model projection; the
// adapter never falls back to a mutable base-runtime model lookup.
type Runtime interface {
	projectruntime.Runtime
	SemanticModelProjection(graph.ResourceID) (*semanticmodel.Model, bool)
}

// Lease is the narrow identity-bearing runtime capability consumed by the
// compiler. The runtime host lease satisfies this contract structurally while
// keeping host topology and workspace lookup out of authoring.
type Lease = projectruntime.Lease

// AcquireRuntime acquires one active runtime lease for a project. Keeping
// acquisition as a callback leaves registry topology outside the authoring
// package while preserving the lease's generation and lifetime guarantees.
type AcquireRuntime func(context.Context, graph.ResourceID) (projectruntime.Lease, error)

// Options wires the runtime capability used by the adapter.
type Options struct {
	AcquireRuntime AcquireRuntime
}

// Adapter implements authoringservice.Compiler using one active runtime
// lease. Authorization is intentionally owned by the authoring service.
type Adapter struct {
	acquireRuntime AcquireRuntime
}

var _ authoringservice.Compiler = (*Adapter)(nil)

// New validates and returns a production compiler adapter.
func New(options Options) (*Adapter, error) {
	if options.AcquireRuntime == nil {
		return nil, fmt.Errorf("dashboard authoring compiler runtime provider is required")
	}
	return &Adapter{acquireRuntime: options.AcquireRuntime}, nil
}

// Compile strictly compiles one authored dashboard against the detached
// semantic model selected from the same active runtime lease. The lease is
// acquired once and released exactly once on every path after acquisition.
func (a *Adapter) Compile(ctx context.Context, projectID, semanticModelID graph.ResourceID, document authoring.Dashboard) (authoringservice.Compilation, error) {
	if a == nil || a.acquireRuntime == nil {
		return authoringservice.Compilation{}, fmt.Errorf("dashboard authoring compiler is not configured")
	}
	if err := projectID.Validate(); err != nil {
		return authoringservice.Compilation{}, fmt.Errorf("project id is required: %w", err)
	}
	if err := semanticModelID.Validate(); err != nil {
		return authoringservice.Compilation{}, fmt.Errorf("semantic model id is required: %w", err)
	}
	if !document.ID.Valid() {
		return authoringservice.Compilation{}, fmt.Errorf("dashboard id is required")
	}
	if strings.TrimSpace(document.SemanticModel.String()) == "" {
		return authoringservice.Compilation{}, fmt.Errorf("dashboard semantic model is required")
	}
	if document.SemanticModel != semanticModelID {
		return authoringservice.Compilation{}, fmt.Errorf("%w: authored semantic model %q does not match requested %q", ErrSemanticMismatch, document.SemanticModel, semanticModelID)
	}
	if err := ctx.Err(); err != nil {
		return authoringservice.Compilation{}, err
	}

	lease, err := a.acquireRuntime(ctx, projectID)
	if err != nil {
		return authoringservice.Compilation{}, err
	}
	if lease == nil {
		return authoringservice.Compilation{}, fmt.Errorf("dashboard authoring compiler runtime provider returned a nil lease")
	}
	defer lease.Release()

	active, ok := lease.Runtime().(Runtime)
	if !ok || active == nil {
		return authoringservice.Compilation{}, fmt.Errorf("dashboard authoring compiler active runtime does not provide semantic model projection")
	}
	model, ok := active.SemanticModelProjection(semanticModelID)
	if !ok || model == nil {
		return authoringservice.Compilation{}, fmt.Errorf("%w: semantic model %q is unavailable in active runtime", ErrSemanticMismatch, semanticModelID)
	}
	if model.Name != semanticModelID.String() {
		return authoringservice.Compilation{}, fmt.Errorf("%w: runtime semantic model %q does not match requested %q", ErrSemanticMismatch, model.Name, semanticModelID)
	}

	compiled, err := compiler.Compile(document, map[string]*semanticmodel.Model{semanticModelID.String(): model})
	if err != nil {
		return authoringservice.Compilation{}, fmt.Errorf("strictly compile dashboard: %w", err)
	}
	if compiled.Definition.ID != document.ID.String() {
		return authoringservice.Compilation{}, fmt.Errorf("%w: compiled dashboard id %q does not match authored id %q", authoring.ErrInvalidAuthoring, compiled.Definition.ID, document.ID)
	}
	if compiled.Definition.SemanticModel != semanticModelID.String() || compiled.Definition.SemanticModel != document.SemanticModel.String() {
		return authoringservice.Compilation{}, fmt.Errorf("%w: compiled semantic model %q does not match requested %q", ErrSemanticMismatch, compiled.Definition.SemanticModel, semanticModelID)
	}
	identity := lease.Identity()
	if err := identity.Validate(); err != nil {
		return authoringservice.Compilation{}, fmt.Errorf("dashboard authoring compiler serving identity does not match project: %w", err)
	}
	if identity.ProjectID != projectID {
		return authoringservice.Compilation{}, fmt.Errorf("dashboard authoring compiler serving identity project %q does not match %q", identity.ProjectID, projectID)
	}
	return authoringservice.Compilation{
		Definition:             compiled.Definition,
		SemanticServingStateID: strings.TrimSpace(identity.GenerationID),
	}, nil
}
