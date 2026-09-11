package module

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestSemanticContextMissingAuthorityFailsClosed(t *testing.T) {
	model := &semanticmodel.Model{}
	if err := authorizeSemanticExploration(t.Context(), nil, "sales", "orders", model, nil); err != nil {
		t.Fatal(err)
	}
	model.AccessPolicy.Datasets = map[string]semanticmodel.SemanticDatasetAccessSpec{"orders": {RequiredAccessGrants: []string{"staff"}}}
	if err := authorizeSemanticExploration(t.Context(), nil, "sales", "orders", model, nil); err == nil {
		t.Fatal("protected agent context accepted without authority")
	}
	if err := authorizeSemanticExploration(t.Context(), nil, "sales", "orders", nil, nil); err == nil {
		t.Fatal("unknown model accepted as public")
	}
}

type semanticContextConsumerStub struct {
	consumer *semanticquery.SemanticAccessConsumer
}

func (s semanticContextConsumerStub) SemanticConsumer(context.Context, string) (*semanticquery.SemanticAccessConsumer, error) {
	return s.consumer, nil
}

func TestSemanticContextAuthorizesCanonicalMembersAndScopedFilters(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales", Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName: "orders", GrainEntity: "order",
			Entities:   map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}},
			Dimensions: map[string]semanticmodel.MetricDimension{"id": {Datatype: semanticmodel.DataTypeInteger}},
		}},
		Metrics: map[string]semanticmodel.Metric{"count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.id"}}},
	}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	alias, unknown := "total", "unknown"
	tests := []struct {
		name   string
		spec   exploration.ExplorationSpec
		denied bool
	}{
		{name: "metric alias sort", spec: exploration.ExplorationSpec{Metrics: []exploration.ExplorationMetricRef{{Field: "count", Alias: &alias}}, Sort: []exploration.ExplorationSort{{Field: alias}}}},
		{name: "unknown dimension", spec: exploration.ExplorationSpec{Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.missing"}}}, denied: true},
		{name: "filter dataset cannot be ignored", spec: exploration.ExplorationSpec{Filters: []exploration.ExplorationFilter{{Field: "orders.id", DatasetID: &unknown}}}, denied: true},
		{name: "unknown sort", spec: exploration.ExplorationSpec{Sort: []exploration.ExplorationSort{{Field: "orders.missing"}}}, denied: true},
		{name: "unknown pivot metric", spec: exploration.ExplorationSpec{Pivot: &exploration.ExplorationPivotConfig{Metrics: []exploration.ExplorationMetricRef{{Field: "missing"}}}}, denied: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := tt.spec
			spec.SchemaVersion = 1
			spec.ModelID = "sales"
			if spec.Dimensions == nil {
				spec.Dimensions = []exploration.ExplorationDimensionRef{}
			}
			if spec.Metrics == nil {
				spec.Metrics = []exploration.ExplorationMetricRef{}
			}
			if spec.Filters == nil {
				spec.Filters = []exploration.ExplorationFilter{}
			}
			if spec.Sort == nil {
				spec.Sort = []exploration.ExplorationSort{}
			}
			if spec.Limit == 0 {
				spec.Limit = 100
			}
			for index := range spec.Sort {
				if spec.Sort[index].Direction == "" {
					spec.Sort[index].Direction = exploration.ExplorationSortDirectionAsc
				}
			}
			dataset := "orders"
			spec.DatasetID = &dataset
			err := authorizeSemanticExploration(t.Context(), semanticContextConsumerStub{consumer}, "sales", "orders", model, &spec)
			if (err != nil) != tt.denied {
				t.Fatalf("authorization error = %v; want denied=%v", err, tt.denied)
			}
		})
	}
}

func TestSemanticContextInfersRootsForOmittedDataset(t *testing.T) {
	model := multiRootSemanticContextModel()
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "sales", DatasetID: nil,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "customer_state"}},
		Metrics:    []exploration.ExplorationMetricRef{{Field: "share"}},
		Filters:    []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	}
	if err := authorizeSemanticExploration(t.Context(), semanticContextConsumerStub{consumer}, "sales", "", model, &spec); err != nil {
		t.Fatalf("omitted dataset multi-root context rejected: %v", err)
	}
}

func TestSemanticContextPivotProbeDeduplicatesOverlappingMultiRootMembers(t *testing.T) {
	model := multiRootSemanticContextModel()
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "sales", DatasetID: nil,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "customer_state"}},
		Metrics:    []exploration.ExplorationMetricRef{{Field: "share"}},
		Filters:    []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
		Pivot: &exploration.ExplorationPivotConfig{
			Rows:    []exploration.ExplorationDimensionRef{{Field: "customer_state"}},
			Columns: []exploration.ExplorationDimensionRef{},
			Metrics: []exploration.ExplorationMetricRef{{Field: "share"}},
		},
	}
	if err := authorizeSemanticExploration(t.Context(), semanticContextConsumerStub{consumer}, "sales", "", model, &spec); err != nil {
		t.Fatalf("overlapping multi-root pivot context rejected: %v", err)
	}
}

func TestSemanticContextOmittedDatasetRejectsUnknownMemberAndUnboundRoot(t *testing.T) {
	model := multiRootSemanticContextModel()
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []exploration.ExplorationSpec{
		{
			SchemaVersion: 1, ModelID: "sales", Dimensions: []exploration.ExplorationDimensionRef{},
			Metrics: []exploration.ExplorationMetricRef{{Field: "missing_metric"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
		},
		{
			SchemaVersion: 1, ModelID: "sales", Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders_status"}},
			Metrics: []exploration.ExplorationMetricRef{{Field: "customer_count"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
		},
	}
	for index, spec := range tests {
		if err := authorizeSemanticExploration(t.Context(), semanticContextConsumerStub{consumer}, "sales", "", model, &spec); err == nil {
			t.Fatalf("omitted dataset case %d unexpectedly authorized", index)
		}
	}
}

func TestSemanticContextProtectedOmittedDatasetRequiresRootGrant(t *testing.T) {
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "sales", DatasetID: nil,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "customer_state"}},
		Metrics:    []exploration.ExplorationMetricRef{{Field: "share"}},
		Filters:    []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	}
	deniedModel := multiRootSemanticContextModel()
	denied, err := protectedSemanticContextConsumer(t, deniedModel, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := authorizeSemanticExploration(t.Context(), semanticContextConsumerStub{denied}, "sales", "", deniedModel, &spec); err == nil {
		t.Fatal("protected omitted-dataset query accepted without required root grant")
	}

	allowedModel := multiRootSemanticContextModel()
	allowed, err := protectedSemanticContextConsumer(t, allowedModel, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := authorizeSemanticExploration(t.Context(), semanticContextConsumerStub{allowed}, "sales", "", allowedModel, &spec); err != nil {
		t.Fatalf("protected omitted-dataset query rejected with required root grant: %v", err)
	}
}

func protectedSemanticContextConsumer(t *testing.T, model *semanticmodel.Model, allow bool) (*semanticquery.SemanticAccessConsumer, error) {
	t.Helper()
	literal, err := semanticmodel.NewSemanticAccessLiteral("sales")
	if err != nil {
		return nil, err
	}
	model.AccessPolicy = semanticmodel.SemanticAccessPolicy{
		AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{
			"view_sales": {UserAttribute: "department", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetAccessSpec{
			"orders":    {RequiredAccessGrants: []string{"view_sales"}},
			"customers": {RequiredAccessGrants: []string{"view_sales"}},
		},
		Metrics: map[string][]string{"share": {"view_sales"}},
	}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		return nil, err
	}
	definition := access.SemanticAttributeDefinition{
		ID: "def-department", Name: "department", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
		Profile: semanticvalue.Profile, DefinitionVersion: 1,
		Metadata:       access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}},
		LifecycleState: access.SemanticAttributeActive, Enabled: true,
	}
	registryDigest, err := access.SemanticAttributeRegistryDigest(semanticvalue.Profile, []access.SemanticAttributeDefinition{definition})
	if err != nil {
		return nil, err
	}
	registry := access.SemanticAttributeRegistrySnapshot{
		State:       access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1, Digest: registryDigest},
		Definitions: []access.SemanticAttributeDefinition{definition},
	}
	principal := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"}
	var attributes []access.EffectiveSemanticAttribute
	var assignments []access.SemanticAttributeAssignment
	if allow {
		values, valueDigest, err := access.CanonicalSemanticAttributeValues(definition, "sales")
		if err != nil {
			return nil, err
		}
		attribute := access.EffectiveSemanticAttribute{
			DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion,
			Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: valueDigest, Source: "direct",
		}
		attributes = []access.EffectiveSemanticAttribute{attribute}
		assignments = []access.SemanticAttributeAssignment{{
			ID: "assignment-department", DefinitionID: definition.ID, DefinitionName: definition.Name,
			DefinitionVersion: definition.DefinitionVersion, Type: definition.Type, Shape: definition.Shape, Subject: principal,
			CanonicalValues: values, ValueDigest: valueDigest, AssignmentVersion: 1,
		}}
	}
	controlDigest, err := access.SemanticAttributeControlDigest(assignments, nil)
	if err != nil {
		return nil, err
	}
	control := access.SemanticAttributeControlSnapshot{
		State: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 1, Digest: controlDigest}, Assignments: assignments,
	}
	attributeDigest, err := semanticquery.EffectiveSemanticAttributeDigest(attributes)
	if err != nil {
		return nil, err
	}
	snapshot := semanticquery.SemanticAccessAttributeSnapshot{
		InstanceID: "instance-1", PrincipalID: principal.ID, Registry: registry, Control: control,
		EffectiveAttributes: attributes, EffectiveAttributeDigest: attributeDigest,
	}
	if allow {
		evidence, err := access.NewSemanticAttributeDirectEvidence("instance-1", principal.ID, []access.SubjectRef{principal}, control, attributes)
		if err != nil {
			return nil, err
		}
		snapshot.DirectAssignmentEvidence = evidence
	}
	observedAt, err := time.Parse(time.RFC3339Nano, "2026-09-06T12:00:00Z")
	if err != nil {
		return nil, err
	}
	authority := semanticquery.SemanticAccessAuthority{InstanceID: "instance-1", Registry: registry, Control: control, ObservedAt: observedAt}
	return semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "sales", Generation: "generation-1", PrincipalID: principal.ID,
		Authority: func() (semanticquery.SemanticAccessAttributeSnapshot, semanticquery.SemanticAccessAuthority, error) {
			return snapshot, authority, nil
		},
	})
}

func multiRootSemanticContextModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "sales",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders":    {Model: "orders"},
			"customers": {Model: "customers"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders", GrainEntity: "order",
				Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id":    {Type: "number", Datatype: semanticmodel.DataTypeInteger},
					"customer_id": {Type: "number", Datatype: semanticmodel.DataTypeInteger},
					"status":      {Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
			"customers": {
				ModelName: "customers", GrainEntity: "customer",
				Entities: map[string]semanticmodel.EntityDefinition{"customer": {Type: "primary", Fields: []string{"customer_id"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer_id": {Type: "number", Datatype: semanticmodel.DataTypeInteger},
					"state":       {Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
		},
		Relationships: []semanticmodel.Relationship{{
			ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"},
			ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one",
		}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"customer_state": {
				Type: "string", Datatype: semanticmodel.DataTypeString,
				Bindings: map[string]semanticmodel.DimensionBinding{
					"orders":    {Field: "customers.state", Path: []string{"orders_customers"}},
					"customers": {Field: "customers.state"},
				},
			},
			"orders_status": {
				Type: "string", Datatype: semanticmodel.DataTypeString,
				Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}},
			},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count":    {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
			"customer_count": {Type: "aggregate", Dataset: "customers", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "customers.customer_id"}},
			"share":          {Type: "ratio", Numerator: "order_count", Denominator: "customer_count"},
		},
	}
}
