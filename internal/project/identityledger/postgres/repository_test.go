package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	apptesting "github.com/flidai/leapview/internal/app/testing"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newLedgerDatabase(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	h := postgrestest.Start(t, apptesting.PostgresConformanceRequired())
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{
		Name: "leapview_control_runtime", Password: "leapview-conformance-secret", Login: true,
	})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "")
	h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SET ROLE leapview_control_migrator`); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		t.Fatalf("apply PostgreSQL migrations: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()

	runtime, err := pgxpool.New(ctx, database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	repo, err := New(runtime)
	if err != nil {
		t.Fatal(err)
	}
	return repo, admin
}

func candidate(instance, bundle, expected string, resources ...identityledger.Resource) identityledger.Candidate {
	return identityledger.Candidate{
		InstanceID: instance, BundleID: bundle, ExpectedBundleID: expected,
		ActorID: "publisher", Resources: resources,
	}
}

func resource(id string, kind projectgraph.Kind) identityledger.Resource {
	return identityledger.Resource{AuthoredID: projectgraph.ResourceID(id), Kind: kind}
}

func TestIdentityHistoryPostgreSQL18ParameterTypes(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	conn, err := repo.db.(*pgxpool.Pool).Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	statement, err := conn.Conn().Prepare(t.Context(), "identity_history_parameters", appendHistorySQL)
	if err != nil {
		t.Fatalf("prepare history query: %v", err)
	}
	var domainOID uint32
	if err := conn.QueryRow(t.Context(), `SELECT 'platform.resource_id'::regtype::oid`).Scan(&domainOID); err != nil {
		t.Fatal(err)
	}
	if statement.ParamOIDs[0] != domainOID || statement.ParamOIDs[1] != domainOID {
		t.Fatalf("identity parameter OIDs = %v, want resource_id %d", statement.ParamOIDs, domainOID)
	}
	_, err = conn.Exec(t.Context(), appendHistorySQL, "", "source:test", "source", "created", "bundle", "actor", "")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "resource_id_check" {
		t.Fatalf("invalid resource ID error = %v, want domain CHECK violation", err)
	}
}

func TestIdentityLedgerPostgreSQL18Lifecycle(t *testing.T) {
	repo, admin := newLedgerDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	first := candidate("instance-a", "bundle-1", "",
		resource("orders", projectgraph.KindSource),
		resource("orders-model", projectgraph.KindModel),
	)
	plan, err := repo.Activate(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Outcomes) != 2 || plan.Outcomes[0].Outcome != identityledger.OutcomeCreated {
		t.Fatalf("first activation outcomes = %#v", plan.Outcomes)
	}

	// The same authored ID is a separate identity in another instance.
	if _, err := repo.Activate(ctx, candidate("instance-b", "bundle-1", "", resource("orders", projectgraph.KindModel))); err != nil {
		t.Fatalf("cross-instance authored ID: %v", err)
	}

	if _, err := repo.PutReference(ctx, identityledger.DurableReference{
		InstanceID: "instance-a", ReferenceID: "grant-orders", OwnerAuthoredID: "grant-1",
		OwnerKind: "grant", TargetAuthoredID: "orders", ExpectedKind: projectgraph.KindSource,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PutReference(ctx, identityledger.DurableReference{
		InstanceID: "instance-a", ReferenceID: "grant-stale", OwnerAuthoredID: "grant-2",
		OwnerKind: "grant", TargetAuthoredID: "orders", ExpectedKind: projectgraph.KindSource,
	}); err != nil {
		t.Fatal(err)
	}

	// Kind is immutable even after the first generation.
	changedKind := candidate("instance-a", "bundle-kind-conflict", "bundle-1",
		resource("orders", projectgraph.KindModel),
	)
	if _, err := repo.Activate(ctx, changedKind); !errors.Is(err, identityledger.ErrKindConflict) {
		t.Fatalf("kind-change error = %v", err)
	}

	second := candidate("instance-a", "bundle-2", "bundle-1", resource("orders-model", projectgraph.KindModel))
	if _, err := repo.Activate(ctx, second); err != nil {
		t.Fatal(err)
	}
	identity, err := repo.Identity(ctx, "instance-a", "orders")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Lifecycle != identityledger.LifecycleTombstoned || identity.ActiveBundleID != "" || identity.TombstonedAt == nil {
		t.Fatalf("tombstoned identity = %#v", identity)
	}
	reference, err := repo.Reference(ctx, "instance-a", "grant-orders")
	if err != nil {
		t.Fatal(err)
	}
	if reference.Lifecycle != identityledger.ReferenceSuspended || reference.SuspendedAt == nil {
		t.Fatalf("suspended reference = %#v", reference)
	}
	stale, err := repo.Reference(ctx, "instance-a", "grant-stale")
	if err != nil {
		t.Fatal(err)
	}
	if stale.Lifecycle != identityledger.ReferenceSuspended || stale.SuspendedAt == nil {
		t.Fatalf("stale suspended reference = %#v", stale)
	}

	third := candidate("instance-a", "bundle-3", "bundle-2",
		resource("orders", projectgraph.KindSource), resource("orders-model", projectgraph.KindModel),
	)
	if _, err := repo.Activate(ctx, third); !errors.Is(err, identityledger.ErrRestoreRequired) {
		t.Fatalf("implicit restore error = %v", err)
	}
	if _, err := repo.RestoreAndActivate(ctx, identityledger.Restore{
		Candidate: third, AuthoredIDs: []projectgraph.ResourceID{"orders"}, Reason: "restore approved after dependency validation",
	}); err != nil {
		t.Fatal(err)
	}
	reference, err = repo.Reference(ctx, "instance-a", "grant-orders")
	if err != nil {
		t.Fatal(err)
	}
	if reference.Lifecycle != identityledger.ReferenceSuspended || reference.SuspendedAt == nil || reference.ReactivatedAt != nil {
		t.Fatalf("restore unexpectedly reactivated reference = %#v", reference)
	}
	if _, err := repo.ReconcileReferences(ctx, "instance-a", []identityledger.DurableReference{{
		InstanceID: "instance-a", ReferenceID: "grant-orders", OwnerAuthoredID: "grant-1",
		OwnerKind: "grant", TargetAuthoredID: "orders", ExpectedKind: projectgraph.KindSource,
	}}); err != nil {
		t.Fatal(err)
	}
	reference, err = repo.Reference(ctx, "instance-a", "grant-orders")
	if err != nil {
		t.Fatal(err)
	}
	if reference.Lifecycle != identityledger.ReferenceActive || reference.SuspendedAt != nil || reference.ReactivatedAt == nil {
		t.Fatalf("exact desired reference was not reactivated = %#v", reference)
	}
	stale, err = repo.Reference(ctx, "instance-a", "grant-stale")
	if err != nil {
		t.Fatal(err)
	}
	if stale.Lifecycle != identityledger.ReferenceSuspended || stale.SuspendedAt == nil || stale.ReactivatedAt != nil {
		t.Fatalf("removed reference was reactivated = %#v", stale)
	}

	// Rollback uses the exact historical bundle snapshot and switches the
	// singleton active bundle in the same transaction.
	if _, err := repo.Rollback(ctx, identityledger.Rollback{
		InstanceID: "instance-a", BundleID: "bundle-1", ExpectedBundleID: "bundle-3",
		ActorID: "publisher", Reason: "release rollback",
	}); err != nil {
		t.Fatal(err)
	}
	var activeCount int
	var activeBundle string
	if err := admin.QueryRow(ctx, `SELECT count(*), min(bundle_id) FROM project.source_bundle WHERE instance_id='instance-a' AND state='active'`).Scan(&activeCount, &activeBundle); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 || activeBundle != "bundle-1" {
		t.Fatalf("active bundles = %d/%q", activeCount, activeBundle)
	}
	var historyCount int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM project.resource_identity_history WHERE instance_id='instance-a' AND authored_id='orders'`).Scan(&historyCount); err != nil {
		t.Fatal(err)
	}
	if historyCount < 4 {
		t.Fatalf("orders history rows = %d, want at least 4", historyCount)
	}
	if _, err := admin.Exec(ctx, `DELETE FROM project.resource_identity WHERE instance_id='instance-a' AND authored_id='orders'`); err == nil {
		t.Fatal("resource identity deletion unexpectedly succeeded")
	}
	if _, err := admin.Exec(ctx, `UPDATE project.resource_identity_history SET reason='tampered' WHERE instance_id='instance-a' AND authored_id='orders'`); err == nil {
		t.Fatal("resource identity history mutation unexpectedly succeeded")
	}
}

