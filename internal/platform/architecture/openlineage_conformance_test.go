package architecture

import (
	"strings"
	"testing"
)

const openLineageProjectionPackage = "internal/refresh/openlineage"

var openLineagePublishedContracts = []string{
	"internal/project/contractprojection",
	"internal/project/contractversion",
	"internal/project/contractpublication",
	"internal/release",
	"internal/analytics/catalogstats",
	"internal/analytics/query/planir",
}

// TestOpenLineageProjectionUsesPublishedContracts keeps the refresh adapter on
// narrow, package-scoped contract ports. In particular, adding this projection
// must not turn refresh into a dependency of the project or release
// capabilities as a whole.
func TestOpenLineageProjectionUsesPublishedContracts(t *testing.T) {
	seenImports := map[string]bool{}
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != openLineageProjectionPackage {
			continue
		}
		for _, imported := range file.imports {
			packagePath := strings.TrimPrefix(imported, modulePath+"/")
			for _, contractPath := range openLineagePublishedContracts {
				if packagePath == contractPath {
					seenImports[contractPath] = true
				}
			}
		}
	}
	for _, contractPath := range openLineagePublishedContracts {
		if !seenImports[contractPath] {
			t.Errorf("%s does not directly reuse published contract %s", openLineageProjectionPackage, contractPath)
		}
	}

	for _, targetPath := range openLineagePublishedContracts {
		if !IsSharedContractImport("refresh", targetPath) {
			t.Errorf("refresh -> %s is not an explicitly published shared contract", targetPath)
		}
		source, sourceOK := ClassifyPackage(openLineageProjectionPackage)
		target, targetOK := ClassifyPackage(targetPath)
		if !sourceOK || !targetOK {
			t.Fatalf("classify source=%s (%v) target=%s (%v)", openLineageProjectionPackage, sourceOK, targetPath, targetOK)
		}
		if violation := CapabilityImportViolation(openLineageProjectionPackage, source, targetPath, target); violation != "" {
			t.Errorf("%s -> %s: %s", openLineageProjectionPackage, targetPath, violation)
		}
	}

	for _, capability := range []string{"project", "release"} {
		if CapabilityDependencies["refresh"][capability] {
			t.Errorf("refresh -> %s is a broad capability edge; OpenLineage must use exact shared contracts", capability)
		}
	}

	// Exact package publication must not accidentally make neighboring
	// implementation or persistence packages importable from refresh.
	for _, targetPath := range []string{
		"internal/project/compiler",
		"internal/project/artifact",
		"internal/project/postgres",
		"internal/release/filesystem",
	} {
		source, sourceOK := ClassifyPackage(openLineageProjectionPackage)
		target, targetOK := ClassifyPackage(targetPath)
		if !sourceOK || !targetOK {
			t.Fatalf("classify negative edge source=%s (%v) target=%s (%v)", openLineageProjectionPackage, sourceOK, targetPath, targetOK)
		}
		if violation := CapabilityImportViolation(openLineageProjectionPackage, source, targetPath, target); violation == "" {
			t.Errorf("%s -> %s is broader than the published OpenLineage contract port", openLineageProjectionPackage, targetPath)
		}
	}
}

// TestOpenLineageHasOneProjectionAndNoTransportPath protects the ownership
// boundary: OpenLineage is one refresh-owned projection and an exporter port,
// not a second lineage graph or an embedded collector/transport.
func TestOpenLineageHasOneProjectionAndNoTransportPath(t *testing.T) {
	files := productionGoFiles(t)
	projectionFiles := 0
	projectionImportPaths := map[string]struct{}{}
	for _, file := range files {
		if strings.Contains(file.pkgDir, "openlineage") && file.pkgDir != openLineageProjectionPackage {
			t.Errorf("%s creates a second OpenLineage package outside %s", file.pkgDir, openLineageProjectionPackage)
		}
		if file.pkgDir != openLineageProjectionPackage {
			for _, imported := range file.imports {
				if imported == modulePath+"/"+openLineageProjectionPackage {
					projectionImportPaths[imported] = struct{}{}
				}
				if strings.Contains(imported, "openlineage") && imported != modulePath+"/"+openLineageProjectionPackage {
					t.Errorf("%s imports a second OpenLineage path %s", file.path, imported)
				}
			}
			continue
		}
		projectionFiles++
		for _, imported := range file.imports {
			if strings.HasPrefix(imported, modulePath+"/") {
				packagePath := strings.TrimPrefix(imported, modulePath+"/")
				if strings.Contains(packagePath, "lineage") && packagePath != openLineageProjectionPackage {
					t.Errorf("%s imports a second lineage path %s", file.path, packagePath)
				}
			}
			for _, forbidden := range []string{
				"net/http",
				"github.com/openlineage/",
				"go.opentelemetry.io/",
				"google.golang.org/grpc",
				"github.com/segmentio/kafka-go",
				"github.com/twmb/franz-go",
			} {
				if imported == forbidden || strings.HasPrefix(imported, forbidden) {
					t.Errorf("%s embeds a lineage transport/network dependency %s", file.path, imported)
				}
			}
		}
		for _, forbidden := range []string{
			"http.NewRequest(",
			"http.Client{",
			"http.Post(",
			"grpc.",
			"kafka.",
			"otel.",
		} {
			if strings.Contains(file.body, forbidden) {
				t.Errorf("%s embeds a lineage transport/network operation %q", file.path, forbidden)
			}
		}
	}
	if projectionFiles == 0 {
		t.Fatalf("the refresh-owned OpenLineage projection package %s is missing", openLineageProjectionPackage)
	}
	if len(projectionImportPaths) > 1 {
		t.Fatalf("OpenLineage projection is imported through %d canonical paths", len(projectionImportPaths))
	}
}

// TestOpenLineageReusesExistingIdentityAuthorities allows the UUID-v5 run ID
// derivation but prevents the projection from becoming a second contract
// hashing or canonicalization authority. Contract bytes/digests are supplied
// by contractprojection and immutable publication evidence.
func TestOpenLineageReusesExistingIdentityAuthorities(t *testing.T) {
	canonicalizer := "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != openLineageProjectionPackage {
			continue
		}
		for _, imported := range file.imports {
			for _, forbidden := range []string{
				"crypto/md5",
				"crypto/sha256",
				"crypto/sha512",
				canonicalizer,
			} {
				if imported == forbidden || strings.HasPrefix(imported, forbidden+"/") {
					t.Errorf("%s introduces a second hashing/canonicalization authority through %s", file.path, imported)
				}
			}
		}
	}
}
