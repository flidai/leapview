package model

import "testing"

func TestExecutionSnapshotOmitsAuthoringAndConnectionState(t *testing.T) {
	modelContext := &AIContext{Instructions: "model"}
	tableContext := &AIContext{Instructions: "table"}
	dimensionContext := &AIContext{Instructions: "dimension"}
	metricContext := &AIContext{Instructions: "metric"}
	model := &Model{
		AIContext:   modelContext,
		Connections: map[string]Connection{"warehouse": {Auth: ConnectionAuth{"token": "secret"}}},
		Sources:     map[string]Source{"orders": {Fields: map[string]SourceField{"id": {Name: "id"}}}},
		Tables: map[string]Table{"orders": {
			AIContext:   tableContext,
			GrainEntity: "order",
			Entities:    map[string]EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}, AIContext: &AIContext{Instructions: "entity"}}},
			Dimensions:  map[string]MetricDimension{"id": {Datatype: DataTypeInteger, AIContext: dimensionContext}},
		}},
		Datasets:                map[string]SemanticDatasetSpec{"orders": {Model: "orders", AIContext: &AIContext{Instructions: "dataset"}}},
		StructuredRelationships: map[string]RelationshipSpec{"orders_customers": {AIContext: &AIContext{Instructions: "relationship"}}},
		Dimensions: map[string]SemanticDimension{"order_id": {
			Type: "number", Datatype: DataTypeInteger, Bindings: map[string]DimensionBinding{"orders": {Field: "orders.id"}}, AIContext: &AIContext{Instructions: "semantic dimension"},
		}},
		Filters: map[string]SemanticFilterSpec{"owned": {Field: "orders.id", Operator: "equals", Value: 1, AIContext: &AIContext{Instructions: "filter"}}},
		Metrics: map[string]Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &MetricInput{Field: "orders.id"}, Where: []string{"owned"}, AIContext: metricContext}},
	}
	snapshot := model.ExecutionSnapshot()
	if snapshot == nil || snapshot == model {
		t.Fatal("snapshot did not detach model")
	}
	if snapshot.AIContext != nil || snapshot.Connections != nil || snapshot.Sources != nil {
		t.Fatalf("snapshot retained non-executable state: %#v", snapshot)
	}
	if snapshot.StructuredRelationships != nil {
		t.Fatalf("snapshot retained authored structured relationships: %#v", snapshot.StructuredRelationships)
	}
	if snapshot.Tables["orders"].AIContext != nil || snapshot.Tables["orders"].Entities["order"].AIContext != nil || snapshot.Tables["orders"].Dimensions["id"].AIContext != nil {
		t.Fatal("snapshot retained table authoring context")
	}
	if snapshot.Datasets["orders"].AIContext != nil || snapshot.Dimensions["order_id"].AIContext != nil || snapshot.Filters["owned"].AIContext != nil || snapshot.Metrics["order_count"].AIContext != nil {
		t.Fatal("snapshot retained semantic authoring context")
	}
	model.Tables["orders"] = Table{Dimensions: map[string]MetricDimension{"changed": {Datatype: DataTypeString}}}
	metric := model.Metrics["order_count"]
	metric.Where[0] = "changed"
	model.Metrics["order_count"] = metric
	if _, ok := snapshot.Tables["orders"].Dimensions["id"]; !ok || snapshot.Metrics["order_count"].Where[0] != "owned" {
		t.Fatal("snapshot executable state aliases authored model")
	}
}

func TestClonePhysicalMetadataOmitsAuthoringContextAndDetachesSlices(t *testing.T) {
	dimension := MetricDimension{
		Field:     "orders.customer_id",
		Table:     "orders",
		Name:      "customer_id",
		AIContext: &AIContext{Instructions: "authoring-only"},
	}
	clonedDimension := CloneMetricDimension(dimension)
	if clonedDimension.AIContext != nil {
		t.Fatal("cloned dimension retained authoring context")
	}

	relationships := []Relationship{{
		ID:          "orders_customers",
		FromDataset: "orders",
		FromFields:  []string{"customer_id"},
		ToDataset:   "customers",
		ToFields:    []string{"id"},
		AIContext:   &AIContext{Instructions: "authoring-only"},
	}}
	clonedRelationships := CloneRelationships(relationships)
	if len(clonedRelationships) != 1 || clonedRelationships[0].AIContext != nil {
		t.Fatal("cloned relationship retained authoring context")
	}
	if &clonedRelationships[0].FromFields[0] == &relationships[0].FromFields[0] || &clonedRelationships[0].ToFields[0] == &relationships[0].ToFields[0] {
		t.Fatal("cloned relationship endpoint fields alias source slices")
	}
	relationships[0].FromFields[0] = "changed"
	relationships[0].ToFields[0] = "changed"
	if clonedRelationships[0].FromFields[0] != "customer_id" || clonedRelationships[0].ToFields[0] != "id" {
		t.Fatal("cloned relationship endpoint fields changed with source slices")
	}
}

func TestExecutionSnapshotDetachesEvidenceAndChecks(t *testing.T) {
	minimum, maximum := int64(1), int64(9)
	model := &Model{Tables: map[string]Table{"orders": Table{
		SQLAnalysisEvidence: &SQLAnalysisEvidence{Validated: true, SourceRefs: []string{"orders"}, ModelRefs: []string{"customers"}},
		Checks:              []ModelCheck{{Type: "accepted_values", Fields: []string{"status"}, Values: []string{"open", "closed"}, Minimum: &minimum, Maximum: &maximum}},
	}}}
	snapshot := model.ExecutionSnapshot()
	if snapshot == nil || snapshot.Tables["orders"].SQLAnalysisEvidence == nil || len(snapshot.Tables["orders"].Checks) != 1 {
		t.Fatal("snapshot lost table evidence or checks")
	}
	snapshotEvidence := snapshot.Tables["orders"].SQLAnalysisEvidence
	snapshotEvidence.SourceRefs[0] = "changed"
	snapshotEvidence.ModelRefs[0] = "changed"
	snapshotCheck := &snapshot.Tables["orders"].Checks[0]
	snapshotCheck.Fields[0] = "changed"
	snapshotCheck.Values[0] = "changed"
	*snapshotCheck.Minimum = 100
	*snapshotCheck.Maximum = 200
	authored := model.Tables["orders"]
	if authored.SQLAnalysisEvidence.SourceRefs[0] != "orders" || authored.SQLAnalysisEvidence.ModelRefs[0] != "customers" || authored.Checks[0].Fields[0] != "status" || authored.Checks[0].Values[0] != "open" || *authored.Checks[0].Minimum != minimum || *authored.Checks[0].Maximum != maximum {
		t.Fatal("snapshot table evidence or checks alias authored state")
	}
}
