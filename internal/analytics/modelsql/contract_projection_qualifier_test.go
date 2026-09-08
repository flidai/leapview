package modelsql

import (
	"fmt"
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type qualifierTestResolver map[string]projectgraph.ResourceID

func (r qualifierTestResolver) ResolveReference(reference string, expected projectgraph.Kind) (projectgraph.ResourceID, error) {
	if id, ok := r[string(expected)+":"+reference]; ok {
		return id, nil
	}
	return "", fmt.Errorf("missing %s reference %q", expected, reference)
}

func TestCanonicalProjectionWithReferencesNormalizesImplicitQualifierByResourceID(t *testing.T) {
	resolver := qualifierTestResolver{
		"source:orders":         "source-same-id",
		"source:renamed_orders": "source-same-id",
	}
	original, err := CanonicalProjectionWithReferences(`SELECT orders.id FROM source.orders`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := CanonicalProjectionWithReferences(`SELECT renamed_orders.id FROM source.renamed_orders`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if original == nil || renamed == nil || *original != *renamed {
		t.Fatalf("renaming the underlying source changed the identity projection:\n%s\n%s", valueOrEmpty(original), valueOrEmpty(renamed))
	}
	if !strings.Contains(*original, `"names":["source-same-id","id"]`) {
		t.Fatalf("implicit qualifier was not normalized: %s", *original)
	}
}

func TestCanonicalProjectionWithReferencesPreservesThreePartModelReference(t *testing.T) {
	resolver := qualifierTestResolver{"model:orders": "model-stable-id"}
	projection, err := CanonicalProjectionWithReferences(`SELECT model.orders.id FROM model.orders`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*projection, `"names":["model-stable-id","id"]`) || !strings.Contains(*projection, `"resourceID":"model-stable-id"`) {
		t.Fatalf("three-part model reference was not normalized: %s", *projection)
	}
}

func TestCanonicalProjectionWithReferencesNormalizesImplicitModelQualifier(t *testing.T) {
	resolver := qualifierTestResolver{"model:orders": "model-stable-id"}
	projection, err := CanonicalProjectionWithReferences(`SELECT orders.id FROM model.orders`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*projection, `"names":["model-stable-id","id"]`) {
		t.Fatalf("implicit model qualifier was not normalized: %s", *projection)
	}
}

func TestCanonicalProjectionWithReferencesExplicitAliasShieldsImplicitQualifier(t *testing.T) {
	resolver := qualifierTestResolver{"source:orders": "source-stable-id"}
	projection, err := CanonicalProjectionWithReferences(`SELECT o.id, orders.id FROM source.orders AS o`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(*projection, `"names":["source-stable-id","id"]`) {
		t.Fatalf("explicit alias query unexpectedly normalized a column qualifier: %s", *projection)
	}
	for _, qualifier := range []string{`"names":["o","id"]`, `"names":["orders","id"]`} {
		if !strings.Contains(*projection, qualifier) {
			t.Fatalf("explicit/unrelated qualifier %s was not preserved: %s", qualifier, *projection)
		}
	}
}

func TestCanonicalProjectionWithReferencesPreservesUnrelatedQualifier(t *testing.T) {
	resolver := qualifierTestResolver{"source:orders": "source-stable-id"}
	projection, err := CanonicalProjectionWithReferences(`SELECT unrelated.id FROM source.orders`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*projection, `"names":["unrelated","id"]`) || strings.Contains(*projection, `"names":["source-stable-id","id"]`) {
		t.Fatalf("unrelated qualifier was not preserved: %s", *projection)
	}
}

func TestCanonicalProjectionWithReferencesCTEShieldOuterQualifier(t *testing.T) {
	resolver := qualifierTestResolver{"source:orders": "source-stable-id"}
	projection, err := CanonicalProjectionWithReferences(`WITH orders AS (SELECT id FROM source.orders) SELECT orders.id FROM orders`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*projection, `"names":["orders","id"]`) || strings.Contains(*projection, `"names":["source-stable-id","id"]`) {
		t.Fatalf("CTE qualifier was not shielded: %s", *projection)
	}
}

func TestCanonicalProjectionWithReferencesUnusedCTEDoesNotShieldRelation(t *testing.T) {
	resolver := qualifierTestResolver{"source:orders": "source-stable-id"}
	projection, err := CanonicalProjectionWithReferences(`WITH orders AS (SELECT 1 AS id) SELECT orders.id FROM source.orders`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*projection, `"names":["source-stable-id","id"]`) {
		t.Fatalf("unused CTE declaration incorrectly shielded source relation: %s", *projection)
	}
}

func TestCanonicalProjectionWithReferencesRejectsAmbiguousImplicitQualifier(t *testing.T) {
	resolver := qualifierTestResolver{"source:orders": "source-stable-id"}
	projection, err := CanonicalProjectionWithReferences(`SELECT orders.id FROM source.orders JOIN source.orders ON TRUE`, resolver)
	if err == nil || projection != nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous qualifier was not rejected: projection=%v error=%v", projection, err)
	}
}

func TestCanonicalProjectionWithReferencesRejectsAliasAndImplicitCollision(t *testing.T) {
	resolver := qualifierTestResolver{
		"source:orders":    "source-orders-id",
		"source:customers": "source-customers-id",
	}
	projection, err := CanonicalProjectionWithReferences(`SELECT orders.id FROM source.orders JOIN source.customers AS orders ON TRUE`, resolver)
	if err == nil || projection != nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("alias/implicit collision was not rejected: projection=%v error=%v", projection, err)
	}
}

func TestCanonicalProjectionWithReferencesRejectsDuplicateExplicitAliases(t *testing.T) {
	resolver := qualifierTestResolver{
		"source:orders":    "source-orders-id",
		"source:customers": "source-customers-id",
	}
	projection, err := CanonicalProjectionWithReferences(`SELECT orders.id FROM source.orders AS orders JOIN source.customers AS orders ON TRUE`, resolver)
	if err == nil || projection != nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate explicit aliases were not rejected: projection=%v error=%v", projection, err)
	}
}

func TestCanonicalProjectionWithReferencesResolvesCorrelatedImplicitQualifier(t *testing.T) {
	resolver := qualifierTestResolver{"source:orders": "source-stable-id"}
	projection, err := CanonicalProjectionWithReferences(`SELECT orders.id FROM source.orders WHERE EXISTS (SELECT 1 WHERE orders.id > 0)`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(*projection, `"names":["source-stable-id","id"]`); got != 2 {
		t.Fatalf("expected both local and correlated qualifiers to normalize, got %d: %s", got, *projection)
	}
}

func TestCanonicalProjectionWithReferencesResolvesSetOperationQualifier(t *testing.T) {
	resolver := qualifierTestResolver{
		"source:orders":         "source-same-id",
		"source:renamed_orders": "source-same-id",
	}
	original, err := CanonicalProjectionWithReferences(`SELECT orders.id FROM source.orders UNION ALL SELECT orders.id FROM source.orders ORDER BY orders.id`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := CanonicalProjectionWithReferences(`SELECT renamed_orders.id FROM source.renamed_orders UNION ALL SELECT renamed_orders.id FROM source.renamed_orders ORDER BY renamed_orders.id`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if original == nil || renamed == nil || *original != *renamed {
		t.Fatalf("renaming set-operation source changed identity projection:\n%s\n%s", valueOrEmpty(original), valueOrEmpty(renamed))
	}
	if got := strings.Count(*original, `"names":["source-same-id","id"]`); got != 3 {
		t.Fatalf("expected both branches and set modifier to normalize, got %d: %s", got, *original)
	}
}

func TestCanonicalProjectionWithReferencesResolvesNestedSetOperationQualifier(t *testing.T) {
	resolver := qualifierTestResolver{
		"source:orders":         "source-same-id",
		"source:renamed_orders": "source-same-id",
	}
	original, err := CanonicalProjectionWithReferences(`(SELECT orders.id FROM source.orders UNION ALL SELECT orders.id FROM source.orders) UNION ALL SELECT orders.id FROM source.orders ORDER BY orders.id`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := CanonicalProjectionWithReferences(`(SELECT renamed_orders.id FROM source.renamed_orders UNION ALL SELECT renamed_orders.id FROM source.renamed_orders) UNION ALL SELECT renamed_orders.id FROM source.renamed_orders ORDER BY renamed_orders.id`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if original == nil || renamed == nil || *original != *renamed {
		t.Fatalf("renaming nested set-operation source changed identity projection:\n%s\n%s", valueOrEmpty(original), valueOrEmpty(renamed))
	}
	if got := strings.Count(*original, `"names":["source-same-id","id"]`); got != 4 {
		t.Fatalf("expected nested branches and set modifier to normalize, got %d: %s", got, *original)
	}
}

func TestCanonicalProjectionWithReferencesExplicitAliasShieldsCorrelatedOuterQualifier(t *testing.T) {
	resolver := qualifierTestResolver{
		"source:orders":   "source-orders-id",
		"source:payments": "source-payments-id",
	}
	projection, err := CanonicalProjectionWithReferences(`SELECT orders.id FROM source.orders WHERE EXISTS (SELECT 1 FROM source.payments AS orders WHERE orders.id > 0)`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(*projection, `"names":["source-orders-id","id"]`); got != 1 {
		t.Fatalf("outer qualifier did not remain distinct from inner explicit alias, got %d: %s", got, *projection)
	}
	if !strings.Contains(*projection, `"names":["orders","id"]`) {
		t.Fatalf("inner explicit alias was normalized: %s", *projection)
	}
}

func TestCanonicalProjectionWithReferencesCTERelationShieldsCorrelatedOuterQualifier(t *testing.T) {
	resolver := qualifierTestResolver{"source:orders": "source-stable-id"}
	projection, err := CanonicalProjectionWithReferences(`SELECT orders.id FROM source.orders WHERE EXISTS (WITH orders AS (SELECT 1 AS id) SELECT orders.id FROM orders)`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(*projection, `"names":["source-stable-id","id"]`); got != 1 {
		t.Fatalf("CTE relation did not shield outer qualifier, got %d: %s", got, *projection)
	}
	if !strings.Contains(*projection, `"names":["orders","id"]`) {
		t.Fatalf("CTE qualifier was normalized: %s", *projection)
	}
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}