func TestRestoreAndActivateRejectsMissingDuplicateActiveAndWrongKindIDs(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx := t.Context()
	base := candidate("instance-restore-validation", "bundle-1", "",
		resource("orders", projectgraph.KindSource),
	)
	if _, err := repo.Activate(ctx, base); err != nil {
		t.Fatal(err)
	}

	request := identityledger.Restore{
		Candidate: candidate("instance-restore-validation", "bundle-missing", "bundle-1", resource("orders", projectgraph.KindSource)),
		Reason:    "reviewed restore",
	}
	if _, err := repo.RestoreAndActivate(ctx, request); !errors.Is(err, identityledger.ErrInvalidInput) {
		t.Fatalf("missing restore IDs error = %v, want invalid input", err)
	}
	request.AuthoredIDs = []projectgraph.ResourceID{"orders", "orders"}
	if _, err := repo.RestoreAndActivate(ctx, request); !errors.Is(err, identityledger.ErrDuplicateAuthoredID) {
		t.Fatalf("duplicate restore IDs error = %v, want duplicate authored ID", err)
	}
	request.AuthoredIDs = []projectgraph.ResourceID{"orders"}
	if _, err := repo.RestoreAndActivate(ctx, request); !errors.Is(err, identityledger.ErrRestoreRequired) {
		t.Fatalf("active restore ID error = %v, want restore required", err)
	}

	if _, err := repo.Activate(ctx, candidate("instance-restore-validation", "bundle-2", "bundle-1", resource("customers", projectgraph.KindSource))); err != nil {
		t.Fatal(err)
	}
	wrongKind := identityledger.Restore{
		Candidate:   candidate("instance-restore-validation", "bundle-wrong-kind", "bundle-2", resource("orders", projectgraph.KindModel)),
		AuthoredIDs: []projectgraph.ResourceID{"orders"},
		Reason:      "reviewed restore",
	}
	if _, err := repo.RestoreAndActivate(ctx, wrongKind); !errors.Is(err, identityledger.ErrKindConflict) {
		t.Fatalf("wrong-kind restore ID error = %v, want kind conflict", err)
	}
}

