package module

import (
	"github.com/flidai/leapview/internal/servingstate"
	"testing"
)

func TestCurrentServingStateIDReportsOnlyInstalledConvergenceHint(t *testing.T) {
	var absent *Module
	if absent.CurrentServingStateID() != "" || (&Module{}).CurrentServingStateID() != "" {
		t.Fatal("missing registry reported an installed generation")
	}
	registry := reconcilerRegistry(t, "generation-1", "generation-2")
	m := &Module{registry: registry}
	if m.CurrentServingStateID() != "" {
		t.Fatal("persisted generations were mistaken for an installed runtime")
	}
	for _, id := range []string{"generation-1", "generation-2"} {
		if err := registry.ReconcileSealed(t.Context(), servingstate.ID(id)); err != nil {
			t.Fatal(err)
		}
		if string(m.CurrentServingStateID()) != id {
			t.Fatal("module hint differs from the installed registry generation")
		}
	}
}
