package gcadapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/gc"
)

var (
	ErrRepairUnavailable = errors.New("delivery repair is unavailable")
	ErrRepairRootDrift   = errors.New("delivery repair root is not the durable SQLite root")
)

// RootReader is the read-only control-plane half of an operational repair.
// The production SQLite adapter satisfies this through gc.ControlPlane; the
// narrower interface keeps repair tests from needing mutation capabilities.
type RootReader interface {
	EnumerateRoots(context.Context, string, time.Time) (deployment.RootSet, error)
}

// RepairTool is an intentionally narrow, fail-closed repair seam. It first
// resolves the exact root from durable SQLite, then asks the target-owned
// inspector to verify immutable artifact bytes/digest and DuckLake closure.
// Only after both checks pass is the caller's control-plane mutation invoked.
// No object-store or DuckLake mutation capability is exposed here.
type RepairTool struct {
	Roots     RootReader
	Inspector gc.Inspector
	Now       func() time.Time
}

func NewRepairTool(roots RootReader, inspector gc.Inspector) (*RepairTool, error) {
	if roots == nil || inspector == nil {
		return nil, fmt.Errorf("%w: root reader and read-only inspector are required", ErrRepairUnavailable)
	}
	return &RepairTool{Roots: roots, Inspector: inspector, Now: func() time.Time { return time.Now().UTC() }}, nil
}

func (r *RepairTool) now() time.Time {
	if r != nil && r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// VerifyAndMutate performs all read-only checks before mutation. A root that
// is absent or differs from the durable SQLite row is rejected, even if an
// object with matching bytes exists. Unknown/ambiguous inspection never
// reaches mutate, leaving the existing root intact for later repair.
func (r *RepairTool) VerifyAndMutate(ctx context.Context, root deployment.DeliveryRoot, mutate func(context.Context, deployment.DeliveryRoot) error) error {
	if r == nil || r.Roots == nil || r.Inspector == nil || mutate == nil {
		return fmt.Errorf("%w: roots, inspector and mutation are required", ErrRepairUnavailable)
	}
	if root.PhysicalPoolID == "" || root.Kind == "" || root.SourceID == "" || root.CatalogDigest == "" || root.ObjectKey == "" {
		return fmt.Errorf("%w: root identity is incomplete", ErrRepairUnavailable)
	}
	roots, err := r.Roots.EnumerateRoots(ctx, root.PhysicalPoolID, r.now())
	if err != nil {
		return fmt.Errorf("%w: enumerate durable root: %v", ErrRepairRootDrift, err)
	}
	if roots.PhysicalPoolID != root.PhysicalPoolID || !containsRepairRoot(roots.Roots, root) {
		return fmt.Errorf("%w: requested root is absent or changed", ErrRepairRootDrift)
	}
	reach, err := r.Inspector.Inspect(ctx, root)
	if err != nil {
		return fmt.Errorf("%w: verify immutable artifact and closure: %v", ErrRepairUnavailable, err)
	}
	if reach.CatalogKey != root.ObjectKey || reach.CatalogDigest != root.CatalogDigest {
		return fmt.Errorf("%w: inspector returned a different artifact identity", ErrRepairUnavailable)
	}
	return mutate(ctx, root)
}

func containsRepairRoot(roots []deployment.DeliveryRoot, want deployment.DeliveryRoot) bool {
	for _, root := range roots {
		if root.PhysicalPoolID == want.PhysicalPoolID && root.Kind == want.Kind && root.SourceID == want.SourceID && root.CandidateID == want.CandidateID && root.GenerationID == want.GenerationID && root.LeaseID == want.LeaseID && root.CatalogDigest == want.CatalogDigest && root.ObjectKey == want.ObjectKey && root.Status == want.Status && root.CreatedAt.Equal(want.CreatedAt) && root.ExpiresAt.Equal(want.ExpiresAt) {
			return true
		}
	}
	return false
}
