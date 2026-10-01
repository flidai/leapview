package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestCurrentEffectivePermissionOptionsFailsClosedWithoutTypedPrincipalGrants(t *testing.T) {
	module := &Module{}
	options, err := module.CurrentEffectivePermissionOptions(context.Background(), "principal-1")
	if err != nil {
		t.Fatal(err)
	}
	if options == nil || len(options) != 0 {
		t.Fatalf("options = %#v, want explicit empty typed authority", options)
	}

	pair, err := access.NewProjectPermissionPair(access.ActionProjectSettingsRead, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	module.SetCurrentEffectivePermissionOptions(func(context.Context, string) ([]access.PermissionPair, error) {
		return []access.PermissionPair{pair}, nil
	})
	options, err = module.CurrentEffectivePermissionOptions(context.Background(), "principal-1")
	if err != nil || len(options) != 1 || options[0] != pair {
		t.Fatalf("configured options = %#v, %v; want exact configured pair", options, err)
	}
}
