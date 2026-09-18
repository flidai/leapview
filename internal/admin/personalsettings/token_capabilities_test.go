package personalsettings

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestPermissionOptionsSignalPreservesExactActionTargets(t *testing.T) {
	projectPair, err := access.NewProjectPermissionPair(access.ActionProjectSettingsRead, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	instancePair, err := access.NewInstancePermissionPair(access.ActionPlatformAccessManage, "instance-1")
	if err != nil {
		t.Fatal(err)
	}
	invalid := access.PermissionPair{Action: access.Action("legacy.read"), Profile: access.PermissionCatalogProfile}
	options := permissionOptionsSignal([]access.PermissionPair{projectPair, instancePair, projectPair, invalid})
	if len(options) != 2 {
		t.Fatalf("options = %#v, want two unique valid pairs", options)
	}
	for _, option := range options {
		if option.Permissions == nil || len(*option.Permissions) != 1 {
			t.Fatalf("option = %#v, want one attached pair", option)
		}
		pair := (*option.Permissions)[0]
		if !strings.Contains(option.Label, pair.Action) {
			t.Fatalf("option label = %q, want exact action %q", option.Label, pair.Action)
		}
	}
	if options[0].Value == options[1].Value {
		t.Fatalf("options reused a value across action-target pairs: %#v", options)
	}
}
