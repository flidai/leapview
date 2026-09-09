package postgres

import (
	"testing"

	"github.com/flidai/leapview/internal/project/contractpublication"
)

func TestContractPublicationPersistsDirectAffectedResourceEvidence(t *testing.T) {
	db := identityTestDB(t)
	first, err := publishInTx(t, db, publicationInput(t, "1.0.0", false), contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineGenesis}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Validation.PolicyEvidence == nil {
		t.Fatal("published policy evidence is missing")
	}
	affected := first.Validation.PolicyEvidence.Affected()
	if len(affected) != 1 || affected[0].InstanceID != first.InstanceID || affected[0].AuthoredID != first.AuthoredID || affected[0].ResourceKind != first.ResourceKind || affected[0].Scope != contractpublication.PolicyAffectedResourceScope {
		t.Fatalf("published affected-resource evidence = %#v", affected)
	}

	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	replayed, err := New(tx).ReplayContractPublicationTx(t.Context(), tx, first.InstanceID, first.AuthoredID, first.ResourceKind, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !contractpublication.EqualContractPublicationContent(first, replayed) {
		t.Fatalf("replayed affected-resource evidence changed: first=%#v replayed=%#v", first.Validation.PolicyEvidence.Affected(), replayed.Validation.PolicyEvidence.Affected())
	}
}
