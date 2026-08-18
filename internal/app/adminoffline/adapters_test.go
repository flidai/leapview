package adminoffline

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/platform"
)

func TestDeliveryAuditRejectsMissingPoolBeforeOpeningControlStore(t *testing.T) {
	_, err := (deliveryRepair{}).AuditDeliveryRoots(context.Background(), adminoffline.DeliveryAuditRequest{})
	if err == nil || !strings.Contains(err.Error(), "physical-pool identity") {
		t.Fatalf("audit error = %v, want missing pool identity", err)
	}
}

func TestSameDeliveryRootSetDetectsIdentityDrift(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	left := []deployment.DeliveryRoot{
		{PhysicalPoolID: "pool", Kind: "candidate", SourceID: "candidate-a", CatalogDigest: "sha256:a", ObjectKey: "catalogs/a", Status: "active", CreatedAt: now},
		{PhysicalPoolID: "pool", Kind: "candidate", SourceID: "candidate-b", CatalogDigest: "sha256:b", ObjectKey: "catalogs/b", Status: "active", CreatedAt: now},
	}
	if !sameDeliveryRootSet(left, append([]deployment.DeliveryRoot(nil), left...)) {
		t.Fatal("identical root sets reported as drifted")
	}
	right := append([]deployment.DeliveryRoot(nil), left...)
	right[0].CatalogDigest = "sha256:c"
	if sameDeliveryRootSet(left, right) {
		t.Fatal("changed root identity accepted")
	}
	right = []deployment.DeliveryRoot{left[0], left[0]}
	if sameDeliveryRootSet(left, right) {
		t.Fatal("duplicate final root hid a missing root")
	}
}

func TestOpenDeliveryAuditDBIsQueryOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leapview.db")
	store, err := platform.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openDeliveryAuditDB(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE audit_probe (id INTEGER)"); err == nil {
		t.Fatal("query-only audit database accepted a write")
	}
}
