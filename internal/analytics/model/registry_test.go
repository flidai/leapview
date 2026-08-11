package model

import "testing"

func TestRegistryIncludesSupportedFormats(t *testing.T) {
	expected := []string{"csv", "json", "parquet", "excel", "text", "blob", "vortex", "delta", "iceberg", "lance"}
	for _, name := range expected {
		format, ok := LookupFormat(name)
		if !ok {
			t.Fatalf("format %q missing from registry", name)
		}
		if format.Name != name {
			t.Fatalf("format %q registered with name %q", name, format.Name)
		}
	}
}

func TestRegistryIncludesSupportedConnectionKinds(t *testing.T) {
	expected := []string{"managed", "s3", "r2", "gcs", "http", "azure_blob", "postgres", "mysql", "sqlite", "ducklake", "quack"}
	for _, kind := range expected {
		connection, ok := LookupConnection(kind)
		if !ok {
			t.Fatalf("connection kind %q missing from registry", kind)
		}
		if connection.Kind != kind {
			t.Fatalf("connection kind %q registered with kind %q", kind, connection.Kind)
		}
	}
	if _, ok := LookupConnection("local"); ok {
		t.Fatal("local connection kind must not be registered")
	}
}

func TestInferFormat(t *testing.T) {
	cases := map[string]string{
		"orders.csv":       "csv",
		"orders.csv.gz":    "csv",
		"orders.json":      "json",
		"orders.jsonl":     "json",
		"orders.ndjson":    "json",
		"orders.parquet":   "parquet",
		"orders.xlsx":      "excel",
		"orders.txt":       "text",
		"orders.blob":      "blob",
		"orders.vortex":    "vortex",
		"products.lance":   "lance",
		"nested/a/b/c.CSV": "csv",
	}
	for path, want := range cases {
		got, ok := InferFormat(path)
		if !ok || got != want {
			t.Fatalf("InferFormat(%q) = %q, %v; want %q, true", path, got, ok, want)
		}
	}
}

func TestRegistrySpecializedCapabilities(t *testing.T) {
	lance, _ := LookupFormat("lance")
	if lance.ScanKind != ScanReplacement || lance.SourceSecretType != "lance" || lance.AllowsOptions {
		t.Fatalf("lance registry = %#v, want replacement scan with lance source secret and no options", lance)
	}

	ducklake, _ := LookupConnection("ducklake")
	if ducklake.AttachKind != AttachDuckLake || ducklake.ObjectRelation != ObjectRelationAttach || !ducklake.AllowsObjectSource || !ducklake.RequiresPath {
		t.Fatalf("ducklake registry = %#v, want object attach with required path", ducklake)
	}

	quack, _ := LookupConnection("quack")
	if quack.AttachKind != AttachQuack || quack.ObjectRelation != ObjectRelationQuackQuery ||
		!quack.AllowsObjectSource || !equalStrings(quack.RequiredExtensions, []string{"httpfs", "quack"}) || quack.SecretType != "quack" {
		t.Fatalf("quack registry = %#v, want governed object connection", quack)
	}

	postgres, _ := LookupConnection("postgres")
	if postgres.AttachKind != AttachDatabase || postgres.ObjectRelation != ObjectRelationAttach || !postgres.AllowsObjectSource {
		t.Fatalf("postgres registry = %#v, want database object attach", postgres)
	}

	s3, _ := LookupConnection("s3")
	if !equalStrings(s3.RequiredExtensions, []string{"httpfs"}) || s3.SecretType != "s3" || !s3.AllowsPathSource {
		t.Fatalf("s3 registry = %#v, want httpfs path source", s3)
	}

}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestRegistryConnectionAuthPolicy(t *testing.T) {
	s3, _ := LookupConnection("s3")
	if !contains(s3.AuthKeys, "access_key_id") || !contains(s3.AuthKeys, "secret_access_key") {
		t.Fatalf("s3 auth keys = %#v, want access key fields", s3.AuthKeys)
	}
	if len(s3.RequiredAuthSets) != 1 || len(s3.RequiredAuthSets[0]) != 2 {
		t.Fatalf("s3 required auth sets = %#v, want key pair", s3.RequiredAuthSets)
	}

	azure, _ := LookupConnection("azure_blob")
	if len(azure.RequiredAuthSets) != 2 {
		t.Fatalf("azure required auth sets = %#v, want connection string or service principal", azure.RequiredAuthSets)
	}

	quack, _ := LookupConnection("quack")
	if len(quack.AuthKeys) != 1 || quack.AuthKeys[0] != "token" ||
		len(quack.RequiredAuthSets) != 1 || len(quack.RequiredAuthSets[0]) != 1 || quack.RequiredAuthSets[0][0] != "token" {
		t.Fatalf("quack auth policy = %#v, want token-only bundle", quack)
	}

}

func TestPathHelpers(t *testing.T) {
	if !IsLocalPath("orders.csv") {
		t.Fatal("orders.csv should be local")
	}
	if IsLocalPath("s3://bucket/orders.csv") {
		t.Fatal("s3 URI should be remote")
	}
	if got := JoinScope("s3://bucket/root/", "events/*"); got != "s3://bucket/root/events/*" {
		t.Fatalf("JoinScope = %q", got)
	}
	if !WithinScope("s3://bucket/root/", "s3://bucket/root/events/1.parquet") {
		t.Fatal("path should be inside scope")
	}
	if WithinScope("s3://bucket/root/", "s3://bucket/root-other/events.parquet") {
		t.Fatal("prefix sibling should not be inside scope")
	}
	if extension, ok := StorageExtension("az://warehouse/table"); !ok || extension != "azure" {
		t.Fatalf("StorageExtension azure = %q, %v", extension, ok)
	}
	if extension, ok := StorageExtension("https://example.com/data.parquet"); !ok || extension != "httpfs" {
		t.Fatalf("StorageExtension https = %q, %v", extension, ok)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
