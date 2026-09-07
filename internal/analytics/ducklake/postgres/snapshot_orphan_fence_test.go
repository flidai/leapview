package postgres

import (
	"testing"
	"time"
)

func TestSnapshotOrphanScanIDForMaintenanceBindsOperationIdentity(t *testing.T) {
	maintenanceID := "0198f2c0-7c7a-7f00-8a11-0000000000b1"
	first := SnapshotOrphanScanIDForMaintenance(maintenanceID, "pool", "catalog")
	if first == "" {
		t.Fatal("scan ID is empty for UUIDv7 maintenance ID")
	}
	if second := SnapshotOrphanScanIDForMaintenance(maintenanceID, "pool", "catalog"); second != first {
		t.Fatalf("scan ID is not deterministic: %q != %q", first, second)
	}
	if changed := SnapshotOrphanScanIDForMaintenance(maintenanceID, "pool-other", "catalog"); changed == first {
		t.Fatal("scan ID did not change when pool identity changed")
	}
	if invalid := SnapshotOrphanScanIDForMaintenance("0198f2c0-7c7a-4f00-8a11-0000000000b1", "pool", "catalog"); invalid != "" {
		t.Fatalf("UUIDv4 maintenance ID produced scan ID %q", invalid)
	}
}

func TestSnapshotOrphanCoordinatorDefaultRenewalUpdatesFenceDeadline(t *testing.T) {
	r, _, poolID, catalogID := retentionTestRepository(t, "orphan_fence_renewal")
	fence, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{
		PhysicalPoolID: poolID,
		CatalogID:      catalogID,
		OwnerID:        "orphan-fence-renewal",
		LeaseExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.ReleaseRetentionMaintenanceFence(t.Context(), fence) })
	oldDeadline := fence.LeaseExpiresAt
	coordinator := &SnapshotOrphanCoordinator{Control: r}
	if err := coordinator.renewFenceBeforeNative(t.Context(), &fence); err != nil {
		t.Fatal(err)
	}
	if !fence.LeaseExpiresAt.After(oldDeadline) {
		t.Fatalf("renewed in-memory deadline=%s, old=%s", fence.LeaseExpiresAt, oldDeadline)
	}
	if err := r.CheckRetentionMaintenanceFence(t.Context(), fence); err != nil {
		t.Fatalf("renewed fence failed live check: %v", err)
	}
}
