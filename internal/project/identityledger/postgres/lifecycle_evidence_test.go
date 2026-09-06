package postgres

import (
	"errors"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	"github.com/jackc/pgx/v5"
)

func TestReadLifecycleEvidenceTracksRestoreAndRollback(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx := t.Context()
	const instanceID = "instance-lifecycle-evidence"
	const authoredID = "source:orders"

	if _, err := repo.Activate(ctx, candidate(instanceID, "bundle-1", "", resource(authoredID, projectgraph.KindSource))); err != nil {
		t.Fatal(err)
	}
	firstPublication, err := repo.PublishContract(ctx, publicationInput(instanceID, authoredID, "1.2.3+build.1", "strict"))
	if err != nil {
		t.Fatal(err)
	}

	first, err := repo.ReadLifecycleEvidence(ctx, instanceID, authoredID, projectgraph.KindSource, "1.2.3+different-build")
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || first.Identity.Lifecycle != identityledger.LifecycleActive || first.Identity.ActiveBundleID != "bundle-1" {
		t.Fatalf("initial lifecycle evidence = %#v", first)
	}
	if !identityledger.EqualContractPublicationContent(first.Publication, firstPublication) {
		t.Fatalf("initial publication evidence changed: got=%#v want=%#v", first.Publication, firstPublication)
	}

	if _, err := repo.Activate(ctx, candidate(instanceID, "bundle-2", "bundle-1", resource("source:customers", projectgraph.KindSource))); err != nil {
		t.Fatal(err)
	}
	tombstoned, err := repo.ReadLifecycleEvidence(ctx, instanceID, authoredID, projectgraph.KindSource, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if tombstoned.Sequence != 2 || tombstoned.Identity.Lifecycle != identityledger.LifecycleTombstoned || tombstoned.Identity.ActiveBundleID != "" {
		t.Fatalf("tombstoned lifecycle evidence = %#v", tombstoned)
	}

	if _, err := repo.RestoreAndActivate(ctx, identityledger.Restore{
		Candidate:   candidate(instanceID, "bundle-3", "bundle-2", resource(authoredID, projectgraph.KindSource)),
		AuthoredIDs: []projectgraph.ResourceID{authoredID}, Reason: "reviewed restore",
	}); err != nil {
		t.Fatal(err)
	}
	restored, err := repo.ReadLifecycleEvidence(ctx, instanceID, authoredID, projectgraph.KindSource, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Sequence != 3 || restored.Identity.Lifecycle != identityledger.LifecycleActive || restored.Identity.ActiveBundleID != "bundle-3" {
		t.Fatalf("restored lifecycle evidence = %#v", restored)
	}

	if _, err := repo.Rollback(ctx, identityledger.Rollback{
		InstanceID: instanceID, BundleID: "bundle-1", ExpectedBundleID: "bundle-3",
		ActorID: "publisher", Reason: "release rollback",
	}); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := repo.ReadLifecycleEvidence(ctx, instanceID, authoredID, projectgraph.KindSource, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Sequence != 4 || rolledBack.Identity.Lifecycle != identityledger.LifecycleActive || rolledBack.Identity.ActiveBundleID != "bundle-1" {
		t.Fatalf("rolled-back lifecycle evidence = %#v", rolledBack)
	}
	if !identityledger.EqualContractPublicationContent(rolledBack.Publication, firstPublication) {
		t.Fatal("rollback changed immutable publication evidence")
	}
	if tombstoned.Sequence >= restored.Sequence || restored.Sequence >= rolledBack.Sequence {
		t.Fatalf("lifecycle sequence regressed: tombstone=%d restore=%d rollback=%d", tombstoned.Sequence, restored.Sequence, rolledBack.Sequence)
	}
}

func TestReadLifecycleEvidenceRejectsWrongScopeKindAndMissingPublication(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx := t.Context()
	const instanceID = "instance-lifecycle-evidence-validation"
	const authoredID = "source:orders"
	if _, err := repo.Activate(ctx, candidate(instanceID, "bundle-1", "", resource(authoredID, projectgraph.KindSource))); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PublishContract(ctx, publicationInput(instanceID, authoredID, "1.0.0", "strict")); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ReadLifecycleEvidence(ctx, instanceID, authoredID, projectgraph.KindModel, "1.0.0"); !errors.Is(err, identityledger.ErrKindConflict) {
		t.Fatalf("wrong-kind evidence error = %v, want kind conflict", err)
	}
	if _, err := repo.ReadLifecycleEvidence(ctx, "instance-lifecycle-evidence-other", authoredID, projectgraph.KindSource, "1.0.0"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wrong-instance evidence error = %v, want no rows", err)
	}
	if _, err := repo.ReadLifecycleEvidence(ctx, instanceID, authoredID, projectgraph.KindSource, "1.0.1"); !errors.Is(err, identityledger.ErrContractPublicationNotFound) {
		t.Fatalf("missing-publication evidence error = %v, want publication not found", err)
	}
}
