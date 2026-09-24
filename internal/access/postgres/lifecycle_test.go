package postgres

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

const (
	lifecyclePrincipal  = "10000000-0000-7000-8000-000000000969"
	lifecycleOther      = "10000000-0000-7000-8000-000000000970"
	lifecycleOccurrence = "20000000-0000-7000-8000-000000000969"
	lifecycleRecovery   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func lifecycleTestFact(id, occurrence string) LifecycleFact {
	return LifecycleFact{
		Boundary:     LifecycleBoundary{CustomerID: "customer-test", DeploymentID: "deployment-test"},
		OccurrenceID: occurrence, ResourceID: id, Store: "principal", Action: "disable", Completed: true,
		SourceRevision: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
}

func lifecycleTestFrontier(t *testing.T, db auditDatabase, boundary LifecycleBoundary) LifecycleFrontier {
	t.Helper()
	tx, err := db.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	f, err := CaptureLifecycleFrontier(t.Context(), tx, boundary, lifecycleRecovery)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return f
}

func lifecycleTestPair(t *testing.T) (auditDatabase, auditDatabase, LifecycleFrontier) {
	t.Helper()
	source := newStandaloneAccessDatabase(t)
	restored := newStandaloneAccessDatabase(t)
	for _, db := range []auditDatabase{source, restored} {
		for _, id := range []string{lifecyclePrincipal, lifecycleOther} {
			if _, err := db.admin.Exec(t.Context(), `INSERT INTO access.principal(id,principal_type,status)
				VALUES ($1::uuid,'user','active')`, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	f := lifecycleTestFrontier(t, source, lifecycleTestFact(lifecyclePrincipal, lifecycleOccurrence).Boundary)
	if _, err := restored.admin.Exec(t.Context(), `UPDATE access.lifecycle_authority SET authority_id=$1::uuid`, f.SourceAuthorityID); err != nil {
		t.Fatal(err)
	}
	return source, restored, f
}

func lifecycleTestReconcile(t *testing.T, source, restored auditDatabase, f LifecycleFrontier) (LifecycleEvidence, error) {
	t.Helper()
	sourceTx, err := source.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sourceTx.Rollback(t.Context()) }()
	restoredTx, err := restored.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restoredTx.Rollback(t.Context()) }()
	evidence, err := ReconcileLifecycleActions(t.Context(), sourceTx, restoredTx, f.Boundary, f)
	if err == nil {
		if err = restoredTx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	return evidence, err
}

func TestLifecyclePostgreSQL18AtomicFrontierReplayAndIdempotency(t *testing.T) {
	source, restored, before := lifecycleTestPair(t)
	repo, err := NewAccess(source.runtime, FingerprintConfig{Key: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	fact := lifecycleTestFact(lifecyclePrincipal, lifecycleOccurrence)
	if _, err := repo.DisablePrincipalWithLifecycle(t.Context(), lifecyclePrincipal, fact); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := source.runtime.QueryRow(t.Context(), `SELECT status FROM access.principal WHERE id=$1::uuid`, lifecyclePrincipal).Scan(&status); err != nil || status != "disabled" {
		t.Fatalf("source disable = %q, %v", status, err)
	}
	after := lifecycleTestFrontier(t, source, fact.Boundary)
	if after.LastSequence <= before.LastSequence || after.SourceAuthorityID != before.SourceAuthorityID {
		t.Fatalf("frontier did not advance: before=%+v after=%+v", before, after)
	}
	// A snapshot after the action has the action in its frontier and needs no replay.
	evidence, err := lifecycleTestReconcile(t, source, restored, after)
	if err != nil || !evidence.Reconciled || evidence.Discovered != 0 {
		t.Fatalf("post-action frontier = %+v, %v", evidence, err)
	}
	if err := restored.runtime.QueryRow(t.Context(), `SELECT status FROM access.principal WHERE id=$1::uuid`, lifecyclePrincipal).Scan(&status); err != nil || status != "active" {
		t.Fatalf("post-action no-op unexpectedly mutated restored fixture = %q, %v", status, err)
	}
	// The stale snapshot requires replay; a second call makes no new mutation.
	evidence, err = lifecycleTestReconcile(t, source, restored, before)
	if err != nil || !evidence.Reconciled || evidence.Discovered != 1 || evidence.Replayed != 1 {
		t.Fatalf("stale replay = %+v, %v", evidence, err)
	}
	var disabledAt string
	if err := restored.runtime.QueryRow(t.Context(), `SELECT status,disabled_at::text FROM access.principal WHERE id=$1::uuid`, lifecyclePrincipal).Scan(&status, &disabledAt); err != nil || status != "disabled" {
		t.Fatalf("restored disable = %q/%q, %v", status, disabledAt, err)
	}
	evidence, err = lifecycleTestReconcile(t, source, restored, before)
	if err != nil || evidence.Replayed != 0 || evidence.AlreadySatisfied != 1 || !evidence.Reconciled {
		t.Fatalf("idempotent replay = %+v, %v", evidence, err)
	}
	var repeatDisabledAt string
	if err := restored.runtime.QueryRow(t.Context(), `SELECT disabled_at::text FROM access.principal WHERE id=$1::uuid`, lifecyclePrincipal).Scan(&repeatDisabledAt); err != nil || repeatDisabledAt != disabledAt {
		t.Fatalf("repeat replay changed disabled timestamp: %q -> %q (%v)", disabledAt, repeatDisabledAt, err)
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), lifecyclePrincipal) || strings.Contains(string(raw), fact.Boundary.CustomerID) || strings.Contains(string(raw), fact.SourceRevision) {
		t.Fatalf("reconciliation evidence leaked lifecycle identity: %s", raw)
	}
	// The wrapper's mutation and append share the same transaction: a reused
	// occurrence with a different resource rolls back the second mutation.
	conflict := lifecycleTestFact(lifecycleOther, lifecycleOccurrence)
	if _, err := repo.DisablePrincipalWithLifecycle(t.Context(), lifecycleOther, conflict); err == nil {
		t.Fatal("conflicting occurrence was accepted")
	}
	if err := source.runtime.QueryRow(t.Context(), `SELECT status FROM access.principal WHERE id=$1::uuid`, lifecycleOther).Scan(&status); err != nil || status != "active" {
		t.Fatalf("conflicting append committed source mutation: %q, %v", status, err)
	}
}

func TestLifecyclePostgreSQL18FailClosedOrderingAndIncomplete(t *testing.T) {
	tests := []struct {
		name string
		fact func() LifecycleFact
	}{
		{"cross customer", func() LifecycleFact {
			f := lifecycleTestFact(lifecyclePrincipal, lifecycleOccurrence)
			f.Boundary.CustomerID = "other-customer"
			return f
		}},
		{"unsupported store", func() LifecycleFact {
			f := lifecycleTestFact(lifecyclePrincipal, lifecycleOccurrence)
			f.Store = "object-storage"
			return f
		}},
		{"unsupported action", func() LifecycleFact {
			f := lifecycleTestFact(lifecyclePrincipal, lifecycleOccurrence)
			f.Action = "delete"
			return f
		}},
		{"incomplete", func() LifecycleFact {
			f := lifecycleTestFact(lifecyclePrincipal, lifecycleOccurrence)
			f.Completed = false
			return f
		}},
		{"unresolved principal", func() LifecycleFact {
			return lifecycleTestFact("10000000-0000-7000-8000-000000000971", lifecycleOccurrence)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, restored, frontier := lifecycleTestPair(t)
			tx, err := source.runtime.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			fact := test.fact()
			if _, err = RecordLifecycleAction(t.Context(), tx, fact); err != nil {
				_ = tx.Rollback(t.Context())
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			evidence, err := lifecycleTestReconcile(t, source, restored, frontier)
			if err == nil || evidence.Reconciled || evidence.Rejected != 1 {
				t.Fatalf("unsafe action accepted: %+v, %v", evidence, err)
			}
			var status string
			if err := restored.runtime.QueryRow(t.Context(), `SELECT status FROM access.principal WHERE id=$1::uuid`, lifecyclePrincipal).Scan(&status); err != nil || status != "active" {
				t.Fatalf("failed replay changed restored state: %q, %v", status, err)
			}
		})
	}
}

func TestLifecyclePostgreSQL18MultipleActionsAndMalformedIdentity(t *testing.T) {
	source, restored, frontier := lifecycleTestPair(t)
	tx, err := source.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first, err := RecordLifecycleAction(t.Context(), tx, lifecycleTestFact(lifecyclePrincipal, lifecycleOccurrence))
	if err != nil {
		t.Fatal(err)
	}
	secondFact := lifecycleTestFact(lifecyclePrincipal, "20000000-0000-7000-8000-000000000970")
	second, err := RecordLifecycleAction(t.Context(), tx, secondFact)
	if err != nil || second.Sequence <= first.Sequence {
		t.Fatalf("event sequence = %d,%d %v", first.Sequence, second.Sequence, err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	evidence, err := lifecycleTestReconcile(t, source, restored, frontier)
	if err != nil || evidence.Discovered != 2 || evidence.Replayed != 1 || evidence.AlreadySatisfied != 1 {
		t.Fatalf("ordered replay = %+v, %v", evidence, err)
	}
	if _, err := RecordLifecycleAction(t.Context(), nil, secondFact); err == nil {
		t.Fatal("nil transaction accepted")
	}
	bad := secondFact
	bad.OccurrenceID = "not-a-uuid"
	readTx, err := source.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readTx.Rollback(t.Context()) }()
	if _, err := RecordLifecycleAction(t.Context(), readTx, bad); err == nil {
		t.Fatal("malformed occurrence accepted")
	}
	wrong := frontier
	wrong.SourceAuthorityID = "10000000-0000-7000-8000-000000000999"
	evidence, err = lifecycleTestReconcile(t, source, restored, wrong)
	if err == nil || evidence.Reconciled {
		t.Fatalf("wrong source authority accepted: %+v, %v", evidence, err)
	}
	missing := frontier
	missing.RecoveryIdentity = ""
	evidence, err = lifecycleTestReconcile(t, source, restored, missing)
	if err == nil || evidence.Reconciled || evidence.RecoveryIdentity != "" {
		t.Fatalf("missing recovery identity accepted: %+v, %v", evidence, err)
	}
	restoredTx, err := restored.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restoredTx.Rollback(t.Context()) }()
	wrongBoundary := frontier.Boundary
	wrongBoundary.CustomerID = "other-customer"
	evidence, err = ReconcileLifecycleActions(t.Context(), readTx, restoredTx, wrongBoundary, frontier)
	if err == nil || evidence.Reconciled || evidence.Rejected != 1 {
		t.Fatalf("wrong restored boundary accepted: %+v, %v", evidence, err)
	}
}

func TestLifecyclePostgreSQL18LateRejectionRollsBackReplay(t *testing.T) {
	source, restored, frontier := lifecycleTestPair(t)
	tx, err := source.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecordLifecycleAction(t.Context(), tx, lifecycleTestFact(lifecyclePrincipal, lifecycleOccurrence)); err != nil {
		t.Fatal(err)
	}
	unsupported := lifecycleTestFact(lifecycleOther, "20000000-0000-7000-8000-000000000970")
	unsupported.Store = "object-storage"
	unsupported.Action = "delete"
	if _, err := RecordLifecycleAction(t.Context(), tx, unsupported); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	sourceTx, err := source.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sourceTx.Rollback(t.Context()) }()
	restoredTx, err := restored.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := ReconcileLifecycleActions(t.Context(), sourceTx, restoredTx, frontier.Boundary, frontier)
	if err == nil || evidence.Replayed != 0 || evidence.Reconciled || evidence.Rejected != 1 {
		t.Fatalf("partial replay accepted: %+v, %v", evidence, err)
	}
	// Even an incorrect caller commit cannot persist the partially replayed row.
	if err := restoredTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := restored.runtime.QueryRow(t.Context(), `SELECT status FROM access.principal WHERE id=$1::uuid`, lifecyclePrincipal).Scan(&status); err != nil || status != "active" {
		t.Fatalf("partial replay survived savepoint rollback: %q, %v", status, err)
	}
}

func TestLifecyclePostgreSQL18ConcurrentReplayIsIdempotent(t *testing.T) {
	source, restored, frontier := lifecycleTestPair(t)
	repo, err := NewAccess(source.runtime, FingerprintConfig{Key: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DisablePrincipalWithLifecycle(t.Context(), lifecyclePrincipal, lifecycleTestFact(lifecyclePrincipal, lifecycleOccurrence)); err != nil {
		t.Fatal(err)
	}
	type result struct {
		evidence LifecycleEvidence
		err      error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sourceTx, err := source.runtime.Begin(t.Context())
			if err != nil {
				results <- result{err: err}
				return
			}
			defer func() { _ = sourceTx.Rollback(t.Context()) }()
			restoredTx, err := restored.runtime.Begin(t.Context())
			if err != nil {
				results <- result{err: err}
				return
			}
			defer func() { _ = restoredTx.Rollback(t.Context()) }()
			evidence, err := ReconcileLifecycleActions(t.Context(), sourceTx, restoredTx, frontier.Boundary, frontier)
			if err == nil {
				err = restoredTx.Commit(t.Context())
			}
			results <- result{evidence: evidence, err: err}
		}()
	}
	wg.Wait()
	close(results)
	var replayed, satisfied int
	for r := range results {
		if r.err != nil || !r.evidence.Reconciled {
			t.Fatalf("concurrent replay failed: %+v, %v", r.evidence, r.err)
		}
		replayed += r.evidence.Replayed
		satisfied += r.evidence.AlreadySatisfied
	}
	if replayed != 1 || satisfied != 1 {
		t.Fatalf("concurrent replay counts = %d replayed, %d satisfied", replayed, satisfied)
	}
}
