package postgres

import (
	"errors"
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/agent"
)

func TestPostgreSQLAgentConfigurationConcurrentSave(t *testing.T) {
	_, repo := agentPostgresTestRepo(t, "configuration")
	ctx := t.Context()
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := repo.SaveConfiguration(ctx, 0, agent.ConfigurationRevision{Enabled: true, Config: agent.Config{Model: "model", APIKey: "must-not-persist"}, Credential: []byte("ciphertext"), ActorID: "admin"})
			outcomes <- err
		})
	}
	wg.Wait()
	close(outcomes)
	successes, conflicts := 0, 0
	for err := range outcomes {
		if err == nil {
			successes++
		} else if errors.Is(err, agent.ErrConfigurationConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	current, err := repo.CurrentConfiguration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Config.APIKey != "" || current.Revision != 1 || current.ActorID != "admin" {
		t.Fatalf("invalid stored configuration: revision=%d actor=%s", current.Revision, current.ActorID)
	}
	previous, err := repo.ConfigurationByRevision(ctx, 1)
	if err != nil || previous.Config.Model != "model" {
		t.Fatalf("revision lookup: %v", err)
	}
}
