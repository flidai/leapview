package architecture

import (
	"strings"
	"testing"
)

func TestSemanticConsumersUseOneDiscoveryAndPlannerBoundary(t *testing.T) {
	root := repoRoot(t)
	for _, check := range []struct {
		path     string
		required []string
	}{
		{"internal/dashboard/queryauthz/semantic_discovery.go", []string{"SemanticAccessResolutionSnapshot(", "NewSemanticAccessConsumer(", "snapshot.ValidateBound()", "consumer.Authorize("}},
		{"internal/project/module/semantic_catalog.go", []string{"lease.Identity()", "compiled.MatchesModel(model)", "NewSemanticAccessDiscovery("}},
		{"internal/analytics/materialize/semantic_consumer.go", []string{"SemanticAccessConsumerContextFromContext(", "consumer.ValidatePlan("}},
		{"internal/access/postgres/semantic_attribute_resolution.go", []string{"BeginTx(ctx, pgx.TxOptions{", "IsoLevel: pgx.RepeatableRead", "AccessMode: pgx.ReadOnly", "requireLiveSemanticAttributePrincipal(", "ListPrincipalSemanticAttributeGroups("}},
	} {
		body := readArchitectureFixture(t, root, check.path)
		for _, fragment := range check.required {
			if !strings.Contains(body, fragment) {
				t.Errorf("%s missing consumer boundary %q", check.path, fragment)
			}
		}
		for _, forbidden := range []string{"unsafe.Pointer", "//go:linkname"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s bypasses opaque evidence with %q", check.path, forbidden)
			}
		}
	}
}
