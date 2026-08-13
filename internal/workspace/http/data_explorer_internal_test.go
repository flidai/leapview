package http

import (
	"net/url"
	"testing"

	uisignals "github.com/flidai/leapview/internal/workspace/ui/signals"
)

func TestDataExplorerCommandFromURLRestoresExploration(t *testing.T) {
	values := url.Values{
		"mode": {"explore"}, "workspace": {"sales"}, "model": {"commerce"}, "dataset": {"orders"},
		"dimension": {"orders.status"}, "measure": {"order_count"}, "limit": {"250"},
		"filter": {`{"field":"orders.status","operator":"equals","values":["delivered"]}`},
		"sort":   {`{"field":"order_count","direction":"desc"}`},
		"time":   {`{"field":"orders.created_at","grain":"month"}`},
	}
	command := dataExplorerCommandFromURL(values)
	if uisignals.ValueOrZero(command.Mode) != "explore" || command.Explore == nil {
		t.Fatalf("command = %#v", command)
	}
	explore := command.Explore
	if explore.Limit != 250 || len(explore.Dimensions) != 1 || len(explore.Measures) != 1 {
		t.Fatalf("exploration = %#v", explore)
	}
	if len(explore.Filters) != 1 || explore.Filters[0].Values[0] != "delivered" {
		t.Fatalf("filters = %#v", explore.Filters)
	}
	if len(explore.Sort) != 1 || explore.Sort[0].Direction != "desc" {
		t.Fatalf("sort = %#v", explore.Sort)
	}
	if explore.Time == nil || explore.Time.Grain != "month" {
		t.Fatalf("time = %#v", explore.Time)
	}
}

func TestDataExploreFieldsDeclareBaseTableCompatibility(t *testing.T) {
	model := DataExplorerModel{
		Tables: map[string]DataExplorerTable{
			"orders":    {Dimensions: map[string]DataExplorerField{"order_id": {Name: "order_id"}}},
			"customers": {Dimensions: map[string]DataExplorerField{"state": {Name: "state"}}},
			"items":     {Dimensions: map[string]DataExplorerField{"sku": {Name: "sku"}}},
		},
		Measures: map[string]DataExplorerMeasure{
			"order_count": {Name: "order_count", Fact: "orders"},
			"item_count":  {Name: "item_count", Fact: "items"},
		},
		Relationships: []DataExplorerRelationship{
			{ID: "orders_customers", From: "orders.customer_id", To: "customers.customer_id", Cardinality: "many_to_one"},
			{ID: "items_orders", From: "items.order_id", To: "orders.order_id", Cardinality: "many_to_one"},
		},
	}
	fields := dataExploreFields(model, uisignals.DataExploreCommand{}, "orders")
	byID := map[string]uisignals.DataExploreFieldSignal{}
	for _, field := range fields {
		byID[field.ID] = field
	}
	if field := byID["orders.order_id"]; !field.Compatible {
		t.Fatalf("base field compatibility = %#v", field)
	}
	if field := byID["customers.state"]; !field.Compatible || field.RelationshipPath == nil || len(*field.RelationshipPath) != 1 || (*field.RelationshipPath)[0] != "orders_customers" {
		t.Fatalf("related field compatibility = %#v", field)
	}
	if field := byID["items.sku"]; field.Compatible || uisignals.ValueOrZero(field.CompatibilityReason) == "" {
		t.Fatalf("reverse one-to-many field compatibility = %#v", field)
	}
	if field := byID["order_count"]; !field.Compatible {
		t.Fatalf("base measure compatibility = %#v", field)
	}
	if field := byID["item_count"]; field.Compatible || uisignals.ValueOrZero(field.CompatibilityReason) == "" {
		t.Fatalf("foreign measure compatibility = %#v", field)
	}
	validDimensions, validMeasures := dataExploreFieldSets(fields)
	if validDimensions["items.sku"] || validMeasures["item_count"] {
		t.Fatalf("incompatible fields entered valid selection sets: dimensions=%#v measures=%#v", validDimensions, validMeasures)
	}
}
