//go:build leapview_static_azure && duckdb_use_static_lib

package duckdb

import "testing"

func TestNativeAzureStaticReadAndRecovery(t *testing.T) {
	db := openNativeConnectorDB(t, "azure")
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM duckdb_extensions() WHERE extension_name='azure' AND installed AND install_mode='STATICALLY_LINKED' AND extension_version='563589b2f24290a4dcdd4247eaedf2b544f9dbcd'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("selected Azure registration count=%d: %v", count, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	testNativeHTTPAndAzureSourceReadAndRecovery(t, "azure_blob")
}
