package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	configschema "github.com/flidai/leapview/internal/project/schema"
)

func TestCompilerEvidenceCorpus(t *testing.T) {
	repo := "../../../.."
	fixtures, err := compileCorpus(repo)
	if err != nil {
		t.Fatal(err)
	}
	// Normal Go CI verifies the UI's recorded source/resource/intent data
	// against the same freshly computed corpus. Host build and Git provenance
	// are deliberately checked separately by the explicit CLI verifier.
	recordedBytes, err := os.ReadFile(filepath.Join(repo, "playground/dashboard-contract-evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded EvidenceDocument
	if err := json.Unmarshal(recordedBytes, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.FormatVersion != 1 || !recorded.CompileOnly {
		t.Fatal("recorded compiler evidence has an unsupported format or execution claim")
	}
	recordedFixtures, err := json.Marshal(recorded.Fixtures)
	if err != nil {
		t.Fatal(err)
	}
	currentFixtures, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	if string(recordedFixtures) != string(currentFixtures) {
		t.Fatal("recorded Playground compiler fixtures are stale; review changes and run go run -tags duckdb_arrow ./internal/app/tools/dashboardcontractevidence -update")
	}
	byID := map[string]FixtureEvidence{}
	for _, fixture := range fixtures {
		byID[fixture.ID] = fixture
		if fixture.SourceDigest != digest([]byte(fixture.Source)) {
			t.Fatalf("%s source digest mismatch", fixture.ID)
		}
	}
	for id, message := range map[string]string{"original-guide": "purchase_month", "zero-span": "minimum", "out-of-grid": "exceed grid", "overlap": "overlap", "missing-visual": "missing-visual", "duplicate-visual-id": "duplicate"} {
		fixture := byID[id]
		if fixture.Valid || len(fixture.Issues) == 0 || !strings.Contains(strings.ToLower(fixture.Issues[0].Message), message) {
			t.Errorf("%s = %#v, want rejected with %q", id, fixture.Issues, message)
		}
		for _, issue := range fixture.Issues {
			if strings.Contains(issue.Message, "contract-evidence-") || strings.Contains(issue.File, "contract-evidence-") {
				t.Errorf("%s contains temporary-root diagnostics", id)
			}
		}
	}
	corrected := byID["corrected-monthly"]
	if !corrected.Valid || corrected.ResolvedIntent == nil {
		t.Fatalf("corrected fixture invalid: %#v", corrected.Issues)
	}
	var trend VisualIntent
	for _, visual := range corrected.ResolvedIntent.Visuals {
		if visual.ID == "revenue-by-month" {
			trend = visual
		}
	}
	if trend.Query.Aggregate == nil {
		t.Fatal("trend missing aggregate binding")
	}
	q := trend.Query.Aggregate
	if len(q.Dimensions) != 1 || q.Dimensions[0].FieldID != "purchase_date" || q.Dimensions[0].Grain != "month" || q.Dimensions[0].Alias != "purchase_month" || q.Limit != 30 || len(q.Sort) != 1 || q.Sort[0].FieldID != "purchase_month" || q.Sort[0].Direction != "asc" {
		t.Fatalf("unexpected compiler trend binding: %#v", q)
	}
	for _, id := range []string{"omitted-defaults", "explicit-defaults", "reordered-definitions", "confined-fragments"} {
		fixture := byID[id]
		if !fixture.Valid || !reflect.DeepEqual(fixture.ResolvedIntent, corrected.ResolvedIntent) || fixture.ResolvedIntentDigest != corrected.ResolvedIntentDigest {
			t.Errorf("%s intent differs or invalid: %#v", id, fixture)
		}
	}
	if reflect.DeepEqual(byID["confined-fragments"].SourceFiles, corrected.SourceFiles) || len(byID["confined-fragments"].FragmentPaths) != 2 {
		t.Fatal("fragment provenance missing")
	}
}

func TestCorpusSourcesAndDigestsAreDeterministic(t *testing.T) {
	first, err := compileCorpus("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	second, err := compileCorpus("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		for i := range first {
			if !reflect.DeepEqual(first[i], second[i]) {
				t.Fatalf("fixture %s differs: bundle %s / %s; intent %s / %s; issues %#v / %#v", first[i].ID, first[i].BundleDigest, second[i].BundleDigest, first[i].ResolvedIntentDigest, second[i].ResolvedIntentDigest, first[i].Issues, second[i].Issues)
			}
		}
	}
}

func TestDiagnosticNormalizationPreservesHierarchyAndEveryCause(t *testing.T) {
	first := "validation failed\n- at '': root failed\n  - at '/b': object failed\n    - at '/b/z': false schema\n    - at '/b/a': minimum\n  - at '/a': missing property"
	second := "validation failed\n- at '': root failed\n  - at '/a': missing property\n  - at '/b': object failed\n    - at '/b/a': minimum\n    - at '/b/z': false schema"
	if stableDiagnosticMessage(first) != second || stableDiagnosticMessage(second) != second {
		t.Fatal("normalization lost or changed a diagnostic subtree")
	}
}

func TestEvidenceVerificationIgnoresGitProvenanceButRejectsInputAndEnvironmentDrift(t *testing.T) {
	recorded := EvidenceDocument{FormatVersion: 1, CompileOnly: true, Metadata: Metadata{CompilerCommit: "before", CompilerSourceDigest: "source", CompilerFingerprint: "build", TrackedSourceCount: 1, UntrackedSourcePaths: []string{"new.go"}, Build: BuildFingerprint{GoVersion: "go1.27.1", GOOS: "linux", GOARCH: "amd64", Tags: "duckdb_arrow"}}}
	staged := recorded
	staged.Metadata.CompilerCommit = "after"
	staged.Metadata.TrackedSourceCount = 2
	staged.Metadata.UntrackedSourcePaths = nil
	staged.Metadata.ModifiedSourcePaths = []string{"existing.go"}
	if err := compareEvidence(recorded, staged); err != nil {
		t.Fatalf("staging/commit invalidated unchanged inputs: %v", err)
	}
	changed := staged
	changed.Metadata.CompilerSourceDigest = "changed-source"
	if err := compareEvidence(recorded, changed); err == nil || !strings.Contains(err.Error(), "inputs or outcomes changed") {
		t.Fatalf("changed source accepted: %v", err)
	}
	platform := staged
	platform.Metadata.Build.GOOS = "darwin"
	if err := compareEvidence(recorded, platform); err == nil || !strings.Contains(err.Error(), "environment mismatch") {
		t.Fatalf("platform difference mislabeled or accepted: %v", err)
	}
	// JSON omission treats nil/empty optional fragment paths identically, while
	// mandatory issues arrays and all exact source bytes remain authoritative.
	recorded.Fixtures = []FixtureEvidence{{ID: "valid", Issues: []configschema.Diagnostic{}}}
	staged.Fixtures = []FixtureEvidence{{ID: "valid", FragmentPaths: []string{}, Issues: []configschema.Diagnostic{}}}
	if err := compareEvidence(recorded, staged); err != nil {
		t.Fatal(err)
	}
	staged.Fixtures[0].Source = "edited"
	if err := compareEvidence(recorded, staged); err == nil {
		t.Fatal("edited exact-source bytes accepted")
	}
}
