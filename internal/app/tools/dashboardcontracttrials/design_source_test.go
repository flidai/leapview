package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGradeDesignSourceChecksIntentNotOnlyCompiler(t *testing.T) {
	_, manifest := corpusTestPaths(t)
	base := filepath.Dir(manifest)
	for _, tc := range []struct {
		name, source             string
		schema, compiler, intent bool
	}{
		{"oracle", "tasks/monthly-create/oracle", true, true, true},
		{"wrong metric", "tasks/monthly-create/negatives/wrong-metric", true, true, false},
		{"empty seed", "tasks/monthly-create/seed", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gradeDesignSource(manifest, "monthly-create", filepath.Join(base, tc.source))
			if err != nil {
				t.Fatal(err)
			}
			if got.ParseAndSchema != tc.schema || got.Compiler != tc.compiler || got.Intent != tc.intent {
				t.Fatalf("unexpected grade: %#v", got)
			}
		})
	}
	if _, err := gradeDesignSource(manifest, "missing", base); err == nil {
		t.Fatal("unknown task accepted")
	}
}

func TestGradeDesignSourceMalformedAndUnknownFieldAreFailures(t *testing.T) {
	_, manifest := corpusTestPaths(t)
	for _, source := range []string{"{", "apiVersion: leapview.dev/v1\nkind: Dashboard\nunknown: true\n"} {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "dashboards"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "dashboards/evaluation.yaml"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := gradeDesignSource(manifest, "monthly-create", root)
		if err != nil {
			t.Fatal(err)
		}
		if got.ParseAndSchema || got.Compiler || got.Intent || got.Pass || got.Diagnostic == "" {
			t.Fatalf("invalid source accepted: %#v", got)
		}
	}
}
