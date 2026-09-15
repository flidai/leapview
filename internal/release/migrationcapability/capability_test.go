package migrationcapability

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

func TestCapabilityCanonicalRoundTripAndDigest(t *testing.T) {
	capability := duckLakeFixture()
	document, err := capability.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := capability.Digest()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCanonical(document)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed, mustNormalize(t, capability)) {
		t.Fatalf("parsed capability changed:\n%#v\n%#v", parsed, capability)
	}
	reordered := capability
	reordered.DuckLake = cloneDuckLake(capability.DuckLake)
	reordered.DuckLake.RunnableCatalogSchemaVersions = []string{"ducklake-catalog/v2", "ducklake-catalog/v1"}
	reorderedDocument, err := reordered.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	reorderedDigest, err := reordered.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(document, reorderedDocument) || digest != reorderedDigest {
		t.Fatalf("equivalent ordering changed identity:\n%s\n%s\n%s/%s", document, reorderedDocument, digest, reorderedDigest)
	}

	mutated := capability
	mutated.DuckLake = cloneDuckLake(capability.DuckLake)
	mutated.DuckLake.CatalogSchemaVersion = "ducklake-catalog/v2"
	mutatedDigest, err := mutated.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if mutatedDigest == digest {
		t.Fatal("content mutation did not change capability digest")
	}
}

func TestCapabilityGoldenVector(t *testing.T) {
	document, err := duckLakeFixture().CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := duckLakeFixture().Digest()
	if err != nil {
		t.Fatal(err)
	}
	wantDocument, err := os.ReadFile(filepath.Join("testdata", "ducklake-v1.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := os.ReadFile(filepath.Join("testdata", "ducklake-v1.golden.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	wantDocument = bytes.TrimSuffix(wantDocument, []byte("\n"))
	if !bytes.Equal(document, wantDocument) || digest != strings.TrimSpace(string(wantDigest)) {
		t.Fatalf("migration-capability/v1 golden mismatch:\ncanonical=%s\ndigest=%s", document, digest)
	}
}

func TestCapabilityRejectsInvalidBindingsAndPayloads(t *testing.T) {
	base := duckLakeFixture()
	for name, mutate := range map[string]func(*Capability){
		"unsupported version":      func(value *Capability) { value.Version = "migration-capability/v2" },
		"mutable artifact":         func(value *Capability) { value.ArtifactAdmissionDigest = "ghcr.io/flidai/leapview:latest" },
		"wrong target":             func(value *Capability) { value.TargetIdentityDigest = "target-prod" },
		"missing owner":            func(value *Capability) { value.Owner = Owner{} },
		"wrong owner":              func(value *Capability) { value.Owner.Identity = GooseOwnerIdentity },
		"missing candidate schema": func(value *Capability) { value.DuckLake.CatalogSchemaVersion = "" },
		"missing current schema membership": func(value *Capability) {
			value.DuckLake.RunnableCatalogSchemaVersions = []string{"ducklake-catalog/v2"}
		},
		"duplicate schema": func(value *Capability) {
			value.DuckLake.RunnableCatalogSchemaVersions = []string{"ducklake-catalog/v1", "ducklake-catalog/v1"}
		},
		"multiple payloads": func(value *Capability) {
			value.Goose = &GooseCapability{SchemaVersion: "goose/v16", RunnableSchemaVersions: []string{"goose/v16"}, MigrationGraphDigest: testDigest("6")}
		},
		"payload mismatch":     func(value *Capability) { value.Subsystem = SubsystemGoose },
		"invalid graph digest": func(value *Capability) { value.DuckLake.MigrationGraphDigest = "sha256:nope" },
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			value.DuckLake = cloneDuckLake(base.DuckLake)
			mutate(&value)
			if _, err := value.CanonicalJSON(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("CanonicalJSON error = %v, want invalid", err)
			}
		})
	}
}

func TestCapabilityRejectsNonCanonicalBytes(t *testing.T) {
	capability := duckLakeFixture()
	capability.DuckLake = cloneDuckLake(capability.DuckLake)
	capability.DuckLake.RunnableCatalogSchemaVersions = []string{"ducklake-catalog/v2", "ducklake-catalog/v1"}
	document := []byte(`{"version":"migration-capability/v1","artifactAdmissionDigest":"` + testDigest("a") + `","targetIdentityDigest":"` + testDigest("b") + `","subsystem":"ducklake","owner":{"identity":"leapview.ducklake.catalog","contractVersion":"ducklake-owner-capability/v1"},"goose":null,"riverJobs":null,"duckLake":{"compatibility":{"duckdb_runtime":"duckdb:1.5.4","ducklake_extension":"ducklake:0.3.0","catalog_format":"ducklake-catalog:v1","storage_implementation":"s3","object_naming_contract":"uuidv7:v1"},"catalogSchemaVersion":"ducklake-catalog/v1","runnableCatalogSchemaVersions":["ducklake-catalog/v2","ducklake-catalog/v1"],"migrationGraphDigest":"` + testDigest("c") + `"},"physicalPool":null}`)
	if _, err := ParseCanonical(document); !errors.Is(err, ErrNonCanonical) {
		t.Fatalf("ParseCanonical error = %v, want non-canonical", err)
	}
	canonical, err := capability.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCanonical(append(canonical, []byte(" trailing")...)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("trailing data error = %v, want invalid", err)
	}
}

func duckLakeFixture() Capability {
	return Capability{
		Version:                 Version,
		ArtifactAdmissionDigest: testDigest("a"),
		TargetIdentityDigest:    testDigest("b"),
		Subsystem:               SubsystemDuckLake,
		Owner:                   Owner{Identity: DuckLakeOwnerIdentity, ContractVersion: DuckLakeOwnerContractVersion},
		DuckLake: &DuckLakeCapability{
			Compatibility: physicalpool.Compatibility{
				DuckDBRuntime: "duckdb:1.5.4", DuckLakeExtension: "ducklake:0.3.0", CatalogFormat: "ducklake-catalog:v1",
				StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1",
			},
			CatalogSchemaVersion:          "ducklake-catalog/v1",
			RunnableCatalogSchemaVersions: []string{"ducklake-catalog/v1", "ducklake-catalog/v2"},
			MigrationGraphDigest:          testDigest("c"),
		},
	}
}

func cloneDuckLake(value *DuckLakeCapability) *DuckLakeCapability {
	copyValue := *value
	copyValue.RunnableCatalogSchemaVersions = append([]string(nil), value.RunnableCatalogSchemaVersions...)
	return &copyValue
}

func mustNormalize(t *testing.T, value Capability) Capability {
	t.Helper()
	normalized, err := value.normalized()
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func testDigest(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }
