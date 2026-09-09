package architecture

import (
	"strings"
	"testing"
)

func TestContractVersionAndPublicationAuthorityStayLayered(t *testing.T) {
	const (
		canonicalizer = "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
		projection    = "internal/project/contractprojection"
		classifier    = "internal/project/contractversion"
		publication   = "internal/project/contractpublication"
	)
	for _, file := range productionGoFiles(t) {
		switch file.pkgDir {
		case classifier:
			for _, forbidden := range []string{
				"crypto/sha256", canonicalizer,
				"database/sql", "github.com/jackc/pgx", modulePath + "/internal/access",
				modulePath + "/internal/deployment", modulePath + "/internal/runtimehost",
				modulePath + "/internal/project/identityledger",
			} {
				if importListContains(file.imports, forbidden) {
					t.Errorf("%s imports authority outside the pure classifier boundary: %s", file.path, forbidden)
				}
			}
			if file.path == classifier+"/classifier.go" && !importListContains(file.imports, modulePath+"/"+projection) {
				t.Errorf("%s must validate inputs through the sealed FAI-620 projection boundary", file.path)
			}
		case publication:
			for _, imported := range file.imports {
				for _, forbidden := range []string{
					"database/sql", "github.com/jackc/pgx", canonicalizer,
					modulePath + "/internal/access", modulePath + "/internal/deployment",
					modulePath + "/internal/runtimehost", modulePath + "/internal/analytics",
					modulePath + "/internal/project/identityledger",
				} {
					if imported == forbidden || strings.HasPrefix(imported, forbidden+"/") {
						t.Errorf("%s imports persistence, runtime, or consumer authority: %s", file.path, imported)
					}
				}
			}
		}
	}
}
