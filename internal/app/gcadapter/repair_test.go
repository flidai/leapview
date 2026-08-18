package gcadapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/gc"
)

type repairRootReader struct {
	set deployment.RootSet
	err error
}

func (r repairRootReader) EnumerateRoots(context.Context, string, time.Time) (deployment.RootSet, error) {
	return r.set, r.err
}

type repairInspector struct {
	reach gc.CatalogReachability
	err   error
	calls int
}

func (i *repairInspector) Inspect(context.Context, deployment.DeliveryRoot) (gc.CatalogReachability, error) {
	i.calls++
	return i.reach, i.err
}

func repairRoot(now time.Time) deployment.DeliveryRoot {
	return deployment.DeliveryRoot{PhysicalPoolID: "pool", Kind: "published", SourceID: "generation", CatalogDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ObjectKey: "catalogs/sha256/a.ducklake", Status: "active", CreatedAt: now}
}

func TestRepairToolVerifiesSQLiteArtifactAndClosureBeforeMutation(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	root := repairRoot(now)
	inspector := &repairInspector{reach: gc.CatalogReachability{CatalogKey: root.ObjectKey, CatalogDigest: root.CatalogDigest}}
	tool, err := NewRepairTool(repairRootReader{set: deployment.RootSet{PhysicalPoolID: root.PhysicalPoolID, Revision: 4, Roots: []deployment.DeliveryRoot{root}}}, inspector)
	if err != nil {
		t.Fatal(err)
	}
	mutated := false
	if err := tool.VerifyAndMutate(t.Context(), root, func(context.Context, deployment.DeliveryRoot) error {
		mutated = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !mutated || inspector.calls != 1 {
		t.Fatalf("mutation=%v inspector calls=%d", mutated, inspector.calls)
	}
}

func TestRepairToolDeniesMutationOnRootOrArtifactDrift(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	root := repairRoot(now)
	inspector := &repairInspector{reach: gc.CatalogReachability{CatalogKey: root.ObjectKey, CatalogDigest: root.CatalogDigest}, err: errors.New("closure is not verifiable")}
	tool, err := NewRepairTool(repairRootReader{set: deployment.RootSet{PhysicalPoolID: root.PhysicalPoolID, Revision: 4, Roots: []deployment.DeliveryRoot{root}}}, inspector)
	if err != nil {
		t.Fatal(err)
	}
	mutated := false
	if err := tool.VerifyAndMutate(t.Context(), root, func(context.Context, deployment.DeliveryRoot) error {
		mutated = true
		return nil
	}); err == nil || mutated {
		t.Fatalf("closure failure err=%v mutation=%v", err, mutated)
	}
	inspector.err = nil
	drifted := root
	drifted.CatalogDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := tool.VerifyAndMutate(t.Context(), drifted, func(context.Context, deployment.DeliveryRoot) error {
		mutated = true
		return nil
	}); !errors.Is(err, ErrRepairRootDrift) || mutated {
		t.Fatalf("root drift err=%v mutation=%v", err, mutated)
	}
}
