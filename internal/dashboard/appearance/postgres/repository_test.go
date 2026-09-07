package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testAudit struct {
	fail  bool
	input *AuditInput
}

func (a testAudit) RecordAuditEvent(_ context.Context, _ Tx, input AuditInput) error {
	if a.fail {
		return errors.New("audit failure")
	}
	if a.input != nil {
		*a.input = input
	}
	return nil
}

type testEvents struct {
	fail     bool
	mismatch bool
	input    *EventInput
}

func (e testEvents) AppendEvent(_ context.Context, _ Tx, input EventInput) (Event, error) {
	if e.fail {
		return Event{}, errors.New("event failure")
	}
	event := Event{EventID: input.EventID, ProjectID: input.ProjectID, DashboardID: input.DashboardID, ActorID: input.ActorID, Revision: input.Revision, Patch: input.Patch, AggregateVersion: input.Revision}
	if e.mismatch {
		event.AggregateVersion++
	}
	if e.input != nil {
		*e.input = input
	}
	return event, nil
}

func appearanceRepo(t *testing.T, options Options) *Repository {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	repository, err := New(db, options)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func appearanceKey() dashboardappearance.Key {
	return dashboardappearance.Key{ProjectID: projectgraph.ResourceID("project:appearance"), DashboardID: projectgraph.ResourceID("dashboard:overview")}
}

func TestRepositoryPostgreSQL18AppearanceRevisionCASAndRollback(t *testing.T) {
	var auditInput AuditInput
	var eventInput EventInput
	repository := appearanceRepo(t, Options{Audit: testAudit{input: &auditInput}, Events: testEvents{input: &eventInput}})
	key := appearanceKey()
	icon := "chart-no-axes-combined"
	row, err := repository.ApplyPatch(t.Context(), key, "principal:test", dashboardappearance.Patch{Icon: &icon})
	if err != nil || row.Revision != 1 || row.Icon != icon {
		t.Fatalf("created appearance = %#v (%v)", row, err)
	}
	auditID, auditErr := uuid.Parse(auditInput.AuditID)
	domainEventID, eventErr := uuid.Parse(auditInput.DomainEventID)
	if auditErr != nil || eventErr != nil || auditID.Version() != 7 || domainEventID.Version() != 7 || auditInput.AuditID == auditInput.DomainEventID {
		t.Fatalf("audit/event identities = %#v, want distinct UUIDv7 values", auditInput)
	}
	if eventInput.EventID != auditInput.DomainEventID || auditInput.AggregateSequence != row.Revision {
		t.Fatalf("audit/event projection mismatch: audit=%#v event=%#v row=%#v", auditInput, eventInput, row)
	}
	color := "blue"
	tx, err := repository.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	}).Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	updated, err := repository.ApplyPatchCAS(t.Context(), tx, key, row.Revision, "principal:test", dashboardappearance.Patch{Color: &color})
	if err != nil || updated.Revision != 2 || updated.Color != color {
		_ = tx.Rollback(t.Context())
		t.Fatalf("CAS appearance = %#v (%v)", updated, err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx, err = repository.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	}).Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ApplyPatchCAS(t.Context(), tx, key, row.Revision, "principal:test", dashboardappearance.Patch{Color: &color}); !errors.Is(err, ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("stale CAS = %v", err)
	}
	_ = tx.Rollback(t.Context())

	failing := appearanceRepo(t, Options{Audit: testAudit{fail: true}, Events: testEvents{}})
	if _, err := failing.ApplyPatch(t.Context(), appearanceKey(), "principal:test", dashboardappearance.Patch{Icon: &icon}); err == nil {
		t.Fatal("audit failure did not abort appearance mutation")
	}
	if _, err := failing.Get(t.Context(), appearanceKey()); err == nil {
		t.Fatal("rolled-back appearance row exists")
	}
	eventFailing := appearanceRepo(t, Options{Audit: testAudit{}, Events: testEvents{fail: true}})
	if _, err := eventFailing.ApplyPatch(t.Context(), appearanceKey(), "principal:test", dashboardappearance.Patch{Icon: &icon}); err == nil {
		t.Fatal("event failure did not abort appearance mutation")
	}
	if _, err := eventFailing.Get(t.Context(), appearanceKey()); err == nil {
		t.Fatal("event-failed appearance row exists")
	}
	mismatched := appearanceRepo(t, Options{Audit: testAudit{}, Events: testEvents{mismatch: true}})
	if _, err := mismatched.ApplyPatch(t.Context(), appearanceKey(), "principal:test", dashboardappearance.Patch{Icon: &icon}); !errors.Is(err, ErrConflict) {
		t.Fatalf("event mismatch error = %v, want conflict", err)
	}
	if _, err := mismatched.Get(t.Context(), appearanceKey()); err == nil {
		t.Fatal("mismatched event appearance row exists")
	}
	if _, err := repository.ApplyPatch(t.Context(), appearanceKey(), "principal:\x00", dashboardappearance.Patch{Icon: &icon}); err == nil {
		t.Fatal("control-character actor accepted")
	}
}

func TestRepositoryPostgreSQL18AppearanceConcurrentCAS(t *testing.T) {
	repository := appearanceRepo(t, Options{Audit: testAudit{}, Events: testEvents{}})
	key := appearanceKey()
	icon := "chart-no-axes-combined"
	initial, err := repository.ApplyPatch(t.Context(), key, "principal:test", dashboardappearance.Patch{Icon: &icon})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			color := "blue"
			tx, err := repository.db.(interface {
				Begin(context.Context) (pgx.Tx, error)
			}).Begin(t.Context())
			if err != nil {
				results <- err
				return
			}
			_, err = repository.ApplyPatchCAS(t.Context(), tx, key, initial.Revision, "principal:test", dashboardappearance.Patch{Color: &color})
			if err == nil {
				err = tx.Commit(t.Context())
			} else {
				_ = tx.Rollback(t.Context())
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatalf("concurrent CAS = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent CAS successes = %d", successes)
	}
}

func TestRepositoryPostgreSQL18CASRequiresExistingRevision(t *testing.T) {
	repository := appearanceRepo(t, Options{Audit: testAudit{}, Events: testEvents{}})
	key := appearanceKey()
	tx, err := repository.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	}).Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	icon := "chart-no-axes-combined"
	if _, err := repository.ApplyPatchCAS(t.Context(), tx, key, 1, "principal:test", dashboardappearance.Patch{Icon: &icon}); !errors.Is(err, ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("CAS created missing row with error = %v", err)
	}
	_ = tx.Rollback(t.Context())
}
