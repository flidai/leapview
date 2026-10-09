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

func TestPostgreSQLAgentConfigurationReferenceSharesActivationTransaction(t *testing.T) {
	pool, repo := agentPostgresTestRepo(t, "configuration_activation")
	ctx := t.Context()
	record := agent.ConfigurationRevision{Enabled: true, Config: agent.Config{Model: "model", APIKey: "must-not-persist"}, CredentialVersionID: "11111111-1111-4111-8111-111111111111", ActorID: "admin"}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := repo.SaveConfigurationTx(ctx, tx, 0, record)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || saved.CredentialVersionID != record.CredentialVersionID || saved.Config.APIKey != "" {
		t.Fatalf("unexpected prepared configuration: revision=%d", saved.Revision)
	}
	if _, err := repo.CurrentConfiguration(ctx); !errors.Is(err, agent.ErrConfigurationNotFound) {
		t.Fatalf("uncommitted configuration visible: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CurrentConfiguration(ctx); !errors.Is(err, agent.ErrConfigurationNotFound) {
		t.Fatalf("rolled-back configuration visible: %v", err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := repo.SaveConfigurationTx(ctx, tx, 0, record); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := repo.CurrentConfiguration(ctx)
	if err != nil || current.CredentialVersionID != record.CredentialVersionID || len(current.Credential) != 0 {
		t.Fatalf("committed reference not retained: %v", err)
	}
	second := record
	second.CredentialVersionID = "22222222-2222-4222-8222-222222222222"
	if _, err := repo.SaveConfiguration(ctx, 1, second); err != nil {
		t.Fatal(err)
	}
	historical, err := repo.ConfigurationByRevision(ctx, 1)
	if err != nil || historical.CredentialVersionID != record.CredentialVersionID {
		t.Fatalf("historical credential pin changed: %v", err)
	}
}

func TestPostgreSQLAgentConfigurationRejectsAmbiguousCredentialReference(t *testing.T) {
	_, repo := agentPostgresTestRepo(t, "configuration_ambiguous")
	for _, record := range []agent.ConfigurationRevision{
		{CredentialVersionID: "11111111-1111-4111-8111-111111111111", Credential: []byte("legacy-ciphertext")},
		{CredentialVersionID: "not-a-version"},
		{CredentialVersionID: "00000000-0000-0000-0000-000000000000"},
	} {
		record.ActorID = "admin"
		if _, err := repo.SaveConfiguration(t.Context(), 0, record); err == nil {
			t.Fatal("accepted ambiguous or invalid credential reference")
		}
	}
	if _, err := repo.CurrentConfiguration(t.Context()); !errors.Is(err, agent.ErrConfigurationNotFound) {
		t.Fatalf("invalid reference persisted: %v", err)
	}
}
