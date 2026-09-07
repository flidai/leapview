package compiler

import (
	"fmt"
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

func TestLowerModelChecksPreservesMetadataAcrossVariants(t *testing.T) {
	checks := modelCheckFixture()
	lowered, err := lowerModelChecks(&checks)
	if err != nil {
		t.Fatalf("lower model checks: %v", err)
	}

	minimum, maximum := int64(1), int64(100)
	want := []semanticmodel.ModelCheck{
		{ID: "orders_not_null", Type: "non_null", Field: "order_id", Severity: "error", Description: "Order identifiers are required", Tags: []string{"quality", "orders"}},
		{ID: "orders_unique", Type: "unique", Fields: []string{"order_id", "tenant_id"}, Severity: "warning", Description: "Order identity is unique per tenant", Tags: []string{"quality", "orders"}},
		{ID: "orders_status_values", Type: "accepted_values", Field: "status", Values: []string{"open", "closed"}, Severity: "error", Description: "Order status is governed", Tags: []string{"quality", "orders"}},
		{ID: "orders_customer_relation", Type: "relationship", Field: "customer_id", To: "customers.customer_id", Severity: "warning", Description: "Orders refer to customers", Tags: []string{"quality", "relationships"}},
		{ID: "orders_row_count", Type: "row_count", Minimum: &minimum, Maximum: &maximum, Severity: "error", Description: "Orders must be populated", Tags: []string{"quality", "volume"}},
	}
	if !reflect.DeepEqual(lowered, want) {
		t.Fatalf("lowered checks = %#v, want %#v", lowered, want)
	}

	// The normalized slice must not retain the authored tags backing array.
	authoredTags := checks[0].Value.(*projectcontracts.ModelCheckNonNullVariant).Tags
	(*authoredTags)[0] = "changed"
	if lowered[0].Tags[0] != "quality" {
		t.Fatal("lowered check tags alias authored state")
	}
}

func TestLowerModelChecksWithoutChecksRemainsEmpty(t *testing.T) {
	if got, err := lowerModelChecks(nil); err != nil || got != nil {
		t.Fatalf("nil checks = %#v, %v; want nil, nil", got, err)
	}
	empty := []projectcontracts.ModelCheck{}
	if got, err := lowerModelChecks(&empty); err != nil || len(got) != 0 {
		t.Fatalf("empty checks = %#v, %v; want empty, nil", got, err)
	}
}

func TestLowerModelChecksRequiresStableID(t *testing.T) {
	for index := range modelCheckFixture() {
		t.Run(fmt.Sprintf("variant_%d", index), func(t *testing.T) {
			checks := []projectcontracts.ModelCheck{modelCheckFixture()[index]}
			setModelCheckID(&checks[0], "")
			_, err := lowerModelChecks(&checks)
			if err == nil || err.Error() != "checks[0] id is required" {
				t.Fatalf("missing ID error = %v, want checks[0] id is required", err)
			}
		})
	}
}

func TestLowerModelChecksRejectsDuplicateStableID(t *testing.T) {
	checks := modelCheckFixture()[:2]
	setModelCheckID(&checks[1], "orders_not_null")
	_, err := lowerModelChecks(&checks)
	if err == nil || err.Error() != `checks[1] duplicates id "orders_not_null"` {
		t.Fatalf("duplicate ID error = %v, want deterministic duplicate error", err)
	}
}

func modelCheckFixture() []projectcontracts.ModelCheck {
	severityError, severityWarning := "error", "warning"
	descriptions := []string{
		"Order identifiers are required",
		"Order identity is unique per tenant",
		"Order status is governed",
		"Orders refer to customers",
		"Orders must be populated",
	}
	tags := [][]string{
		{"quality", "orders"},
		{"quality", "orders"},
		{"quality", "orders"},
		{"quality", "relationships"},
		{"quality", "volume"},
	}
	minimum, maximum := int64(1), int64(100)
	return []projectcontracts.ModelCheck{
		{Value: &projectcontracts.ModelCheckNonNullVariant{
			NonNullModelCheck: projectcontracts.NonNullModelCheck{ID: "orders_not_null", Field: "order_id", Severity: &severityError, Description: &descriptions[0], Tags: &tags[0]},
			Type:              "non_null",
		}},
		{Value: &projectcontracts.ModelCheckUniqueVariant{
			UniqueModelCheck: projectcontracts.UniqueModelCheck{ID: "orders_unique", Fields: []string{"order_id", "tenant_id"}, Severity: &severityWarning, Description: &descriptions[1], Tags: &tags[1]},
			Type:             "unique",
		}},
		{Value: &projectcontracts.ModelCheckAcceptedValuesVariant{
			AcceptedValuesModelCheck: projectcontracts.AcceptedValuesModelCheck{ID: "orders_status_values", Field: "status", Values: []string{"open", "closed"}, Severity: &severityError, Description: &descriptions[2], Tags: &tags[2]},
			Type:                     "accepted_values",
		}},
		{Value: &projectcontracts.ModelCheckRelationshipVariant{
			RelationshipModelCheck: projectcontracts.RelationshipModelCheck{ID: "orders_customer_relation", Field: "customer_id", To: "customers.customer_id", Severity: &severityWarning, Description: &descriptions[3], Tags: &tags[3]},
			Type:                   "relationship",
		}},
		{Value: &projectcontracts.ModelCheckRowCountVariant{
			RowCountModelCheck: projectcontracts.RowCountModelCheck{ID: "orders_row_count", Minimum: &minimum, Maximum: &maximum, Severity: &severityError, Description: &descriptions[4], Tags: &tags[4]},
			Type:               "row_count",
		}},
	}
}

func setModelCheckID(check *projectcontracts.ModelCheck, id string) {
	switch variant := check.Value.(type) {
	case *projectcontracts.ModelCheckNonNullVariant:
		variant.ID = id
	case *projectcontracts.ModelCheckUniqueVariant:
		variant.ID = id
	case *projectcontracts.ModelCheckAcceptedValuesVariant:
		variant.ID = id
	case *projectcontracts.ModelCheckRelationshipVariant:
		variant.ID = id
	case *projectcontracts.ModelCheckRowCountVariant:
		variant.ID = id
	default:
		panic("unsupported model check fixture variant")
	}
}
