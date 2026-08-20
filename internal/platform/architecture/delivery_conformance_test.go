package architecture

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPlanDeliveryPhysicalAuthorityGuards keeps the immutable DuckLake
// boundary reviewable in source. It intentionally scans authored production
// code and schema only; qualification tests may mention forbidden statements
// as adversarial inputs without making them reachable.
func TestPlanDeliveryPhysicalAuthorityGuards(t *testing.T) {
	root := repoRoot(t)
	productionRoots := []string{
		"internal/deployment", "internal/app/runtimefactory", "internal/app/composition.go",
		"internal/analytics/candidatecatalog", "internal/analytics/sealedcatalog",
	}
	forbidden := []string{
		"file_membership", "table_membership", "reference_count",
		"CREATE TABLE data_file", "CREATE TABLE delete_file",
		"physical_manifest", "physical publication",
	}
	dangerousNative := []string{"CALL ducklake_cleanup_old_files", "CALL ducklake_delete_orphaned_files", "CHECKPOINT "}
	for _, relative := range productionRoots {
		path := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		visit := func(filePath string, body string) {
			if strings.HasSuffix(filePath, "_test.go") || filepath.Ext(filePath) == ".gen.go" {
				return
			}
			lower := strings.ToLower(body)
			for _, token := range forbidden {
				if strings.Contains(lower, strings.ToLower(token)) {
					t.Errorf("%s retains forbidden SQLite/legacy physical authority token %q", filePath, token)
				}
			}
			if relative == "internal/deployment" || relative == "internal/app/runtimefactory" {
				for _, token := range dangerousNative {
					if strings.Contains(lower, strings.ToLower(token)) {
						t.Errorf("%s reaches native DuckLake maintenance %q from delivery/serving code", filePath, token)
					}
				}
			}
		}
		if info.IsDir() {
			err := filepath.Walk(path, func(filePath string, fileInfo os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if fileInfo != nil && !fileInfo.IsDir() && (strings.HasSuffix(filePath, ".go") || strings.HasSuffix(filePath, ".sql")) {
					body, readErr := os.ReadFile(filePath)
					if readErr != nil {
						return readErr
					}
					visit(filePath, string(body))
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		} else {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			visit(relative, string(body))
		}
	}

	sealedFactory, err := os.ReadFile(filepath.Join(root, "internal/app/runtimefactory/sealed.go"))
	if err != nil {
		t.Fatal(err)
	}
	sealedText := string(sealedFactory)
	for _, required := range []string{"cannot use legacy Prepare", "PrepareSealed", "sealedcatalog.Open"} {
		if !strings.Contains(sealedText, required) {
			t.Errorf("production sealed factory missing fail-closed boundary %q", required)
		}
	}
	composition, err := os.ReadFile(filepath.Join(root, "internal/app/composition.go"))
	if err != nil {
		t.Fatal(err)
	}
	compositionText := string(composition)
	for _, required := range []string{"NewSQLiteSealedFactory", "LegacyServingPathEnabled: false", "ValidateDeliveryStartup"} {
		if !strings.Contains(compositionText, required) {
			t.Errorf("production composition missing sealed delivery/startup gate %q", required)
		}
	}
}

// TestLEA414ProductionUsesSealedCanonicalPath keeps the cutover boundary
// explicit. Legacy runtime factories and candidate preparation remain useful
// fixtures for migration tests, but production composition must always select
// the sealed delivery factory and source-only legacy synchronization cannot
// create a physical candidate around that boundary.
func TestLEA414ProductionUsesSealedCanonicalPath(t *testing.T) {
	root := repoRoot(t)
	compositionBytes, err := os.ReadFile(filepath.Join(root, "internal/app/composition.go"))
	if err != nil {
		t.Fatal(err)
	}
	composition := string(compositionBytes)
	for _, required := range []string{
		"canonicalDeliveryRequired := true",
		"NewSQLiteSealedFactory",
		"LegacyServingPathEnabled: false",
	} {
		if !strings.Contains(composition, required) {
			t.Errorf("production composition missing LEA-414 gate %q", required)
		}
	}
	if !regexp.MustCompile(`RequireCanonicalDelivery:\s+canonicalDeliveryRequired`).MatchString(composition) {
		t.Error("production composition does not require canonical delivery")
	}

	// No non-test production source may construct the snapshot-pinned factory.
	// The legacy constructor is retained only for focused migration fixtures.
	legacyFactoryCalls := []string{}
	for _, relative := range []string{"internal", "cmd"} {
		base := filepath.Join(root, relative)
		if walkErr := filepath.Walk(base, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info == nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(body), "runtimefactory.NewFactory(") {
				legacyFactoryCalls = append(legacyFactoryCalls, path)
			}
			return nil
		}); walkErr != nil {
			t.Fatal(walkErr)
		}
	}
	if len(legacyFactoryCalls) != 0 {
		t.Fatalf("snapshot-pinned runtime factory is reachable from production: %v", legacyFactoryCalls)
	}

	syncBytes, err := os.ReadFile(filepath.Join(root, "internal/deployment/module/candidate_sync.go"))
	if err != nil {
		t.Fatal(err)
	}
	syncSource := string(syncBytes)
	sourceOnly := strings.Index(syncSource, "if request.SourceOnly")
	startCandidate := strings.Index(syncSource, "m.candidates.Start")
	canonicalBuilder := strings.Index(syncSource, "if m.deliveryCandidateBuilder != nil")
	legacyPrepare := strings.Index(syncSource, "m.prepareCandidate(")
	if sourceOnly < 0 || startCandidate < 0 || sourceOnly > startCandidate {
		t.Fatal("legacy candidate synchronization must reject source-only requests before candidate creation")
	}
	if canonicalBuilder < 0 || legacyPrepare < 0 || canonicalBuilder > legacyPrepare {
		t.Fatal("candidate synchronization must route physical preparation through canonical delivery before the compatibility fallback")
	}
}
