package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSemanticRegistryContextPostgreSQL18(t *testing.T) {
	db := newControlAuthorityDatabase(t)
	project := controlAuthorityProject(t)
	seedControlPrincipal(t, db.admin, controlActorID)
	for _, instance := range []string{controlInstanceA, controlInstanceB} {
		seedControlResources(t, db.admin, instance, project)
		if _, err := db.repo.InitializeControlState(t.Context(), access.ControlStateSeed{InstanceID: instance, ProjectID: controlProject, ActorID: controlActorID}, project); err != nil {
			t.Fatal(err)
		}
	}
	definition, err := db.repo.RegisterSemanticAttribute(t.Context(), access.RegisterSemanticAttributeInput{Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: controlActorID}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.repo.ReadSemanticRegistry(t.Context(), controlInstanceA)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Validate(controlInstanceA, project.ProjectID()); err != nil {
		t.Fatal(err)
	}
	if len(first.Registry.Definitions) != 1 || first.Registry.Definitions[0].ID != definition.ID {
		t.Fatalf("registry definitions = %+v", first.Registry.Definitions)
	}
	if err := first.Validate(controlInstanceB, project.ProjectID()); err == nil {
		t.Fatal("cross-instance registry context accepted")
	}
	second, err := db.repo.ReadSemanticRegistry(t.Context(), controlInstanceB)
	if err != nil || second.Control.InstanceID != controlInstanceB || first.Equal(second) {
		t.Fatalf("second instance: %+v %v", second, err)
	}
	if _, err := db.repo.ReadSemanticRegistry(t.Context(), "instance-missing"); err == nil {
		t.Fatal("missing control scope accepted")
	}
	if _, err := db.repo.UpdateSemanticAttributeMetadata(t.Context(), access.UpdateSemanticAttributeMetadataInput{Name: "region", ExpectedVersion: definition.DefinitionVersion, Metadata: access.SemanticAttributeMetadata{DisplayName: "Changed"}, Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: controlActorID}}); err != nil {
		t.Fatal(err)
	}
	updated, err := db.repo.ReadSemanticRegistry(t.Context(), controlInstanceA)
	if err != nil {
		t.Fatal(err)
	}
	if first.Equal(updated) || first.Registry.State.Revision >= updated.Registry.State.Revision || first.Registry.State.Digest == updated.Registry.State.Digest {
		t.Fatal("registry mutation retained stale context")
	}
	tx, err := db.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	bound, err := ReadSemanticRegistryTx(t.Context(), tx, controlInstanceA)
	if err != nil {
		t.Fatalf("caller-owned semantic registry context: %v", err)
	}
	if bound.Control.InstanceID != controlInstanceA || len(bound.Registry.Definitions) != 1 {
		t.Fatalf("caller-owned context = %+v", bound)
	}
	var stillOpen int
	if err := tx.QueryRow(t.Context(), `SELECT 1`).Scan(&stillOpen); err != nil || stillOpen != 1 {
		t.Fatalf("publication transaction ownership was not retained: %v", err)
	}
	if _, err := (&Repository{db: tx}).ReadSemanticRegistry(t.Context(), controlInstanceA); err == nil {
		t.Fatal("caller-owned transaction accepted")
	}
}

func TestReadSemanticRegistryTxHoldsRegistryMutationLock(t *testing.T) {
	db := newControlAuthorityDatabase(t)
	project := controlAuthorityProject(t)
	seedControlPrincipal(t, db.admin, controlActorID)
	seedControlResources(t, db.admin, controlInstanceA, project)
	if _, err := db.repo.InitializeControlState(t.Context(), access.ControlStateSeed{InstanceID: controlInstanceA, ProjectID: controlProject, ActorID: controlActorID}, project); err != nil {
		t.Fatal(err)
	}
	definition, err := db.repo.RegisterSemanticAttribute(t.Context(), access.RegisterSemanticAttributeInput{
		Name: "locked_region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
		Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: controlActorID},
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	value, err := ReadSemanticRegistryTx(t.Context(), tx, controlInstanceA)
	if err != nil || value.Registry.State.Revision <= 0 {
		_ = tx.Rollback(t.Context())
		t.Fatalf("bound registry context = %+v, err=%v", value, err)
	}
	var blockerPID int32
	if err := tx.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	// The control row is held FOR SHARE as well as the registry singleton. A
	// competing FOR UPDATE NOWAIT probe makes that lock assertion deterministic
	// without relying on scheduler timing.
	probe, err := db.runtime.Begin(t.Context())
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	_, probeErr := probe.Exec(t.Context(), `
		SELECT instance_id FROM access.control_state WHERE instance_id=$1 FOR UPDATE NOWAIT`, controlInstanceA)
	_ = probe.Rollback(t.Context())
	var lockError *pgconn.PgError
	if !errors.As(probeErr, &lockError) || lockError.Code != "55P03" {
		_ = tx.Rollback(t.Context())
		t.Fatalf("control mutation probe must report lock_not_available, got %v", probeErr)
	}

	started := make(chan struct{})
	done := make(chan error, 1)
	completed := make(chan struct{})
	mutationContext, cancelMutation := context.WithCancel(t.Context())
	defer func() {
		cancelMutation()
		_ = tx.Rollback(t.Context())
		select {
		case <-completed:
		case <-time.After(5 * time.Second):
			t.Error("registry mutation goroutine did not clean up")
		}
	}()
	go func() {
		defer close(completed)
		close(started)
		_, mutationErr := db.repo.UpdateSemanticAttributeMetadata(mutationContext, access.UpdateSemanticAttributeMetadataInput{
			Name: "locked_region", ExpectedVersion: definition.DefinitionVersion,
			Metadata: access.SemanticAttributeMetadata{DisplayName: "blocked until publication completes"},
			Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: controlActorID},
		})
		done <- mutationErr
	}()
	<-started
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := db.admin.QueryRow(t.Context(), `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE $1::integer = ANY(pg_blocking_pids(pid))
			)`, blockerPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case mutationErr := <-done:
			t.Fatalf("registry mutation completed without waiting on the publication lock: %v", mutationErr)
		case <-deadline.C:
			t.Fatal("PostgreSQL did not observe registry mutation blocked by publication transaction")
		case <-tick.C:
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case mutationErr := <-done:
		if mutationErr != nil {
			t.Fatalf("registry mutation after publication commit: %v", mutationErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registry mutation remained blocked after publication transaction completed")
	}
}
