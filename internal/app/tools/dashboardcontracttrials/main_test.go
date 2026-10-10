package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	compiler "github.com/flidai/leapview/internal/project/compiler"
)

// The scoring oracle must reject semantically valid wrong-target changes, not
// award a pass merely because the edited dashboard still compiles.
func TestIntentOraclePreservesUnrelatedVisualsAndPageOrder(t *testing.T) {
	expected, err := compiler.LoadDashboardDocument(filepath.Join("..", "..", "..", "..", "playground", "dashboard-evaluation", "oracle", "reuse.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonical(expected)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"wrong visual", "page reorder"} {
		t.Run(scenario, func(t *testing.T) {
			actual, err := expected.Clone()
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "wrong visual" {
				visual := actual.Spec.Visuals["unrelated-trend"]
				title := "Revenue across months"
				visual.Title = &title
				actual.Spec.Visuals["unrelated-trend"] = visual
			} else {
				actual.Spec.Pages[0], actual.Spec.Pages[1] = actual.Spec.Pages[1], actual.Spec.Pages[0]
			}
			got, err := canonical(actual)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(got, want) {
				t.Fatal("oracle accepted unrelated semantic change")
			}
		})
	}
}

func TestIntentOracleTreatsOmittedDefaultsAndDefinitionOrderAsEquivalent(t *testing.T) {
	expected, err := compiler.LoadDashboardDocument(filepath.Join("..", "..", "..", "..", "playground", "dashboard-evaluation", "oracle", "reuse.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := expected.Clone()
	if err != nil {
		t.Fatal(err)
	}
	actual.Spec.Layout = nil
	definitions := make(map[string]document.DashboardVisual, len(actual.Spec.Visuals))
	for _, id := range []string{"revenue-by-month", "total-revenue", "unrelated-trend"} {
		definitions[id] = actual.Spec.Visuals[id]
	}
	actual.Spec.Visuals = definitions
	got, err := canonical(actual)
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonical(expected)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("equivalent defaults/definition order rejected")
	}
	// A real explicit nondefault value must not be normalized away.
	actual.Spec.Layout = &document.DashboardLayoutDefaults{Columns: 12, RowHeight: 48, Gap: 8, Padding: 16}
	got, err = canonical(actual)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got, want) {
		t.Fatal("nondefault layout difference hidden by normalization")
	}
}

func TestFrozenInputsRejectChangedOrExtraSources(t *testing.T) {
	base := t.TempDir()
	source := "runs/trial/dashboards/evaluation.yaml"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(base, source)), 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, value []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(base, name), value, 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(source, []byte("seed"))
	write("oracle.yaml", []byte("oracle"))
	hash := func(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }
	data, _ := json.Marshal(map[string]any{"pretrial": map[string]string{"oracle.yaml": hash("oracle")}})
	write("freeze.json", data)
	data, _ = json.Marshal(map[string]any{"firstAttemptSourceHashes": map[string]string{source: hash("seed")}})
	write("submissions.json", data)
	if err := verifyRecordedInputs(base); err != nil {
		t.Fatal(err)
	}
	write(source, []byte("repair"))
	if verifyRecordedInputs(base) == nil {
		t.Fatal("changed first attempt accepted")
	}
	write(source, []byte("seed"))
	extra := "runs/trial/dashboards/extra.yml"
	write(extra, []byte("extra"))
	if verifyRecordedInputs(base) == nil {
		t.Fatal("unrecorded yml source accepted")
	}
	os.Remove(filepath.Join(base, extra))
	write("oracle.yaml", []byte("changed"))
	if verifyRecordedInputs(base) == nil {
		t.Fatal("changed oracle accepted")
	}
}
