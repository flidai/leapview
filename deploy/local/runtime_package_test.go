package local

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const postgresImage = "docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8"

type composeDocument struct {
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]composeVolume  `yaml:"volumes"`
}

type composeService struct {
	Image       string                    `yaml:"image"`
	Command     []string                  `yaml:"command"`
	NetworkMode string                    `yaml:"network_mode"`
	Environment map[string]string         `yaml:"environment"`
	Ports       []string                  `yaml:"ports"`
	Volumes     []any                     `yaml:"volumes"`
	Labels      map[string]string         `yaml:"labels"`
	DependsOn   map[string]map[string]any `yaml:"depends_on"`
	ReadOnly    bool                      `yaml:"read_only"`
	CapDrop     []string                  `yaml:"cap_drop"`
}

type composeVolume struct {
	Labels map[string]string `yaml:"labels"`
}

func TestLocalRuntimeComposeContract(t *testing.T) {
	document := readCompose(t)
	serviceNames := sortedKeys(document.Services)
	if got, want := strings.Join(serviceNames, ","), "leapview,postgres"; got != want {
		t.Fatalf("services = %q, want %q", got, want)
	}

	postgres := document.Services["postgres"]
	if postgres.Image != postgresImage {
		t.Fatalf("PostgreSQL image = %q, want immutable PostgreSQL 18 image %q", postgres.Image, postgresImage)
	}
	if got := strings.Join(postgres.Command, " "); got != "postgres -c listen_addresses=127.0.0.1" {
		t.Fatalf("PostgreSQL command = %q, want namespace-loopback listener", got)
	}
	if len(postgres.Ports) != 1 || postgres.Ports[0] != "127.0.0.1:${LEAPVIEW_LOCAL_APP_PORT:?local application port is required}:8080" {
		t.Fatalf("PostgreSQL namespace ports = %#v, want one loopback application publication", postgres.Ports)
	}
	for _, port := range postgres.Ports {
		if strings.HasSuffix(port, ":5432") {
			t.Fatal("local runtime publishes PostgreSQL to the host")
		}
	}
	if !containsVolume(postgres.Volumes, "leapview-postgres:/var/lib/postgresql") ||
		!containsVolume(postgres.Volumes, "./postgres-init.sh:/docker-entrypoint-initdb.d/10-leapview-roles.sh:ro") {
		t.Fatalf("PostgreSQL volumes = %#v, want persistent data and read-only canonical initializer", postgres.Volumes)
	}

	application := document.Services["leapview"]
	if application.Image != "${LEAPVIEW_IMAGE:?version-matched LeapView image digest is required}" {
		t.Fatalf("LeapView image = %q, want required version-matched image", application.Image)
	}
	if application.NetworkMode != "service:postgres" {
		t.Fatalf("LeapView network mode = %q, want PostgreSQL namespace sharing", application.NetworkMode)
	}
	if got := strings.Join(application.Command, " "); got != "serve --environment dev" {
		t.Fatalf("LeapView command = %q, want explicit non-production development serve", got)
	}
	if !application.ReadOnly || !contains(application.CapDrop, "ALL") {
		t.Fatal("LeapView service is missing the read-only/capability-drop boundary")
	}
	if _, ok := application.DependsOn["postgres"]; !ok {
		t.Fatal("LeapView service does not wait for PostgreSQL")
	}
	if !containsVolume(application.Volumes, "leapview-runtime:/var/lib/leapview") {
		t.Fatalf("LeapView volumes = %#v, want persistent private runtime volume", application.Volumes)
	}

	requiredEnvironment := map[string]string{
		"LEAPVIEW_ADDR":                               "0.0.0.0:8080",
		"LEAPVIEW_PUBLIC_URL":                         "http://127.0.0.1:${LEAPVIEW_LOCAL_APP_PORT:?local application port is required}",
		"LEAPVIEW_ALLOWED_HOSTS":                      "127.0.0.1,localhost",
		"LEAPVIEW_ENVIRONMENT":                        "dev",
		"LEAPVIEW_PRODUCTION":                         "false",
		"LEAPVIEW_HOME":                               "/var/lib/leapview/home",
		"LEAPVIEW_MANAGED_DATA_BACKEND":               "local",
		"LEAPVIEW_MANAGED_DATA_DIR":                   "/var/lib/leapview/home/managed-data",
		"LEAPVIEW_OBJECT_STORE_BACKEND":               "filesystem",
		"LEAPVIEW_OBJECT_STORE_FILESYSTEM_ROOT":       "/var/lib/leapview/home/artifacts/object-store",
		"LEAPVIEW_LOCAL_AUTH":                         "true",
		"LEAPVIEW_POSTGRES_EXPECTED_MAJOR":            "18",
		"LEAPVIEW_POSTGRES_REQUIRE_TLS":               "false",
		"LEAPVIEW_CONTRIBUTOR_DIAGNOSTICS":            "false",
		"LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE":      "leapview_control_runtime",
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE":     "leapview_control_migrator",
		"LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE":     "leapview_control_readonly",
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_ROLE":  "leapview_control_maintenance",
		"LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE":     "leapview_ducklake_runtime",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_ROLE": "leapview_ducklake_maintenance",
	}
	for name, want := range requiredEnvironment {
		if got := application.Environment[name]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{
		"LEAPVIEW_POSTGRES_CONTROL_URL",
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_CONTROL_READONLY_URL",
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL",
		"LEAPVIEW_POSTGRES_DUCKLAKE_URL",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL",
	} {
		value := application.Environment[name]
		if !strings.Contains(value, "@127.0.0.1:5432/") || !strings.HasSuffix(value, "?sslmode=disable") {
			t.Errorf("%s must remain on the shared loopback network namespace", name)
		}
	}
	for _, operationOnly := range []string{
		"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL",
	} {
		if _, ok := application.Environment[operationOnly]; ok {
			t.Errorf("ordinary serving received operation-only credential %s", operationOnly)
		}
	}
	for _, forbidden := range []string{"LEAPVIEW_DEV_AUTH_BYPASS", "LEAPVIEW_DEV_API_TOKEN"} {
		if _, ok := application.Environment[forbidden]; ok {
			t.Errorf("released local runtime enabled contributor-only authentication setting %s", forbidden)
		}
	}

	volumeNames := sortedKeys(document.Volumes)
	if got, want := strings.Join(volumeNames, ","), "leapview-postgres,leapview-runtime"; got != want {
		t.Fatalf("volumes = %q, want %q", got, want)
	}
	for name, volume := range document.Volumes {
		assertOwnershipLabels(t, "volume "+name, volume.Labels)
	}
	for name, service := range document.Services {
		assertOwnershipLabels(t, "service "+name, service.Labels)
	}
}

func TestLocalRuntimeUsesCanonicalPostgresAuthority(t *testing.T) {
	initializer := readFile(t, filepath.Join("..", "postgres", "init.sh"))
	for _, required := range []string{
		"ensure_database leapview_control leapview_control_owner",
		"ensure_database leapview_ducklake leapview_ducklake_owner",
		"CREATE ROLE leapview_control_runtime",
		"CREATE ROLE leapview_control_migrator",
		"CREATE ROLE leapview_ducklake_runtime",
		"CREATE ROLE leapview_ducklake_migrator",
	} {
		if !strings.Contains(initializer, required) {
			t.Errorf("canonical PostgreSQL initializer missing %q", required)
		}
	}

	workflow := readFile(t, filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	for _, required := range []string{
		`cp deploy/local/compose.yaml deploy/local/README.md deploy/local/runtime-package.schema.json "$local_runtime_dir/"`,
		`cp deploy/postgres/init.sh "$local_runtime_dir/postgres-init.sh"`,
		`"schemaVersion": 1`,
		`"persistentStateSchemaVersion": 1`,
		`"composeMinimumVersion": "2.17.0"`,
		`"image": os.environ["IMAGE_REFERENCE"]`,
		`"major": 18`,
		`"image": os.environ["POSTGRES_IMAGE"]`,
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow missing local runtime package contract %q", required)
		}
	}

	schema := readFile(t, "runtime-package.schema.json")
	for _, required := range []string{
		`"additionalProperties": false`,
		`"const": "2.17.0"`,
		`"pattern": "^[^@\\s]+@sha256:[0-9a-f]{64}$"`,
		`"const": 18`,
		postgresImage,
	} {
		if !strings.Contains(schema, required) {
			t.Errorf("runtime package schema missing %q", required)
		}
	}
}

func TestLocalRuntimePackageSchemaRejectsIdentityDrift(t *testing.T) {
	encoded := []byte(readFile(t, "runtime-package.schema.json"))
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("runtime-package.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("runtime-package.json")
	if err != nil {
		t.Fatal(err)
	}

	valid := map[string]any{
		"schemaVersion":                float64(1),
		"persistentStateSchemaVersion": float64(1),
		"composeMinimumVersion":        "2.17.0",
		"leapview": map[string]any{
			"version":  "v1.2.3",
			"revision": strings.Repeat("a", 40),
			"image":    "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64),
		},
		"postgres": map[string]any{
			"major": float64(18),
			"image": postgresImage,
		},
	}
	if err := schema.Validate(valid); err != nil {
		t.Fatalf("valid release manifest rejected: %v", err)
	}

	for name, mutate := range map[string]func(map[string]any){
		"unknown field":  func(value map[string]any) { value["target"] = "prod" },
		"unknown schema": func(value map[string]any) { value["schemaVersion"] = float64(2) },
		"state upgrade":  func(value map[string]any) { value["persistentStateSchemaVersion"] = float64(2) },
		"older compose":  func(value map[string]any) { value["composeMinimumVersion"] = "2.16.0" },
		"mutable app image": func(value map[string]any) {
			value["leapview"].(map[string]any)["image"] = "ghcr.io/flidai/leapview:v1.2.3"
		},
		"wrong revision": func(value map[string]any) {
			value["leapview"].(map[string]any)["revision"] = "main"
		},
		"different postgres": func(value map[string]any) {
			value["postgres"].(map[string]any)["image"] = "docker.io/library/postgres:18-alpine"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneJSONDocument(t, valid)
			mutate(candidate)
			if err := schema.Validate(candidate); err == nil {
				t.Fatal("runtime package schema accepted incompatible identity")
			}
		})
	}
}

func TestLocalRuntimeComposeRendersWithoutDaemon(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker CLI is unavailable")
	}
	command := exec.Command("docker", "compose", "--project-name", "leapview-local-contract", "--file", "compose.yaml", "config", "--quiet")
	command.Env = append(os.Environ(), localRuntimeTestEnvironment()...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("docker compose config failed: %v\n%s", err, output)
	}
}

