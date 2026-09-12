package developmentprofile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

func TestCatalogFromManifestUsesExactCompilerIdentityAndType(t *testing.T) {
	manifest := projectmanifest.ResourceManifest{
		Connections: map[string]semanticmodel.Connection{
			"connection_warehouse": {Kind: "postgres"},
			"connection_public":    {Kind: "s3", Access: semanticmodel.ConnectionAccessPublic},
		},
		NameIndex: projectmanifest.NameIndex{Connections: map[string]string{
			"warehouse": "connection_warehouse",
			"files":     "connection_public",
		}},
	}
	catalog, err := CatalogFromManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if catalog["warehouse"].ID != "connection_warehouse" || catalog["warehouse"].ConnectorKind != "postgres" || catalog["files"].Access != semanticmodel.ConnectionAccessPublic {
		t.Fatalf("catalog = %#v", catalog)
	}

	manifest.NameIndex.Connections["copy"] = "connection_warehouse"
	if _, err := CatalogFromManifest(manifest); err == nil {
		t.Fatal("CatalogFromManifest accepted duplicate graph identity")
	}
	delete(manifest.NameIndex.Connections, "copy")
	delete(manifest.NameIndex.Connections, "files")
	if _, err := CatalogFromManifest(manifest); err == nil {
		t.Fatal("CatalogFromManifest accepted an incomplete name index")
	}
}

