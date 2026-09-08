package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FAI-637 must not accidentally turn the legacy JSON metadata columns or
// authored YAML into the typed semantic-attribute authority. The guard is
// intentionally structural: it leaves those columns available to their
// existing SCIM/access callers while requiring typed state to enter through
// the access-owned baseline schema and repositories.
func TestSemanticAttributeAuthorityIsTypedAndControlPlaneOwned(t *testing.T) {
	root := repoRoot(t)
	baseline := readArchitectureFixture(t, root, "internal/access/postgres/schema.sql")
	for _, declaration := range []string{
		"CREATE TABLE access.principal",
		"CREATE TABLE access.access_group",
		"attributes jsonb NOT NULL",
	} {
		if !strings.Contains(baseline, declaration) {
			t.Fatalf("baseline schema lost legacy metadata declaration %q", declaration)
		}
	}

	typedFiles := []string{
		"internal/access/postgres/queries/semantic_attribute_control.sql",
		"internal/access/postgres/semantic_attributes.go",
		"internal/access/postgres/semantic_attribute_control.go",
		"internal/access/postgres/semantic_attribute_assignments.go",
		"internal/access/postgres/semantic_attribute_claims.go",
	}
	for _, relative := range typedFiles {
		body := readArchitectureFixture(t, root, relative)
		lower := strings.ToLower(body)
		for _, legacy := range []string{"principal.attributes", "access_group.attributes", "attributes jsonb"} {
			if strings.Contains(lower, legacy) {
				t.Errorf("typed semantic authority %s references legacy JSON metadata %q", relative, legacy)
			}
		}
	}

	for relative, tables := range map[string][]string{
		"internal/access/postgres/schema.sql": {
			"access.semantic_attribute_registry",
			"access.semantic_attribute_definition",
			"access.semantic_attribute_control_state",
			"access.semantic_attribute_assignment",
			"access.semantic_attribute_claim_mapping",
		},
	} {
		body := readArchitectureFixture(t, root, relative)
		for _, table := range tables {
			if !strings.Contains(body, table) {
				t.Errorf("typed migration %s does not declare/reference %s", relative, table)
			}
		}
	}

	for _, directory := range []string{"dashboards", "config", "evaluation"} {
		path := filepath.Join(root, directory)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(path, func(filePath string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			extension := strings.ToLower(filepath.Ext(filePath))
			if extension != ".yaml" && extension != ".yml" {
				return nil
			}
			body, readErr := os.ReadFile(filePath)
			if readErr != nil {
				return readErr
			}
			for lineNumber, line := range strings.Split(string(body), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "#") {
					continue
				}
				for _, forbidden := range []string{
					"semanticAttributeDefinitions:",
					"semanticAttributeAssignments:",
					"semanticAttributeClaimMappings:",
					"trustedClaimMappings:",
					"attributeValues:",
				} {
					if strings.HasPrefix(trimmed, forbidden) || strings.HasPrefix(trimmed, "- "+forbidden) {
						relative, relErr := filepath.Rel(root, filePath)
						if relErr != nil {
							return relErr
						}
						t.Errorf("%s:%d authors typed semantic control state with %s", filepath.ToSlash(relative), lineNumber+1, forbidden)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s for typed semantic control YAML: %v", directory, err)
		}
	}
}

func readArchitectureFixture(t *testing.T, root, relative string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(body)
}