func readCompose(t *testing.T) composeDocument {
	t.Helper()
	var document composeDocument
	if err := yaml.Unmarshal([]byte(readFile(t, "compose.yaml")), &document); err != nil {
		t.Fatalf("parse compose.yaml: %v", err)
	}
	return document
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func assertOwnershipLabels(t *testing.T, name string, labels map[string]string) {
	t.Helper()
	want := map[string]string{
		"io.leapview.local-runtime":          "true",
		"io.leapview.local-runtime.schema":   "1",
		"io.leapview.local-runtime.checkout": "${LEAPVIEW_LOCAL_CHECKOUT_ID:?canonical checkout identity is required}",
		"io.leapview.local-runtime.owner":    "${LEAPVIEW_LOCAL_OWNER_ID:?local runtime owner identity is required}",
	}
	for key, value := range want {
		if labels[key] != value {
			t.Errorf("%s label %s = %q, want %q", name, key, labels[key], value)
		}
	}
}

func localRuntimeTestEnvironment() []string {
	values := []string{
		"LEAPVIEW_IMAGE=example.invalid/leapview@sha256:" + strings.Repeat("a", 64),
		"LEAPVIEW_LOCAL_APP_PORT=18080",
		"LEAPVIEW_LOCAL_CHECKOUT_ID=sha256:" + strings.Repeat("b", 64),
		"LEAPVIEW_LOCAL_OWNER_ID=owner-test",
		"LEAPVIEW_CSRF_KEY=" + strings.Repeat("c", 32),
		"LEAPVIEW_POSTGRES_BOOTSTRAP_PASSWORD=bootstrap",
	}
	for _, name := range []string{
		"LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD",
		"LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_PASSWORD",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD",
	} {
		values = append(values, name+"=password")
	}
	return values
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsVolume(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneJSONDocument(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	return output
}
