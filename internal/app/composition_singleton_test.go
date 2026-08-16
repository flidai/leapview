package app

import (
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

func TestSingletonProjectIDIgnoresOtherEnvironments(t *testing.T) {
	projectID, err := singletonProjectID([]servingstatemodule.ActiveScope{
		{ProjectID: projectgraph.ResourceID("project-prod"), Environment: "prod"},
		{ProjectID: projectgraph.ResourceID("unrelated-dev"), Environment: "dev"},
	}, servingstatemodule.Environment("prod"))
	if err != nil {
		t.Fatal(err)
	}
	if projectID != "project-prod" {
		t.Fatalf("project = %q, want project-prod", projectID)
	}
}

func TestSingletonProjectIDRejectsTwoProjectsInConfiguredEnvironment(t *testing.T) {
	_, err := singletonProjectID([]servingstatemodule.ActiveScope{
		{ProjectID: projectgraph.ResourceID("project-a"), Environment: "prod"},
		{ProjectID: projectgraph.ResourceID("project-b"), Environment: "prod"},
		{ProjectID: projectgraph.ResourceID("unrelated-dev"), Environment: "dev"},
	}, servingstatemodule.Environment("prod"))
	if err == nil || !strings.Contains(err.Error(), "span multiple projects") {
		t.Fatalf("error = %v, want configured-environment project conflict", err)
	}
}
