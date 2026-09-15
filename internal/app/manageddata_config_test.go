package app

import (
	"testing"

	appconfig "github.com/flidai/leapview/internal/app/config"
)

func TestManagedDataProductConfigIncludesS3ObservationIdentity(t *testing.T) {
	product := managedDataProductConfig(appconfig.Config{
		ManagedDataBackend:                "s3",
		ManagedDataS3ObservationProfileID: "profile",
		ManagedDataS3ObservationAccount:   "account",
	})
	if product.S3ObservationProfileID != "profile" || product.S3ObservationAccount != "account" {
		t.Fatalf("managed-data product observation identity = %#v", product)
	}
}
