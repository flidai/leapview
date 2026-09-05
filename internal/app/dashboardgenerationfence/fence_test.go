package dashboardgenerationfence

import (
	"context"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestNewRequiresNativeDeploymentAuthority(t *testing.T) {
	if _, err := New(nil, "target"); err == nil {
		t.Fatal("nil deployment authority unexpectedly accepted")
	}
}

func TestFenceFailsClosedWithoutTransaction(t *testing.T) {
	var fence *Fence
	if err := fence.ValidateActiveGeneration(context.Background(), nil, projectgraph.ServingIdentity{}); err == nil {
		t.Fatal("nil fence unexpectedly accepted")
	}
}
