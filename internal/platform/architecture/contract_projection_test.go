package architecture

import (
	"strings"
	"testing"
)

func TestContractCanonicalizationIsIsolatedFromExistingArtifactIdentity(t *testing.T) {
	const (
		canonicalizer = "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
		projection    = modulePath + "/internal/project/contractprojection"
	)
	for _, file := range productionGoFiles(t) {
		if importListContains(file.imports, canonicalizer) && file.path != "internal/project/contractprojection/canonical.go" {
			t.Errorf("%s imports RFC 8785 outside the sealed contract boundary", file.path)
		}
		if file.pkgDir == "internal/project/artifact" || file.pkgDir == "internal/project/graph" || file.pkgDir == "internal/release" {
			if importListContains(file.imports, projection) || importListContains(file.imports, canonicalizer) {
				t.Errorf("%s changes an existing graph/artifact/release identity boundary", file.path)
			}
		}
	}
}

func TestContractProjectionDoesNotOwnPublicationOrRuntimeAuthority(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/project/contractprojection" {
			continue
		}
		for _, imported := range file.imports {
			for _, forbidden := range []string{
				"database/sql", "github.com/jackc/pgx", modulePath + "/internal/deployment",
				modulePath + "/internal/runtimehost", modulePath + "/internal/access",
				modulePath + "/internal/project/identityledger", modulePath + "/internal/project/contractversion",
				modulePath + "/internal/analytics/query", modulePath + "/internal/analytics/materialize",
			} {
				if imported == forbidden || strings.HasPrefix(imported, forbidden+"/") {
					t.Errorf("%s imports authority outside the projection boundary: %s", file.path, imported)
				}
			}
		}
	}
}

func TestContractProjectionUsesSharedSemanticValueContract(t *testing.T) {
	source, sourceOK := ClassifyPackage("internal/project/contractprojection")
	target, targetOK := ClassifyPackage("internal/semanticvalue")
	if !sourceOK || !targetOK {
		t.Fatal("projection or semantic value capability is unclassified")
	}
	if violation := CapabilityImportViolation("internal/project/contractprojection", source, "internal/semanticvalue", target); violation != "" {
		t.Fatalf("projection must reuse the shared canonical value contract: %s", violation)
	}
}