func TestLoadResolvesExactGraphIdentitiesAndSortsConnections(t *testing.T) {
	root := t.TempDir()
	file := writeProfile(t, root, `version: 1
profiles:
  local:
    connections:
      warehouse:
        endpoint:
          host: analytics.internal
          port: 5432
          database: commerce
          tlsMode: verify-full
        credentials:
          env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE
      public_files:
        endpoint:
          objectScope: s3://public-bucket/reports/
        credentials:
          none: true
`)
	selected, err := loadProfile(LoadOptions{
		CheckoutRoot: root, SourceRoot: filepath.Join(root, "dashboards"), ProfileFile: file,
		Connections: map[string]LogicalConnection{
			"warehouse":    {ID: "connection_warehouse", ConnectorKind: "postgres"},
			"public_files": {ID: "connection_public_files", ConnectorKind: "s3", Access: semanticmodel.ConnectionAccessPublic},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selected.ProfileName != DefaultProfileName || selected.File != file {
		t.Fatalf("selection = %#v", selected)
	}
	if names := []string{selected.Connections[0].Name, selected.Connections[1].Name}; !reflect.DeepEqual(names, []string{"public_files", "warehouse"}) {
		t.Fatalf("sorted names = %#v", names)
	}
	if selected.Connections[0].ID != "connection_public_files" || !selected.Connections[0].Credentials.None {
		t.Fatalf("public connection = %#v", selected.Connections[0])
	}
	if selected.Connections[1].ConnectorKind != "postgres" || selected.Connections[1].Credentials.EnvironmentVariable != "LEAPVIEW_DEV_CONNECTION_WAREHOUSE" {
		t.Fatalf("warehouse connection = %#v", selected.Connections[1])
	}
}

func TestLoadSelectionIsSingleFileAndProfile(t *testing.T) {
	checkout := t.TempDir()
	invocation := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, ".leapview"), 0o755); err != nil {
		t.Fatal(err)
	}
	defaultFile := writeNamedProfile(t, filepath.Join(checkout, DefaultRelativePath), `version: 1
profiles:
  local:
    connections: {}
  prod:
    connections: {}
`)
	explicitFile := writeNamedProfile(t, filepath.Join(invocation, "chosen.yaml"), `version: 1
profiles:
  local:
    connections: {}
`)

	selected, err := loadProfile(LoadOptions{CheckoutRoot: checkout, InvocationDirectory: invocation})
	if err != nil || selected.File != defaultFile || selected.ProfileName != "local" {
		t.Fatalf("default selection = %#v, %v", selected, err)
	}
	selected, err = loadProfile(LoadOptions{CheckoutRoot: checkout, InvocationDirectory: invocation, ProfileFile: "chosen.yaml"})
	if err != nil || selected.File != explicitFile {
		t.Fatalf("explicit selection = %#v, %v", selected, err)
	}
	_, err = loadProfile(LoadOptions{CheckoutRoot: checkout, InvocationDirectory: invocation, ProfileFile: "   "})
	assertDiagnostic(t, err, "profile.file", "")
	selected, err = loadProfile(LoadOptions{CheckoutRoot: checkout, ProfileName: "prod"})
	if err != nil || selected.File != defaultFile || selected.ProfileName != "prod" {
		t.Fatalf("prod remained-local selection = %#v, %v", selected, err)
	}
}

func TestLoadAllowsMissingDefaultOnlyWithoutExternalBindings(t *testing.T) {
	root := t.TempDir()
	selected, err := loadProfile(LoadOptions{CheckoutRoot: root, Connections: map[string]LogicalConnection{
		"fixture": {ID: "connection_fixture", ConnectorKind: "managed"},
	}})
	if err != nil || len(selected.Connections) != 0 {
		t.Fatalf("fixture selection = %#v, %v", selected, err)
	}
	_, err = loadProfile(LoadOptions{CheckoutRoot: root, Connections: map[string]LogicalConnection{
		"warehouse": {ID: "connection_warehouse", ConnectorKind: "postgres"},
	}})
	assertDiagnostic(t, err, "profile.setup_required", "")
	_, err = loadProfile(LoadOptions{CheckoutRoot: root, ProfileFile: "missing.yaml"})
	assertDiagnostic(t, err, "profile.file", "")
}

func TestLoadRejectsProfileFlagsForRemoteDevelopment(t *testing.T) {
	root := t.TempDir()
	for _, options := range []LoadOptions{
		{CheckoutRoot: root, RemoteTarget: "https://staging.example.com", ProfileFile: "profile.yaml"},
		{CheckoutRoot: root, RemoteTarget: "https://staging.example.com", ProfileName: "local"},
	} {
		_, err := loadProfile(options)
		assertDiagnostic(t, err, "profile.remote_conflict", "")
	}
}

func TestLoadRejectsProfileInsidePortableSourceRootAfterSymlinkResolution(t *testing.T) {
	checkout := t.TempDir()
	source := filepath.Join(checkout, "dashboards")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := writeNamedProfile(t, filepath.Join(source, "profile.yaml"), validEmptyProfile)
	_, err := loadProfile(LoadOptions{CheckoutRoot: checkout, SourceRoot: source, ProfileFile: inside})
	assertDiagnostic(t, err, "profile.portable_source", "")

	link := filepath.Join(checkout, "linked-profile.yaml")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	_, err = loadProfile(LoadOptions{CheckoutRoot: checkout, SourceRoot: source, ProfileFile: link})
	assertDiagnostic(t, err, "profile.portable_source", "")

	external := writeProfile(t, t.TempDir(), validEmptyProfile)
	escape := filepath.Join(source, "escape.yaml")
	if err := os.Symlink(external, escape); err != nil {
		t.Fatal(err)
	}
	_, err = loadProfile(LoadOptions{CheckoutRoot: checkout, SourceRoot: source, ProfileFile: escape})
	assertDiagnostic(t, err, "profile.portable_source", "")
}

func TestLoadRequiresSourceRootForExplicitProfile(t *testing.T) {
	checkout := t.TempDir()
	file := writeProfile(t, checkout, validEmptyProfile)

	_, err := Load(LoadOptions{CheckoutRoot: checkout, ProfileFile: file})
	assertDiagnostic(t, err, "profile.source", "")

	_, err = Load(LoadOptions{CheckoutRoot: checkout, SourceRoot: filepath.Join(checkout, "missing"), ProfileFile: file})
	assertDiagnostic(t, err, "profile.source", "")
}

func TestLoadRejectsDefaultProfileSymlinkOutsideCheckout(t *testing.T) {
	checkout := t.TempDir()
	external := writeProfile(t, t.TempDir(), validEmptyProfile)
	defaultFile := filepath.Join(checkout, DefaultRelativePath)
	if err := os.MkdirAll(filepath.Dir(defaultFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, defaultFile); err != nil {
		t.Fatal(err)
	}

	_, err := loadProfile(LoadOptions{CheckoutRoot: checkout})
	assertDiagnostic(t, err, "profile.checkout_escape", "")

	selected, err := loadProfile(LoadOptions{CheckoutRoot: checkout, ProfileFile: external})
	if err != nil || selected.File != external {
		t.Fatalf("explicit external selection = %#v, %v", selected, err)
	}
}

func TestLoadRejectsStrictDocumentViolations(t *testing.T) {
	tests := []struct {
		name, document, code string
	}{
		{"unsupported version", strings.Replace(validEmptyProfile, "version: 1", "version: 2", 1), "profile.version"},
		{"string version", strings.Replace(validEmptyProfile, "version: 1", `version: "1"`, 1), "profile.version"},
		{"missing version", strings.Replace(validEmptyProfile, "version: 1\n", "", 1), "profile.version"},
		{"unknown root field", validEmptyProfile + "target: prod\n", "profile.field_unknown"},
		{"duplicate root key", validEmptyProfile + "version: 1\n", "profile.duplicate_key"},
		{"duplicate nested key", strings.Replace(validEmptyProfile, "    connections: {}", "    connections: {}\n    connections: {}", 1), "profile.duplicate_key"},
		{"multiple documents", validEmptyProfile + "---\nversion: 1\nprofiles: {}\n", "profile.document"},
		{"anchor", "version: 1\nprofiles: &profiles\n  local:\n    connections: {}\n", "profile.yaml_feature"},
		{"alias", "version: 1\nprofiles: &profiles\n  local:\n    connections: {}\ncopy: *profiles\n", "profile.yaml_feature"},
		{"explicit standard tag", "version: !!int 1\nprofiles:\n  local:\n    connections: {}\n", "profile.yaml_feature"},
		{"explicit string tag", "version: 1\nprofiles:\n  local:\n    connections: !!map {}\n", "profile.yaml_feature"},
		{"template", strings.Replace(validEmptyProfile, "connections: {}", "connections: '{{ shell }}'", 1), "profile.template"},
		{"shell variable", strings.Replace(validEmptyProfile, "connections: {}", "connections: $HOME", 1), "profile.template"},
		{"non-map", "- version\n- 1\n", "profile.document"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			file := writeProfile(t, root, test.document)
			_, err := loadProfile(LoadOptions{CheckoutRoot: root, ProfileFile: file})
			assertDiagnostic(t, err, test.code, "")
		})
	}
}

func TestLoadValidatesUnselectedProfiles(t *testing.T) {
	root := t.TempDir()
	file := writeProfile(t, root, `version: 1
profiles:
  local:
    connections: {}
  broken:
    target: prod
`)
	_, err := loadProfile(LoadOptions{CheckoutRoot: root, ProfileFile: file})
	assertDiagnostic(t, err, "profile.field_unknown", "")
}

func TestLoadRejectsConnectionAndEndpointViolations(t *testing.T) {
	tests := []struct {
		name, connection, code string
	}{
		{"unknown graph name", "other", "profile.connection_unknown"},
		{"case mismatch", "Warehouse", "profile.connection_unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			file := writeProfile(t, root, postgresProfile(test.connection, "host: analytics.internal", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE"))
			_, err := loadProfile(LoadOptions{CheckoutRoot: root, ProfileFile: file, Connections: postgresCatalog()})
			assertDiagnostic(t, err, test.code, "")
		})
	}

	cases := []struct {
		name, endpoint, credentials, code string
	}{
		{"unknown endpoint field", "hostname: analytics.internal", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.field_unknown"},
		{"unknown connector option", "options:\n            data_path: /tmp/data", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.option_unknown"},
		{"secret option", "options:\n            password: exposed", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.option_unknown"},
		{"URL userinfo", "objectScope: 's3://user:password@bucket/path'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"opaque URL userinfo", "objectScope: 's3:user:password@bucket?mode=fast'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"encoded opaque URL userinfo", "objectScope: 's3:user%3Apassword%40bucket?mode=fast'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"relative URL userinfo", "objectScope: 'bucket/user:password@host?mode=fast'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"host username", "host: 'user@analytics.internal'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"URL secret query", "objectScope: 's3://bucket/path?access_key=exposed'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"relative URL secret query", "objectScope: 'bucket/path?token=exposed'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"opaque URL secret query", "objectScope: 's3:bucket/path?password=exposed'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"compound access key query", "objectScope: 's3://bucket/path?access_key_id=exposed'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"prefixed access key query", "objectScope: 's3://bucket/path?aws_access_key_id=exposed'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"camel-case client secret query", "objectScope: 's3://bucket/path?clientSecret=exposed'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"connection string query", "objectScope: 's3://bucket/path?connection_string=exposed'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"libpq credential assignment", "host: 'host=analytics.internal password=exposed'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"malformed URL query", "objectScope: 's3://bucket/path?mode=fast;ignored=value'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"URL secret fragment", "objectScope: 's3://bucket/path#token=exposed'", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint_secret"},
		{"invalid port", "port: 70000", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "profile.endpoint"},
		{"inline credentials", "host: analytics.internal", "password: exposed", "profile.field_unknown"},
		{"two credential modes", "host: analytics.internal", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE\nnone: true", "profile.credential_mode"},
		{"bad env reference", "host: analytics.internal", "env: DATABASE_URL", "profile.credential_env"},
		{"none private", "host: analytics.internal", "none: true", "profile.credential_none"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			file := writeProfile(t, root, postgresProfile("warehouse", test.endpoint, test.credentials))
			_, err := loadProfile(LoadOptions{CheckoutRoot: root, ProfileFile: file, Connections: postgresCatalog()})
			assertDiagnostic(t, err, test.code, "exposed")
		})
	}
}

func TestLoadAllowsNonSecretEndpointQuery(t *testing.T) {
	for _, endpoint := range []string{
		"objectScope: 's3://bucket/path?region=us-east-1'",
		"objectScope: 's3://bucket/path@version'",
		"objectScope: 's3://bucket/path?owner=foo@bar'",
	} {
		root := t.TempDir()
		file := writeProfile(t, root, postgresProfile("warehouse", endpoint, "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE"))
		if _, err := loadProfile(LoadOptions{CheckoutRoot: root, ProfileFile: file, Connections: postgresCatalog()}); err != nil {
			t.Fatalf("Load() rejected non-secret endpoint %q: %v", endpoint, err)
		}
	}
}

func TestLoadAllowsAbsoluteFilesystemPathsContainingAtSign(t *testing.T) {
	for _, test := range []struct {
		kind, option string
	}{
		{kind: "sqlite", option: "path"},
		{kind: "ducklake", option: "data_path"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			root := t.TempDir()
			file := writeProfile(t, root, postgresProfile(
				"warehouse",
				"options:\n            "+test.option+": 'C:\\Users\\john@example.com\\analytics.db'",
				"env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE",
			))
			_, err := loadProfile(LoadOptions{
				CheckoutRoot: root,
				ProfileFile:  file,
				Connections: map[string]LogicalConnection{
					"warehouse": {ID: "connection_warehouse", ConnectorKind: test.kind},
				},
			})
			if err != nil {
				t.Fatalf("Load() rejected %s path: %v", test.kind, err)
			}
		})
	}
}

func TestLoadRejectsCredentialAssignmentInAllowedEndpointOption(t *testing.T) {
	root := t.TempDir()
	file := writeProfile(t, root, postgresProfile(
		"warehouse",
		"options:\n            data_path: 'AccountName=example;AccountKey=exposed'",
		"env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE",
	))
	_, err := loadProfile(LoadOptions{
		CheckoutRoot: root,
		ProfileFile:  file,
		Connections: map[string]LogicalConnection{
			"warehouse": {ID: "connection_warehouse", ConnectorKind: "ducklake"},
		},
	})
	assertDiagnostic(t, err, "profile.endpoint_secret", "exposed")
}

func TestLoadRejectsManagedConnectionEntries(t *testing.T) {
	root := t.TempDir()
	file := writeProfile(t, root, postgresProfile("fixture", "objectScope: fixture", "none: true"))
	_, err := loadProfile(LoadOptions{CheckoutRoot: root, ProfileFile: file, Connections: map[string]LogicalConnection{
		"fixture": {ID: "connection_fixture", ConnectorKind: "managed", Access: semanticmodel.ConnectionAccessPublic},
	}})
	assertDiagnostic(t, err, "profile.connection_managed", "")
}

func TestLoadRejectsOversizedAndInvalidUTF8Documents(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "profile.yaml")
	if err := os.WriteFile(file, make([]byte, MaxDocumentBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadProfile(LoadOptions{CheckoutRoot: root, ProfileFile: file})
	assertDiagnostic(t, err, "profile.size", "")
	if err := os.WriteFile(file, []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = loadProfile(LoadOptions{CheckoutRoot: root, ProfileFile: file})
	assertDiagnostic(t, err, "profile.encoding", "")
}

func TestDiagnosticsNeverIncludeRejectedSecretValues(t *testing.T) {
	tests := []struct {
		name, document, code string
	}{
		{
			name:     "rejected value",
			document: postgresProfile("warehouse", "host: analytics.internal", "password: unmistakable-secret-value"),
			code:     "profile.field_unknown",
		},
		{
			name: "unknown root key",
			document: validEmptyProfile +
				`"postgres://user:unmistakable-secret-value@host": {}` + "\n",
			code: "profile.field_unknown",
		},
		{
			name: "invalid profile key",
			document: "version: 1\nprofiles:\n" +
				`  "postgres://user:unmistakable-secret-value@host":` + "\n    connections: {}\n",
			code: "profile.name",
		},
		{
			name:     "unknown connection key",
			document: postgresProfile(`"postgres://user:unmistakable-secret-value@host"`, "host: analytics.internal", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE"),
			code:     "profile.connection_unknown",
		},
		{
			name: "unknown option key",
			document: postgresProfile("warehouse",
				"options:\n            \"postgres://user:unmistakable-secret-value@host\": value",
				"env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE"),
			code: "profile.option_unknown",
		},
		{
			name: "duplicate key",
			document: validEmptyProfile +
				`"postgres://user:unmistakable-secret-value@host": {}` + "\n" +
				`"postgres://user:unmistakable-secret-value@host": {}` + "\n",
			code: "profile.duplicate_key",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			file := writeProfile(t, root, test.document)
			_, err := loadProfile(LoadOptions{CheckoutRoot: root, ProfileFile: file, Connections: postgresCatalog()})
			assertDiagnostic(t, err, test.code, "unmistakable-secret-value")
		})
	}
}

func assertDiagnostic(t *testing.T, err error, code, forbidden string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Load() error = nil, want %s", code)
	}
	var diagnostic *DiagnosticError
	if !errors.As(err, &diagnostic) || diagnostic.Code != code {
		t.Fatalf("Load() error = %T %v, want DiagnosticError code %s", err, err, code)
	}
	if forbidden != "" && (strings.Contains(err.Error(), forbidden) || strings.Contains(diagnostic.Field, forbidden)) {
		t.Fatalf("diagnostic leaked rejected value: %#v", diagnostic)
	}
}

func writeProfile(t *testing.T, root, contents string) string {
	t.Helper()
	return writeNamedProfile(t, filepath.Join(root, "profile.yaml"), contents)
}

func writeNamedProfile(t *testing.T, path, contents string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func postgresCatalog() map[string]LogicalConnection {
	return map[string]LogicalConnection{"warehouse": {ID: "connection_warehouse", ConnectorKind: "postgres"}}
}

func loadProfile(options LoadOptions) (Selected, error) {
	if strings.TrimSpace(options.SourceRoot) == "" {
		options.SourceRoot = filepath.Join(options.CheckoutRoot, "dashboards")
	}
	if err := os.MkdirAll(options.SourceRoot, 0o755); err != nil {
		return Selected{}, err
	}
	return Load(options)
}

func postgresProfile(name, endpoint, credentials string) string {
	return "version: 1\nprofiles:\n  local:\n    connections:\n      " + name + ":\n        endpoint:\n          " + strings.ReplaceAll(endpoint, "\n", "\n          ") + "\n        credentials:\n          " + strings.ReplaceAll(credentials, "\n", "\n          ") + "\n"
}

const validEmptyProfile = `version: 1
profiles:
  local:
    connections: {}
`

func FuzzProfileDocumentBoundary(f *testing.F) {
	f.Add([]byte(validEmptyProfile))
	f.Add([]byte(postgresProfile("warehouse", "host: analytics.internal", "env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE")))
	f.Add([]byte("version: 1\nprofiles: &p {local: {connections: {}}}\ncopy: *p\n"))
	f.Fuzz(func(t *testing.T, content []byte) {
		if len(content) > 64<<10 || !utf8.Valid(content) {
			return
		}
		root, err := parseDocument("profile.yaml", content)
		if err != nil {
			return
		}
		_, _ = resolveDocument("profile.yaml", DefaultProfileName, root, postgresCatalog())
	})
}
