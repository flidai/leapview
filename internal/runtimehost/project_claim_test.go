package runtimehost

import (
	"errors"
	"sync"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestManagerBindsClaimedProjectBeforeGenerationAndRejectsChanges(t *testing.T) {
	manager := NewManagerWithFactory(ManagerOptions{Environment: servingstate.Environment("prod")})
	defer manager.Close()
	if err := manager.BindClaimedProject(projectgraph.ResourceID("finance"), "prod"); err != nil {
		t.Fatal(err)
	}
	if got := manager.ProjectID(); got != "finance" {
		t.Fatalf("bound project = %q, want finance", got)
	}
	if err := manager.BindClaimedProject("finance", "prod"); err != nil {
		t.Fatalf("idempotent bind = %v", err)
	}
	if err := manager.BindClaimedProject("marketing", "prod"); err == nil {
		t.Fatal("different project bind succeeded")
	}
	if err := manager.BindClaimedProject("finance", "staging"); err == nil {
		t.Fatal("different environment bind succeeded")
	}
}

func TestManagerConcurrentClaimBindingHasOneWinner(t *testing.T) {
	manager := NewManagerWithFactory(ManagerOptions{Environment: "prod"})
	defer manager.Close()
	projects := []projectgraph.ResourceID{"finance", "marketing"}
	results := make(chan error, len(projects))
	var wg sync.WaitGroup
	for _, projectID := range projects {
		wg.Add(1)
		go func(projectID projectgraph.ResourceID) {
			defer wg.Done()
			results <- manager.BindClaimedProject(projectID, "prod")
		}(projectID)
	}
	wg.Wait()
	close(results)
	var success, conflict int
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrProjectBindConflict) {
			conflict++
		} else {
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent binds = success %d conflict %d, want one each", success, conflict)
	}
}
