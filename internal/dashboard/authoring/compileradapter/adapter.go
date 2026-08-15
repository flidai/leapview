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
	"github.com/flidai/leapview/internal/runtimehost"
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
	runtimehost.Runtime
	SemanticModelProjection(string) (*semanticmodel.Model, bool)
}

// AcquireRuntime acquires one active runtime lease for a workspace. Keeping
// acquisition as a callback leaves registry topology outside the authoring
// package while preserving the lease's generation and lifetime guarantees.
type AcquireRuntime func(context.Context, string) (runtimehost.Lease, error)

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
func (a *Adapter) Compile(ctx context.Context, workspaceID, semanticModelID string, document authoring.Dashboard) (authoringservice.Compilation, error) {
	if a == nil || a.acquireRuntime == nil {
		return authoringservice.Compilation{}, fmt.Errorf("dashboard authoring compiler is not configured")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return authoringservice.Compilation{}, fmt.Errorf("workspace id is required")
	}
	semanticModelID = strings.TrimSpace(semanticModelID)
	if semanticModelID == "" {
		return authoringservice.Compilation{}, fmt.Errorf("semantic model id is required")
	}
	if strings.TrimSpace(document.ID) == "" {
		return authoringservice.Compilation{}, fmt.Errorf("dashboard id is required")
	}
	if strings.TrimSpace(document.SemanticModel) == "" {
		return authoringservice.Compilation{}, fmt.Errorf("dashboard semantic model is required")
	}
	if document.SemanticModel != semanticModelID {
		return authoringservice.Compilation{}, fmt.Errorf("%w: authored semantic model %q does not match requested %q", ErrSemanticMismatch, document.SemanticModel, semanticModelID)
	}
	if err := ctx.Err(); err != nil {
		return authoringservice.Compilation{}, err
	}

	lease, err := a.acquireRuntime(ctx, workspaceID)
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
	if model.Name != semanticModelID {
		return authoringservice.Compilation{}, fmt.Errorf("%w: runtime semantic model %q does not match requested %q", ErrSemanticMismatch, model.Name, semanticModelID)
	}

	compiled, err := compiler.Compile(document, map[string]*semanticmodel.Model{semanticModelID: model})
	if err != nil {
		return authoringservice.Compilation{}, fmt.Errorf("strictly compile dashboard: %w", err)
	}
	if compiled.Definition.ID != document.ID {
		return authoringservice.Compilation{}, fmt.Errorf("%w: compiled dashboard id %q does not match authored id %q", authoring.ErrInvalidAuthoring, compiled.Definition.ID, document.ID)
	}
	if compiled.Definition.SemanticModel != semanticModelID || compiled.Definition.SemanticModel != document.SemanticModel {
		return authoringservice.Compilation{}, fmt.Errorf("%w: compiled semantic model %q does not match requested %q", ErrSemanticMismatch, compiled.Definition.SemanticModel, semanticModelID)
	}
	servingStateID := strings.TrimSpace(string(lease.ServingStateID()))
	if servingStateID == "" {
		return authoringservice.Compilation{}, fmt.Errorf("dashboard authoring compiler serving-state identity is unavailable")
	}
	return authoringservice.Compilation{
		Definition:             compiled.Definition,
		SemanticServingStateID: servingStateID,
	}, nil
}
