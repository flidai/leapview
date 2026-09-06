package architecture

import (
	"strings"
	"testing"
)

func TestSemanticConsumerKeepsExistingAuthorities(t *testing.T) {
	root := repoRoot(t)
	for _, path := range []string{
		"internal/analytics/query/semantic_consumer.go",
		"internal/analytics/materialize/semantic_consumer.go",
		"internal/analytics/materialize/semantic_consumer_authorization.go",
		"internal/analytics/materialize/semantic_cache.go",
		"internal/dashboard/queryauthz/semantic_discovery.go",
	} {
		body := readArchitectureFixture(t, root, path)
		for _, forbidden := range []string{`"crypto/`, `"database/sql"`, `"text/template"`, "type Predicate struct", "func matchesGrant", "CanonicalSemanticAttributeValues("} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s introduced another authority: %s", path, forbidden)
			}
		}
	}
	for path, required := range map[string][]string{
		"internal/analytics/materialize/runtime.go":                      {"admitSemanticConsumer(ctx, request)", "validateSemanticPlan(execCtx, plan)"},
		"internal/analytics/materialize/runtime_arrow_result.go":         {"validateSemanticPlan(ctx, plan)", "r.semanticConsumer != nil && r.semanticCache != nil", "r.validateSemanticCache(checkCtx, request)"},
		"internal/analytics/materialize/query_cache.go":                  {"guard(checkCtx)", "c.scope.Delete(address.key)", "validate(flightCtx)", "validate(ctx)"},
		"internal/project/identityledger/postgres/lifecycle_evidence.go": {"pgx.RepeatableRead", "pgx.ReadOnly", "max(h.sequence)", "contractPublicationTx"},
		"internal/analytics/materialize/runtime_bundle_pipeline.go":      {"r.protectedSemanticModel()"},
		"internal/access/postgres/semantic_attribute_resolution.go":      {"REPEATABLE READ, READ ONLY", "EffectiveDirectSemanticAttributeAssignments"},
	} {
		body := readArchitectureFixture(t, root, path)
		for _, marker := range required {
			if !strings.Contains(body, marker) {
				t.Errorf("%s lost consumer guard %q", path, marker)
			}
		}
	}
}