func TestIdentityLedgerPostgreSQLReferenceReconciliationConflictsAndRetries(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := repo.Activate(ctx, candidate("instance-references", "bundle-1", "", resource("orders", projectgraph.KindSource), resource("orders-model", projectgraph.KindModel))); err != nil {
		t.Fatal(err)
	}
	desired := []identityledger.DurableReference{
		{InstanceID: "instance-references", ReferenceID: "grant-orders", OwnerAuthoredID: "grant-1", OwnerKind: "grant", TargetAuthoredID: "orders", ExpectedKind: projectgraph.KindSource},
		{InstanceID: "instance-references", ReferenceID: "grant-model", OwnerAuthoredID: "grant-2", OwnerKind: "grant", TargetAuthoredID: "orders-model", ExpectedKind: projectgraph.KindModel},
		{InstanceID: "instance-references", ReferenceID: "publication-model", OwnerAuthoredID: "publication-1", OwnerKind: "dashboard_publication", TargetAuthoredID: "orders-model", ExpectedKind: projectgraph.KindModel},
	}
	if _, err := repo.PutReference(ctx, identityledger.DurableReference{
		InstanceID: "instance-references", ReferenceID: "audit-orders", OwnerAuthoredID: "audit-1", OwnerKind: "audit", TargetAuthoredID: "orders", ExpectedKind: projectgraph.KindSource,
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.ReconcileReferences(ctx, "instance-references", desired); err != nil || len(got) != len(desired) {
		t.Fatalf("initial reference reconciliation = %#v, %v", got, err)
	}
	if _, err := repo.ReconcileReferences(ctx, "instance-references", nil); err != nil {
		t.Fatal(err)
	}
	for _, referenceID := range []string{"grant-orders", "grant-model", "publication-model"} {
		ref, err := repo.Reference(ctx, "instance-references", referenceID)
		if err != nil {
			t.Fatal(err)
		}
		if ref.Lifecycle != identityledger.ReferenceActive || ref.SuspendedAt != nil || ref.ReactivatedAt != nil {
			t.Fatalf("empty candidate changed %q = %#v", referenceID, ref)
		}
	}
	if _, err := repo.ReconcileReferences(ctx, "instance-references", desired[:1]); err != nil {
		t.Fatal(err)
	}
	modelRef, err := repo.Reference(ctx, "instance-references", "grant-model")
	if err != nil {
		t.Fatal(err)
	}
	if modelRef.Lifecycle != identityledger.ReferenceActive || modelRef.SuspendedAt != nil || modelRef.ReactivatedAt != nil {
		t.Fatalf("omitted model reference changed = %#v", modelRef)
	}
	auditRef, err := repo.Reference(ctx, "instance-references", "audit-orders")
	if err != nil {
		t.Fatal(err)
	}
	if auditRef.Lifecycle != identityledger.ReferenceActive {
		t.Fatalf("unrelated audit reference was changed = %#v", auditRef)
	}
	if _, err := repo.ReconcileReferences(ctx, "instance-references", desired[:1]); err != nil {
		t.Fatalf("idempotent reference reconciliation = %v", err)
	}
	if _, err := repo.ReconcileReferences(ctx, "instance-references", []identityledger.DurableReference{{
		InstanceID: "instance-references", ReferenceID: "grant-orders", OwnerAuthoredID: "changed-owner", OwnerKind: "grant", TargetAuthoredID: "orders", ExpectedKind: projectgraph.KindSource,
	}}); !errors.Is(err, identityledger.ErrReferenceConflict) {
		t.Fatalf("binding conflict = %v, want ErrReferenceConflict", err)
	}
	if _, err := repo.ReconcileReferences(ctx, "instance-references", []identityledger.DurableReference{{
		InstanceID: "instance-references", ReferenceID: "grant-kind", OwnerAuthoredID: "grant-kind", OwnerKind: "grant", TargetAuthoredID: "orders", ExpectedKind: projectgraph.KindModel,
	}}); !errors.Is(err, identityledger.ErrKindConflict) {
		t.Fatalf("target kind conflict = %v, want ErrKindConflict", err)
	}
	ordersRef, err := repo.Reference(ctx, "instance-references", "grant-orders")
	if err != nil {
		t.Fatal(err)
	}
	if ordersRef.OwnerAuthoredID != "grant-1" || ordersRef.Lifecycle != identityledger.ReferenceActive {
		t.Fatalf("conflicting reconciliation changed original binding = %#v", ordersRef)
	}
}

func TestIdentityLedgerPostgreSQL18ConcurrentActivation(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := repo.Activate(ctx, candidate("instance-race", "bundle-base", "", resource("orders", projectgraph.KindSource))); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, bundle := range []string{"bundle-left", "bundle-right"} {
		wg.Add(1)
		go func(bundle string) {
			defer wg.Done()
			<-start
			_, err := repo.Activate(ctx, candidate("instance-race", bundle, "bundle-base", resource("orders", projectgraph.KindSource)))
			errs <- err
		}(bundle)
	}
	close(start)
	wg.Wait()
	close(errs)
	var succeeded, conflicted int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, identityledger.ErrActivationConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent activation error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent outcomes success/conflict = %d/%d", succeeded, conflicted)
	}
}
