package runtimefactory

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/platform"
)

func TestDurableGCPoolIDsReadsPhysicalPoolAdmissions(t *testing.T) {
	store, err := platform.Open(t.Context(), t.TempDir()+"/state.db")
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()

	poolID := "sha256:" + strings.Repeat("a", 64)
	evidenceDigest := "sha256:" + strings.Repeat("b", 64)
	compatibilityDigest := "sha256:" + strings.Repeat("c", 64)
	if _, err := store.SQLDB().ExecContext(t.Context(), `
		INSERT INTO physical_pools (
			id, identity_digest, storage_location, storage_namespace,
			storage_implementation, object_naming_contract, encryption_domain, isolation_boundary,
			retention_authority, retention_policy_json
		) VALUES (?, ?, 'file:///tmp', 'delivery', 'local', 'uuidv7:v1', 'instance', 'instance', 'instance', '{}')`, poolID, poolID); err != nil {
		t.Fatalf("insert physical pool: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `
		INSERT INTO physical_pool_admissions (
			pool_id, compatibility_json, evidence_json, evidence_digest,
			compatibility_digest, conformance_version
		) VALUES (?, '{}', '{}', ?, ?, 'test:v1')`, poolID, evidenceDigest, compatibilityDigest); err != nil {
		t.Fatalf("insert physical-pool admission: %v", err)
	}

	poolIDs, err := durableGCPoolIDs(t.Context(), store.SQLDB())
	if err != nil {
		t.Fatalf("durableGCPoolIDs() error = %v", err)
	}
	if len(poolIDs) != 1 || poolIDs[0] != poolID {
		t.Fatalf("durableGCPoolIDs() = %#v, want [%q]", poolIDs, poolID)
	}
}

func TestBoundedGCHolderIDIsCanonicalForLongInputs(t *testing.T) {
	holder := strings.Repeat("holder/", 40)
	pool := strings.Repeat("pool:", 40)
	got := boundedGCHolderID(holder, pool)
	if err := deployment.ValidateDeliveryID(got); err != nil {
		t.Fatalf("bounded GC holder ID = %q, want canonical delivery ID: %v", got, err)
	}
	if got != boundedGCHolderID(holder, pool) {
		t.Fatal("bounded GC holder ID is not deterministic")
	}
}
