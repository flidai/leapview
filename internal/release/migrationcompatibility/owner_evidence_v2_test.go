package migrationcompatibility

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

func TestEvidenceV2CanonicalBindingAndGoldenDigest(t *testing.T) {
	evidence := ownerEvidenceV2Fixture(t)
	canonical, err := evidence.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical evidence: %v", err)
	}
	goldenBytes, err := os.ReadFile("testdata/migration-compatibility-v2.canonical.json")
	if err != nil {
		t.Fatalf("read canonical compatibility vector: %v", err)
	}
	goldenBytes = bytes.TrimSuffix(goldenBytes, []byte("\n"))
	if !bytes.Equal(canonical, goldenBytes) {
		t.Fatalf("canonical compatibility vector changed\ngot:  %s\nwant: %s", canonical, goldenBytes)
	}
	parsed, err := ParseEvidenceV2(canonical)
	if err != nil {
		t.Fatalf("parse canonical evidence: %v", err)
	}
	reencoded, err := parsed.CanonicalJSON()
	if err != nil {
		t.Fatalf("reencode evidence: %v", err)
	}
	if !bytes.Equal(canonical, reencoded) {
		t.Fatal("canonical evidence changed after parse")
	}
	digest, err := evidence.Digest()
	if err != nil {
		t.Fatalf("digest evidence: %v", err)
	}
	const golden = "sha256:42a030e78d4c8cb0b79862d979ef5b63b24e0d95f1df742293e337dbfae0cb05"
	if digest != golden {
		t.Fatalf("compatibility digest changed: got %s want %s\ncanonical=%s", digest, golden, canonical)
	}
	if parsed.Binding != evidence.Binding || parsed.Goose.Binding != evidence.Binding || parsed.RiverJobs.Binding != evidence.Binding || parsed.DuckLake.Binding != evidence.Binding || parsed.PhysicalPool.Binding != evidence.Binding {
		t.Fatal("owner evidence did not retain the exact transition binding")
	}
}

func TestEvidenceV2IsDeterministic(t *testing.T) {
	first := ownerEvidenceV2Fixture(t)
	second := ownerEvidenceV2Fixture(t)
	firstBytes, err := first.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := second.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, _ := first.Digest()
	secondDigest, _ := second.Digest()
	if !bytes.Equal(firstBytes, secondBytes) || firstDigest != secondDigest {
		t.Fatal("equivalent owner evidence was not deterministic")
	}
}

func TestEvidenceV2RejectsWrongBindingsAndMissingOwners(t *testing.T) {
	tests := map[string]struct {
		mutate func(*EvidenceV2)
		want   error
	}{
		"wrong predecessor": {func(value *EvidenceV2) {
			value.Goose.Binding.PredecessorOCIAdmissionDigest = digestV2('9')
			refreshGooseDigestV2(t, &value.Goose)
		}, ErrOwnerEvidenceV2Invalid},
		"wrong candidate": {func(value *EvidenceV2) {
			value.RiverJobs.Binding.CandidateOCIAdmissionDigest = digestV2('9')
			refreshRiverJobsDigestV2(t, &value.RiverJobs)
		}, ErrOwnerEvidenceV2Invalid},
		"wrong target": {func(value *EvidenceV2) {
			value.DuckLake.Binding.TargetIdentityDigest = digestV2('9')
			refreshDuckLakeDigestV2(t, &value.DuckLake)
		}, ErrOwnerEvidenceV2Invalid},
		"missing Goose":    {func(value *EvidenceV2) { value.Goose = GooseOwnerEvidenceV2{} }, ErrOwnerEvidenceV2Missing},
		"missing River":    {func(value *EvidenceV2) { value.RiverJobs = RiverJobsOwnerEvidenceV2{} }, ErrOwnerEvidenceV2Missing},
		"missing DuckLake": {func(value *EvidenceV2) { value.DuckLake = DuckLakeOwnerEvidenceV2{} }, ErrOwnerEvidenceV2Missing},
		"missing pool":     {func(value *EvidenceV2) { value.PhysicalPool = PhysicalPoolOwnerEvidenceV2{} }, ErrOwnerEvidenceV2Missing},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := ownerEvidenceV2Fixture(t)
			mutate.mutate(&value)
			if _, err := value.CanonicalJSON(); !errors.Is(err, mutate.want) {
				t.Fatalf("error = %v, want %v", err, mutate.want)
			}
		})
	}
}

func TestEvidenceV2RejectsStaleConflictingAndCallerOnlyEvidence(t *testing.T) {
	t.Run("stale owner contract", func(t *testing.T) {
		value := ownerEvidenceV2Fixture(t)
		value.Goose.Owner.ContractVersion = "goose-owner-evidence/v0"
		if _, err := value.CanonicalJSON(); !errors.Is(err, ErrOwnerContractV2Unsupported) {
			t.Fatalf("error = %v, want unsupported owner contract", err)
		}
	})

	t.Run("conflicting subsystem tuple", func(t *testing.T) {
		value := ownerEvidenceV2Fixture(t)
		value.PhysicalPool.State.Candidate.ObjectNamingContract = "uuidv7:v2"
		refreshPhysicalPoolDigestV2(t, &value.PhysicalPool)
		if _, err := value.CanonicalJSON(); !errors.Is(err, ErrOwnerEvidenceV2Conflict) {
			t.Fatalf("error = %v, want owner conflict", err)
		}
	})

	t.Run("caller-only binding", func(t *testing.T) {
		if _, err := NewEvidenceV2(GooseOwnerEvidenceV2{}, RiverJobsOwnerEvidenceV2{}, DuckLakeOwnerEvidenceV2{}, PhysicalPoolOwnerEvidenceV2{}); !errors.Is(err, ErrOwnerEvidenceV2Missing) {
			t.Fatalf("error = %v, want missing owner evidence", err)
		}
	})
}

