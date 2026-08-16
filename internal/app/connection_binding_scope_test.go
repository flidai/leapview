package app

import (
	"testing"

	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestValidateCanonicalConnectionBindingScopeRequiresActiveProject(t *testing.T) {
	scope := analyticsmodule.ConnectionBindingScope{ProjectID: projectgraph.ResourceID("project_other"), Environment: "prod"}
	if err := validateCanonicalConnectionBindingScope(scope, "project_active", "prod"); err == nil {
		t.Fatal("expected project mismatch to be rejected")
	}
}

func TestValidateCanonicalConnectionBindingScopeRequiresConfiguredEnvironment(t *testing.T) {
	scope := analyticsmodule.ConnectionBindingScope{ProjectID: projectgraph.ResourceID("project_active"), Environment: "staging"}
	if err := validateCanonicalConnectionBindingScope(scope, "project_active", "prod"); err == nil {
		t.Fatal("expected environment mismatch to be rejected")
	}
}

func TestValidateCanonicalConnectionBindingScopeAcceptsExactScope(t *testing.T) {
	scope := analyticsmodule.ConnectionBindingScope{ProjectID: projectgraph.ResourceID("project_active"), Environment: "prod"}
	if err := validateCanonicalConnectionBindingScope(scope, "project_active", "prod"); err != nil {
		t.Fatalf("exact configured scope rejected: %v", err)
	}
}
