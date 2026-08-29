package cache

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
)

func digest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func validDependency(t *testing.T) resultidentity.Dependency {
	t.Helper()
	d, err := resultidentity.NewDependency(resultidentity.DependencyInput{
		SemanticModelID: "semantic_sales", SemanticModelDigest: digest('a'),
		Relations:          []resultidentity.RelationRevision{{RelationID: "orders", RevisionDigest: digest('b')}},
		BindingFingerprint: digest('c'),
		Execution:          resultidentity.ExecutionIdentity{PlannerDigest: digest('d'), RuntimeDigest: digest('e'), CapabilityDigest: digest('f'), SettingsDigest: digest('0')},
		ResultFormat:       resultidentity.ResultFormat{Name: "arrow-ipc", Version: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func validPartition(t *testing.T) resultidentity.Partition {
	t.Helper()
	p, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionProduction, ProjectID: "project_sales", Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCanonicalQueryDigestDistinguishesTypedValuesAndExcludesAuditIdentity(t *testing.T) {
	base := dataquery.Query{Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows, ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders", Fields: []dataquery.Field{{Field: "id", Alias: "id"}}, Filters: []dataquery.Filter{{Field: "id", Operator: "eq", Values: []any{int64(1)}}}}
	first, err := CanonicalQueryDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	base.PrincipalID, base.RequestID, base.CorrelationID = "principal-a", "request-a", "corr-a"
	second, err := CanonicalQueryDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("audit identities changed query equivalence")
	}
	base.Filters[0].Values = []any{float64(1)}
	third, err := CanonicalQueryDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	if first == third {
		t.Fatal("int64 and float64 bound values collided")
	}
}

func TestNewKeyBindsPartitionDependencyPolicyAndQuery(t *testing.T) {
	queryDigest, err := CanonicalQueryDigest(dataquery.Query{Kind: dataquery.KindSemanticRows, Target: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	input := KeyInput{Partition: validPartition(t), Dependency: validDependency(t), EffectivePolicyFingerprint: digest('1'), CanonicalQueryDigest: queryDigest}
	key, err := NewKey(input)
	if err != nil {
		t.Fatal(err)
	}
	if key.Version() != CacheKeyVersion || key.Digest() == "" || len(key.Canonical()) == 0 {
		t.Fatalf("invalid key: %#v", key)
	}
	original := key.Canonical()
	original[0] = 'x'
	if key.Canonical()[0] != '{' {
		t.Fatal("canonical bytes aliased")
	}
	changed := input
	changed.EffectivePolicyFingerprint = digest('2')
	other, err := NewKey(changed)
	if err != nil {
		t.Fatal(err)
	}
	if key.Digest() == other.Digest() {
		t.Fatal("policy boundary did not rotate cache key")
	}
}

func TestCanonicalQueryDigestRejectsUnsupportedValues(t *testing.T) {
	_, err := CanonicalQueryDigest(dataquery.Query{Filters: []dataquery.Filter{{Values: []any{func() {}}}}})
	if err == nil {
		t.Fatal("unsupported value was accepted")
	}
}

func TestCanonicalQueryDigestPreservesAdmittedDynamicTypes(t *testing.T) {
	when := time.Date(2026, 8, 29, 12, 34, 56, 0, time.UTC)
	intValue := int64(7)
	base := dataquery.Query{Filters: []dataquery.Filter{{Field: "id", Operator: "in", Values: []any{json.Number("7"), &intValue, &when}}}}
	digest, err := CanonicalQueryDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{float64(7), int64(7), json.Number("7"), &intValue, &when} {
		query := base
		query.Filters = []dataquery.Filter{{Field: "id", Operator: "eq", Values: []any{value}}}
		if _, err := CanonicalQueryDigest(query); err != nil {
			t.Fatalf("value %T rejected: %v", value, err)
		}
	}
	changed := base
	changed.Filters = []dataquery.Filter{{Field: "id", Operator: "in", Values: []any{json.Number("7.0"), &intValue, &when}}}
	changedDigest, err := CanonicalQueryDigest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if digest == changedDigest {
		t.Fatal("json.Number lexical/type distinction was lost")
	}
}