func TestEvidenceV2RejectsInvalidOwnerConstruction(t *testing.T) {
	tests := map[string]BindingV2{
		"missing predecessor": {CandidateOCIAdmissionDigest: digestV2('b'), TargetIdentityDigest: digestV2('c')},
		"mutable predecessor": {PredecessorOCIAdmissionDigest: "registry.example/leapview:latest", CandidateOCIAdmissionDigest: digestV2('b'), TargetIdentityDigest: digestV2('c')},
		"missing target":      {PredecessorOCIAdmissionDigest: digestV2('a'), CandidateOCIAdmissionDigest: digestV2('b')},
	}
	for name, binding := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewGooseOwnerEvidenceV2(binding, transitionpreflight.CompatibilityBackwardCompatible, GooseStateV2{PredecessorSchemaVersion: "goose/v14", CandidateSchemaVersion: "goose/v15"}); !errors.Is(err, ErrOwnerEvidenceV2Invalid) && !errors.Is(err, ErrEvidenceV2Invalid) {
				t.Fatalf("error = %v, want invalid owner evidence", err)
			}
		})
	}
}

func TestEvidenceV2DigestChangesWithOwnerState(t *testing.T) {
	first := ownerEvidenceV2Fixture(t)
	second := ownerEvidenceV2Fixture(t)
	goose, err := NewGooseOwnerEvidenceV2(second.Binding, transitionpreflight.CompatibilityBackwardCompatible, GooseStateV2{PredecessorSchemaVersion: "goose/v14", CandidateSchemaVersion: "goose/v16"})
	if err != nil {
		t.Fatal(err)
	}
	second, err = NewEvidenceV2(goose, second.RiverJobs, second.DuckLake, second.PhysicalPool)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, _ := first.Digest()
	secondDigest, _ := second.Digest()
	if firstDigest == secondDigest {
		t.Fatal("owner state mutation did not change the aggregate digest")
	}
}

func TestEvidenceV2RejectsTamperingAndNonCanonicalBytes(t *testing.T) {
	value := ownerEvidenceV2Fixture(t)
	canonical, err := value.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(canonical, []byte("goose/v15"), []byte("goose/v16"), 1)
	if _, err := ParseEvidenceV2(tampered); !errors.Is(err, ErrOwnerEvidenceV2DigestMismatch) {
		t.Fatalf("tamper error = %v, want digest mismatch", err)
	}
	spaced := append([]byte(" "), canonical...)
	if _, err := ParseEvidenceV2(spaced); !errors.Is(err, ErrOwnerEvidenceV2NonCanonical) {
		t.Fatalf("non-canonical error = %v", err)
	}
}

func ownerEvidenceV2Fixture(t *testing.T) EvidenceV2 {
	t.Helper()
	binding := bindingV2Fixture()
	predecessor := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1.5.4", DuckLakeExtension: "ducklake:0.3", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	candidate := predecessor
	goose, err := NewGooseOwnerEvidenceV2(binding, transitionpreflight.CompatibilityBackwardCompatible, GooseStateV2{PredecessorSchemaVersion: "goose/v14", CandidateSchemaVersion: "goose/v15"})
	if err != nil {
		t.Fatal(err)
	}
	river, err := NewRiverJobsOwnerEvidenceV2(binding, transitionpreflight.CompatibilityBackwardCompatible, RiverJobsStateV2{PredecessorSchemaVersion: "river/v1", CandidateSchemaVersion: "river/v1", PredecessorJobHistoryVersion: "jobs/v1", CandidateJobHistoryVersion: "jobs/v1"})
	if err != nil {
		t.Fatal(err)
	}
	duckLake, err := NewDuckLakeOwnerEvidenceV2(binding, transitionpreflight.CompatibilityBackwardCompatible, DuckLakeStateV2{Predecessor: predecessor, Candidate: candidate, PredecessorCatalogSchemaVersion: "catalog/v1", CandidateCatalogSchemaVersion: "catalog/v1"})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := NewPhysicalPoolOwnerEvidenceV2(binding, transitionpreflight.CompatibilityBackwardCompatible, PhysicalPoolStateV2{Predecessor: predecessor, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := NewEvidenceV2(goose, river, duckLake, pool)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func bindingV2Fixture() BindingV2 {
	return BindingV2{PredecessorOCIAdmissionDigest: digestV2('a'), CandidateOCIAdmissionDigest: digestV2('b'), TargetIdentityDigest: digestV2('c')}
}

func refreshPhysicalPoolDigestV2(t *testing.T, evidence *PhysicalPoolOwnerEvidenceV2) {
	t.Helper()
	digest, err := evidence.payloadDigest()
	if err != nil {
		t.Fatal(err)
	}
	evidence.EvidenceDigest = digest
}

func refreshGooseDigestV2(t *testing.T, evidence *GooseOwnerEvidenceV2) {
	t.Helper()
	digest, err := evidence.payloadDigest()
	if err != nil {
		t.Fatal(err)
	}
	evidence.EvidenceDigest = digest
}

func refreshRiverJobsDigestV2(t *testing.T, evidence *RiverJobsOwnerEvidenceV2) {
	t.Helper()
	digest, err := evidence.payloadDigest()
	if err != nil {
		t.Fatal(err)
	}
	evidence.EvidenceDigest = digest
}

func refreshDuckLakeDigestV2(t *testing.T, evidence *DuckLakeOwnerEvidenceV2) {
	t.Helper()
	digest, err := evidence.payloadDigest()
	if err != nil {
		t.Fatal(err)
	}
	evidence.EvidenceDigest = digest
}

func digestV2(value byte) string { return "sha256:" + strings.Repeat(string(value), 64) }
