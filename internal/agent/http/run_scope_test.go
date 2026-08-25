package http

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestBindRunProjectUsesActiveRuntimeAndPreservesIdentity(t *testing.T) {
	handler := NewHandler(Options{
		ActiveProjectID: "project:stale",
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "project:active", nil
		},
	})
	scope, err := handler.bindRunProject(t.Context(), agent.Scope{PrincipalID: "principal-1", DevAuthBypass: true})
	if err != nil {
		t.Fatal(err)
	}
	if scope.ProjectID != "project:active" || scope.PrincipalID != "principal-1" || !scope.DevAuthBypass {
		t.Fatalf("bound scope = %#v", scope)
	}
}

func TestBindRunProjectFailsClosedWithoutRuntime(t *testing.T) {
	handler := NewHandler(Options{ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) {
		return "", errors.New("not active")
	}})
	if _, err := handler.bindRunProject(t.Context(), agent.Scope{PrincipalID: "principal-1"}); err == nil || !strings.Contains(err.Error(), "active project runtime is required") {
		t.Fatalf("bindRunProject() error = %v", err)
	}
}

func TestBindRunProjectRejectsCredentialProjectMismatch(t *testing.T) {
	handler := NewHandler(Options{ActiveProjectID: "project:active"})
	_, err := handler.bindRunProject(t.Context(), agent.Scope{
		PrincipalID: "principal-1",
		Credential:  agent.CredentialScope{ProjectID: "project:other", Restricted: true},
	})
	if err == nil || !strings.Contains(err.Error(), "credential project") {
		t.Fatalf("bindRunProject() error = %v", err)
	}
}
