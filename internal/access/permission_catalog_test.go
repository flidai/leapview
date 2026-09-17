package access

import (
	"errors"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestPermissionCatalogIsValidAndDefensive(t *testing.T) {
	catalog := PermissionCatalog()
	if err := ValidatePermissionCatalog(catalog); err != nil {
		t.Fatalf("ValidatePermissionCatalog() error = %v", err)
	}
	if len(catalog) < 40 {
		t.Fatalf("catalog contains %d actions, want complete typed families", len(catalog))
	}
	catalog[0].Action = "forged.action"
	catalog[0].CheckKinds[0] = projectgraph.KindProjectNamespace
	definition, ok := Permission(ActionDashboardRead)
	if !ok {
		t.Fatal("dashboard.read is absent")
	}
	if definition.Action != ActionDashboardRead || len(definition.CheckKinds) != 1 || definition.CheckKinds[0] != projectgraph.KindDashboard {
		t.Fatalf("package catalog mutated through defensive result: %#v", definition)
	}
}

func TestPermissionCatalogSeparatesCreationFromObjectAuthority(t *testing.T) {
	definition, ok := Permission(ActionDashboardCreate)
	if !ok {
		t.Fatal("dashboard.create is absent")
	}
	if definition.Scope != PermissionScopeProject || len(definition.ResourceKinds) != 1 || definition.ResourceKinds[0] != projectgraph.KindDashboard {
		t.Fatalf("dashboard.create definition = %#v", definition)
	}
	if err := ValidateActionForKind(ActionDashboardCreate, projectgraph.KindProjectNamespace); err != nil {
		t.Fatalf("dashboard.create on Project error = %v", err)
	}
	if err := ValidateActionForKind(ActionDashboardCreate, projectgraph.KindDashboard); err == nil {
		t.Fatal("dashboard.create accepted on a not-yet-existing Dashboard")
	}
	for _, action := range []Action{ActionDashboardRead, ActionDashboardUpdate, ActionDashboardDelete, ActionDashboardPublish} {
		if err := ValidateActionForKind(action, projectgraph.KindDashboard); err != nil {
			t.Fatalf("%s on Dashboard error = %v", action, err)
		}
	}
}

func TestSemanticQueryHasExplicitConsumptionPrerequisite(t *testing.T) {
	query, ok := Permission(ActionSemanticQuery)
	if !ok {
		t.Fatal("semantic.query is absent")
	}
	if len(query.Prerequisites) != 1 || query.Prerequisites[0] != ActionSemanticConsume {
		t.Fatalf("semantic.query prerequisites = %v", query.Prerequisites)
	}
	consume, ok := Permission(ActionSemanticConsume)
	if !ok || consume.Action == query.Action {
		t.Fatalf("semantic.consume definition = %#v, found=%v", consume, ok)
	}
}

func TestPermissionCatalogRejectsUnknownPrerequisitesAndCycles(t *testing.T) {
	base := PermissionDefinition{
		Action: Action("test.read"), Family: "Test", Description: "Read test data.", Scope: PermissionScopeResource,
		ResourceKinds: []projectgraph.Kind{projectgraph.KindModel}, CheckKinds: []projectgraph.Kind{projectgraph.KindModel},
	}
	unknown := clonePermissionDefinition(base)
	unknown.Prerequisites = []Action{"missing.action"}
	if err := ValidatePermissionCatalog([]PermissionDefinition{unknown}); !errors.Is(err, ErrInvalidPermissionCatalog) {
		t.Fatalf("unknown prerequisite error = %v", err)
	}
	one := clonePermissionDefinition(base)
	two := clonePermissionDefinition(base)
	one.Action = "test.one"
	two.Action = "test.two"
	one.Prerequisites = []Action{two.Action}
	two.Prerequisites = []Action{one.Action}
	if err := ValidatePermissionCatalog([]PermissionDefinition{one, two}); !errors.Is(err, ErrInvalidPermissionCatalog) {
		t.Fatalf("cycle error = %v", err)
	}
}

func TestPermissionCatalogRejectsInvalidKindsAndInstanceGraphKinds(t *testing.T) {
	invalidKind := PermissionDefinition{
		Action: "test.read", Family: "Test", Description: "Read test data.", Scope: PermissionScopeResource,
		ResourceKinds: []projectgraph.Kind{"unknown"}, CheckKinds: []projectgraph.Kind{"unknown"},
	}
	if err := ValidatePermissionCatalog([]PermissionDefinition{invalidKind}); !errors.Is(err, ErrInvalidPermissionCatalog) {
		t.Fatalf("invalid kind error = %v", err)
	}
	instance := instancePermission("test.read", "Test", "Read test data.")
	instance.CheckKinds = []projectgraph.Kind{projectgraph.KindProjectNamespace}
	if err := ValidatePermissionCatalog([]PermissionDefinition{instance}); !errors.Is(err, ErrInvalidPermissionCatalog) {
		t.Fatalf("instance graph kind error = %v", err)
	}
}

func TestPermissionCatalogRejectsMalformedOrDuplicateActions(t *testing.T) {
	definition := resourcePermission("test.read", "Test", "Read test data.", projectgraph.KindModel, true)
	duplicate := clonePermissionDefinition(definition)
	if err := ValidatePermissionCatalog([]PermissionDefinition{definition, duplicate}); !errors.Is(err, ErrInvalidPermissionCatalog) {
		t.Fatalf("duplicate action error = %v", err)
	}
	definition.Action = "TEST_READ"
	if err := ValidatePermissionCatalog([]PermissionDefinition{definition}); !errors.Is(err, ErrInvalidPermissionCatalog) {
		t.Fatalf("malformed action error = %v", err)
	}
}
