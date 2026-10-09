package postgres

import (
	"context"
	"testing"
	"time"
)

func TestCredentialBindingLockPreventsEndpointAuthorityMutationThroughCommit(t *testing.T) {
	db, runtimeDB := connectionBindingDatabases(t)
	repository := New(db)
	binding := testBinding(t)
	if err := repository.Create(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	tx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	actual, err := repository.BindingForShareTx(t.Context(), tx, binding.Scope, binding.TargetID, binding.ConnectionID)
	if err != nil || actual.Revision != binding.Revision || actual.Evidence().EndpointConfigHash != binding.Evidence().EndpointConfigHash {
		t.Fatalf("bound authority err=%v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	entered := make(chan struct{})
	updated := make(chan error, 1)
	go func() {
		close(entered)
		_, err := db.Exec(ctx, "UPDATE connection_binding.target_connection_binding SET endpoint_json=jsonb_set(endpoint_json, '{host}', '\"changed.internal\"'), revision=revision+1 WHERE id=$1", binding.ID.String())
		updated <- err
	}()
	<-entered
	select {
	case err := <-updated:
		t.Fatalf("binding update crossed retained source authority lock: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-updated:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
