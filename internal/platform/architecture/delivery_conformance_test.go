package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlanDeliveryPhysicalAuthorityGuards keeps the immutable DuckLake
// boundary reviewable in source. It intentionally scans authored production
// code and schema only; qualification tests may mention forbidden statements
// as adversarial inputs without making them reachable.
func TestPlanDeliveryPhysicalAuthorityGuards(t *testing.T) {
	root := repoRoot(t)
	// build.go dispatches production to postgres_build.go. The legacy
	// composition.go path is intentionally excluded: it remains the explicit
	// SQLite development/evaluation fixture used by local tests.
	productionRoots := []string{
		"internal/deployment", "internal/app/runtimefactory", "internal/app/build.go", "internal/app/postgres_build.go",
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
	productionBuild, err := os.ReadFile(filepath.Join(root, "internal/app/build.go"))
	if err != nil {
		t.Fatal(err)
	}
	productionBuildText := string(productionBuild)
	for _, required := range []string{"BuildProduction", "buildPostgresProductionTarget"} {
		if !strings.Contains(productionBuildText, required) {
			t.Errorf("production entrypoint missing PostgreSQL delivery gate %q", required)
		}
	}
	if strings.Contains(productionBuildText, "NewSQLiteSealedFactory") {
		t.Error("production entrypoint retains the legacy NewSQLiteSealedFactory")
	}

	postgresBuild, err := os.ReadFile(filepath.Join(root, "internal/app/postgres_build.go"))
	if err != nil {
		t.Fatal(err)
	}
	postgresBuildText := string(postgresBuild)
	for _, required := range []string{"NewPostgresSealedFactory", "RequireSealedCatalog: true", "ResolveSealedActiveState"} {
		if !strings.Contains(postgresBuildText, required) {
			t.Errorf("PostgreSQL production composition missing sealed delivery/startup gate %q", required)
		}
	}
	if strings.Contains(postgresBuildText, "NewSQLiteSealedFactory") {
		t.Error("PostgreSQL production composition retains the legacy NewSQLiteSealedFactory")
	}
}

// TestLEA414ProductionUsesSealedCanonicalPath keeps the cutover boundary
// explicit. Production composition must select the target-owned PostgreSQL
// sealed delivery factory; the SQLite adapter remains outside this path for
// development/evaluation fixtures only.
func TestLEA414ProductionUsesSealedCanonicalPath(t *testing.T) {
	root := repoRoot(t)
	productionBuildBytes, err := os.ReadFile(filepath.Join(root, "internal/app/build.go"))
	if err != nil {
		t.Fatal(err)
	}
	productionBuild := string(productionBuildBytes)
	for _, required := range []string{"BuildProduction", "buildPostgresProductionTarget"} {
		if !strings.Contains(productionBuild, required) {
			t.Errorf("production entrypoint missing FAI-575 gate %q", required)
		}
	}
	if strings.Contains(productionBuild, "NewSQLiteSealedFactory") {
		t.Error("production entrypoint selects the legacy NewSQLiteSealedFactory")
	}

	postgresBuildBytes, err := os.ReadFile(filepath.Join(root, "internal/app/postgres_build.go"))
	if err != nil {
		t.Fatal(err)
	}
	postgresBuild := string(postgresBuildBytes)
	normalizedPostgresBuild := strings.Join(strings.Fields(postgresBuild), " ")
	for _, required := range []string{"NewPostgresSealedFactory", "RequireSealedCatalog: true", "ResolveSealedActiveState", "NativeDeliveryMutations: nativeDelivery", "NativeDeliveryReader:    nativeDeliveryReader"} {
		if !strings.Contains(normalizedPostgresBuild, strings.Join(strings.Fields(required), " ")) {
			t.Errorf("native PostgreSQL composition missing FAI-575 target contract %q", required)
		}
	}
	if strings.Contains(postgresBuild, "NewSQLiteSealedFactory") {
		t.Error("native PostgreSQL composition selects the legacy NewSQLiteSealedFactory")
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
