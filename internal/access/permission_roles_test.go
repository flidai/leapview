package access

import "testing"

func TestPermissionRolePresetsAreCatalogBoundAndDefensive(t *testing.T) {
	presets := PermissionRolePresets()
	if err := ValidatePermissionRolePresets(presets, PermissionCatalog()); err != nil {
		t.Fatalf("ValidatePermissionRolePresets() error = %v", err)
	}
	presets[0].Actions[0] = ActionPlatformAccessManage
	actions, ok := PermissionRoleActions(PermissionRoleViewer)
	if !ok || len(actions) != 2 || actions[0] != ActionDashboardRead {
		t.Fatalf("Viewer mutated through defensive result: %v, found=%v", actions, ok)
	}
}

func TestPermissionRolePresetsPreserveSeparationOfDuties(t *testing.T) {
	assertRoleExcludes(t, PermissionRoleViewer, ActionSemanticQuery, ActionPipelineRun, ActionDashboardPublish)
	assertRoleExcludes(t, PermissionRoleEditor, ActionDashboardPublish, ActionDashboardDelete, ActionResourceShare, ActionPipelineRun, ActionProjectAccessManage)
	assertRoleExcludes(t, PermissionRoleProjectAdmin, ActionSemanticConsume, ActionSemanticQuery, ActionPlatformAccessManage)
	assertRoleExcludes(t, PermissionRolePublisher, ActionDashboardUpdate, ActionDashboardDelete, ActionResourceShare, ActionDeliveryApprove)
	assertRoleExcludes(t, PermissionRoleReleaseOperator, ActionDeliveryApprove)
	assertRoleExcludes(t, PermissionRoleReleaseApprover, ActionDeliveryActivate)

	assertRoleIncludes(t, PermissionRoleViewer, ActionDashboardRead, ActionSemanticConsume)
	assertRoleIncludes(t, PermissionRoleExplorer, ActionSemanticRead, ActionSemanticQuery, ActionSemanticConsume)
	assertRoleIncludes(t, PermissionRoleProjectAdmin, ActionProjectAccessManage, ActionProjectAccessDelegate)
}

func assertRoleIncludes(t *testing.T, role PermissionRole, wanted ...Action) {
	t.Helper()
	actions, ok := PermissionRoleActions(role)
	if !ok {
		t.Fatalf("role %q is absent", role)
	}
	set := make(map[Action]struct{}, len(actions))
	for _, action := range actions {
		set[action] = struct{}{}
	}
	for _, action := range wanted {
		if _, ok := set[action]; !ok {
			t.Errorf("role %q lacks %q", role, action)
		}
	}
}

func assertRoleExcludes(t *testing.T, role PermissionRole, unwanted ...Action) {
	t.Helper()
	actions, ok := PermissionRoleActions(role)
	if !ok {
		t.Fatalf("role %q is absent", role)
	}
	set := make(map[Action]struct{}, len(actions))
	for _, action := range actions {
		set[action] = struct{}{}
	}
	for _, action := range unwanted {
		if _, ok := set[action]; ok {
			t.Errorf("role %q unexpectedly includes %q", role, action)
		}
	}
}
