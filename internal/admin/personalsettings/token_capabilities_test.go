package personalsettings

import (
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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
	resource, err := access.NewResourceRef("semantic-model:operations", projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	resourcePair, err := access.NewExactPermissionPair(access.ActionSemanticRead, "project-1", resource)
	if err != nil {
		t.Fatal(err)
	}
	invalid := access.PermissionPair{Action: access.Action("legacy.read"), Profile: access.PermissionCatalogProfile}
	options := permissionOptionsSignal([]access.PermissionPair{projectPair, instancePair, resourcePair, projectPair, invalid})
	if len(options) != 3 {
		t.Fatalf("options = %#v, want three unique valid pairs", options)
	}
	for _, option := range options {
		if option.Permissions == nil || len(*option.Permissions) != 1 {
			t.Fatalf("option = %#v, want one attached pair", option)
		}
		pair := (*option.Permissions)[0]
		if strings.Contains(option.Label, pair.Action) || strings.Contains(option.Description, pair.Action) {
			t.Fatalf("option = %#v, raw action %q must stay out of the default presentation", option, pair.Action)
		}
		for _, rawID := range []*string{pair.Target.InstanceID, pair.Target.ProjectID, pair.Target.ResourceID} {
			if rawID != nil && *rawID != "" && (strings.Contains(option.Label, *rawID) || strings.Contains(option.Description, *rawID)) {
				t.Fatalf("option = %#v, raw ID %q must stay out of the default presentation", option, *rawID)
			}
		}
	}
	if options[0].Value == options[1].Value {
		t.Fatalf("options reused a value across action-target pairs: %#v", options)
	}

	wantPresentation := map[access.Action][3]string{
		access.ActionProjectSettingsRead:  {"View project settings", "Current project", "Project administration"},
		access.ActionPlatformAccessManage: {"Manage platform access", "This instance", "Platform administration"},
		access.ActionSemanticRead:         {"Operations", "Semantic model · Discover metadata", "Semantic models"},
	}
	for _, option := range options {
		pair := (*option.Permissions)[0]
		want := wantPresentation[access.Action(pair.Action)]
		if got := [3]string{option.Label, option.Description, option.Category}; !reflect.DeepEqual(got, want) {
			t.Errorf("presentation for %q = %#v, want %#v", pair.Action, got, want)
		}
	}
}

func TestPermissionActionLabelsCoverSelectableCatalog(t *testing.T) {
	for _, definition := range access.PermissionCatalog() {
		if !definition.UISelectable {
			continue
		}
		if got := permissionActionLabel(definition.Action); got == "Use permission" {
			t.Errorf("selectable action %q has no human-facing label", definition.Action)
		}
	}
}
