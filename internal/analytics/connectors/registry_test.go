package connectors

import (
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	"testing"
)

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

func TestSourceDataIdentityCapabilitiesFailClosedExceptManagedContent(t *testing.T) {
	managed, ok := LookupConnection("managed")
	if !ok || managed.SourceDataIdentityCapability != SourceDataIdentityContentRevision {
		t.Fatalf("managed source identity capability = %#v, want content revision", managed)
	}
	for _, kind := range []string{"http", "s3", "r2", "gcs", "azure_blob", "postgres", "mysql", "sqlite", "ducklake", "quack"} {
		spec, ok := LookupConnection(kind)
		if !ok || spec.SourceDataIdentityCapability != SourceDataIdentityUnavailable {
			t.Fatalf("connector %q source identity capability = %#v, want unavailable", kind, spec)
		}
	}
	for _, name := range FormatNames() {
		format, ok := LookupFormat(name)
		if !ok || format.SourceDataIdentityCapability != SourceDataIdentityUnavailable {
			t.Fatalf("format %q source identity capability = %#v, want unavailable", name, format)
		}
	}
}

func TestConnectionActivationModesAreExplicit(t *testing.T) {
	expected := map[string]ActivationMode{
		"managed": ManagedActivation,
		"http":    AuthoredActivation,
		"s3":      TargetBindingActivation, "r2": TargetBindingActivation,
		"gcs": TargetBindingActivation, "azure_blob": TargetBindingActivation,
		"postgres": TargetBindingActivation, "mysql": TargetBindingActivation,
		"sqlite": TargetBindingActivation, "ducklake": TargetBindingActivation,
		"quack": TargetBindingActivation,
	}
	for kind, want := range expected {
		spec, ok := LookupConnection(kind)
		if !ok || spec.ActivationMode != want {
			t.Fatalf("connection %q activation mode = %q, want %q", kind, spec.ActivationMode, want)
		}
	}
}

func TestPublicAccessCapabilityMatchesGeneratedConnectorDeclarations(t *testing.T) {
	for _, kind := range []string{"managed", "s3", "r2", "gcs", "http", "azure_blob"} {
		spec, ok := LookupConnection(kind)
		if !ok || !spec.AllowPublicAccess {
			t.Fatalf("connector %q public capability = %#v, want generated access support", kind, spec)
		}
	}
	for _, kind := range []string{"postgres", "mysql", "sqlite", "ducklake", "quack"} {
		spec, ok := LookupConnection(kind)
		if !ok || spec.AllowPublicAccess {
			t.Fatalf("connector %q public capability = %#v, want generated access rejection", kind, spec)
		}
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

func TestGeneratedRegistryHasTotalFormatAdapters(t *testing.T) {
	if len(projectcontracts.FormatRegistry) != len(projectcontracts.PathFormatNames) {
		t.Fatalf("generated format registry = %d entries, names = %d", len(projectcontracts.FormatRegistry), len(projectcontracts.PathFormatNames))
	}
	seen := map[string]struct{}{}
	for _, profile := range projectcontracts.FormatRegistry {
		if _, duplicate := seen[profile.Name]; duplicate {
			t.Fatalf("duplicate generated format profile %q", profile.Name)
		}
		seen[profile.Name] = struct{}{}
		format, ok := LookupFormat(profile.Name)
		if !ok || format.Name != profile.Name {
			t.Fatalf("generated format %q has no runtime adapter: %#v, ok=%v", profile.Name, format, ok)
		}
	}
	for _, name := range FormatNames() {
		if _, ok := seen[name]; !ok {
			t.Fatalf("runtime format %q is absent from generated registry", name)
		}
	}
}

func TestGeneratedRegistryHasTotalConnectionAdapters(t *testing.T) {
	if len(projectcontracts.ConnectorRegistry) == 0 {
		t.Fatal("generated connector registry is empty")
	}
	seen := map[string]struct{}{}
	for _, profile := range projectcontracts.ConnectorRegistry {
		if profile.Key == "" || profile.AdapterKey == "" {
			t.Fatalf("generated connector profile has incomplete identity: %#v", profile)
		}
		if _, duplicate := seen[profile.Key]; duplicate {
			t.Fatalf("duplicate generated connector profile %q", profile.Key)
		}
		seen[profile.Key] = struct{}{}
		connection, ok := LookupConnection(profile.Key)
		if !ok {
			t.Fatalf("generated connector %q has no runtime adapter (adapter key %q)", profile.Key, profile.AdapterKey)
		}
		if connection.Kind != profile.Key {
			t.Fatalf("generated connector %q resolved with private adapter identity %q", profile.Key, connection.Kind)
		}
	}
}

func TestLookupConnectionResolvesPrivateAdapterKey(t *testing.T) {
	const publicKey = "fixture_public_connector"
	const adapterKey = "fixture_private_adapter"

	originalRegistry := projectcontracts.ConnectorRegistry
	originalConnections := connections
	t.Cleanup(func() {
		projectcontracts.ConnectorRegistry = originalRegistry
		connections = originalConnections
	})

	registry := append([]projectcontracts.ConnectorProfile(nil), originalRegistry...)
	registry = append(registry, projectcontracts.ConnectorProfile{
		Key:                  publicKey,
		SchemaName:           "FixtureConnection",
		ActivationMode:       projectcontracts.ConnectorActivationMode("authored"),
		LocationCapabilities: []string{KindPath},
		ApprovedExtensions:   []string{"httpfs"},
		SecretType:           "fixture",
		SupportStatus:        projectcontracts.ConnectorSupportStatus("experimental"),
		AdapterKey:           adapterKey,
		AllowPublicAccess:    true,
	})
	projectcontracts.ConnectorRegistry = registry

	implementation := make(map[string]ConnectionSpec, len(originalConnections)+1)
	for key, value := range originalConnections {
		implementation[key] = value
	}
	implementation[adapterKey] = ConnectionSpec{
		ActivationMode:     AuthoredActivation,
		SecretType:         "fixture",
		RequiredExtensions: []string{"httpfs"},
		AllowsPathSource:   true,
		AllowPublicAccess:  true,
	}
	connections = implementation

	got, ok := LookupConnection(publicKey)
	if !ok {
		t.Fatal("fixture connector was not resolved")
	}
	if got.Kind != publicKey {
		t.Fatalf("fixture public identity = %q, want %q", got.Kind, publicKey)
	}
	if got.SecretType != "fixture" || !got.AllowsPathSource || !got.AllowPublicAccess {
		t.Fatalf("fixture runtime capabilities = %#v", got)
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

	managed, _ := LookupConnection("managed")
	if !managed.AllowNoAuth || len(managed.AuthKeys) != 0 || !managed.AllowsPathSource {
		t.Fatalf("managed auth policy = %#v, want no-auth path source", managed)
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
	for _, escaped := range []string{
		"s3://bucket/root/../private/events.parquet",
		"s3://bucket/root/%2e%2e/private/events.parquet",
		"s3://other/root/events.parquet",
	} {
		if WithinScope("s3://bucket/root/", escaped) {
			t.Fatalf("escaped path %q should not be inside scope", escaped)
		}
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
