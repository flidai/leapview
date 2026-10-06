package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMinIOQualificationsUsePinnedSourceFixture(t *testing.T) {
	root := repoRoot(t)
	for _, path := range []string{
		"internal/analytics/ducklake/minio_conformance_test.go",
		"internal/app/providerrestore/provider_restore_qualification_test.go",
		"internal/manageddata/storage/s3/historical_qualification_test.go",
		"internal/recoveryset/observationstore/qualification_test.go",
		"internal/recoveryset/postgres/managed_data_s3_dr_seed_qualification_test.go",
		"internal/recoveryset/postgres/successor_s3_qualification_test.go",
	} {
		t.Run(path, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				t.Fatal(err)
			}
			text := string(content)
			if !strings.Contains(text, "testminio.Run(ctx,") || strings.Contains(text, "tcminio.Run(ctx,") {
				t.Fatal("MinIO qualifications must use the pinned source-built fixture, including on cold CI runners")
			}
			if strings.Contains(text, "quay.io/minio/minio") {
				t.Fatal("MinIO qualifications must not pull the unavailable prebuilt image")
			}
		})
	}
}
