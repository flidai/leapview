package query

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/semanticvalue"
)

const (
	semanticAccessObservedAt = "2026-09-06T12:00:00Z"
)

func semanticAccessTestModel(t *testing.T) *semanticmodel.Model {
	t.Helper()
	literal := func(value any) semanticmodel.SemanticAccessLiteral {
		result, err := semanticmodel.NewSemanticAccessLiteral(value)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	fields := map[string]semanticmodel.MetricDimension{
		"id":          {Field: "orders.id", Table: "orders", Name: "id", Type: "number", Datatype: semanticmodel.DataTypeInteger},
		"region":      {Field: "orders.region", Table: "orders", Name: "region", Type: "string", Datatype: semanticmodel.DataTypeString},
		"account_id":  {Field: "orders.account_id", Table: "orders", Name: "account_id", Type: "number", Datatype: semanticmodel.DataTypeInteger},
		"amount":      {Field: "orders.amount", Table: "orders", Name: "amount", Type: "number", Datatype: semanticmodel.DataTypeDecimal},
		"approved":    {Field: "orders.approved", Table: "orders", Name: "approved", Type: "boolean", Datatype: semanticmodel.DataTypeBoolean},
		"order_date":  {Field: "orders.order_date", Table: "orders", Name: "order_date", Type: "date", Datatype: semanticmodel.DataTypeDate},
		"occurred_at": {Field: "orders.occurred_at", Table: "orders", Name: "occurred_at", Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ},
	}
	columns := map[string]semanticmodel.ModelColumn{}
	for name, field := range fields {
		columns[name] = semanticmodel.ModelColumn{Name: name, SourceField: name, Type: field.Type, Datatype: field.Datatype}
	}
	dimension := func(field string, datatype semanticmodel.LogicalDataType) semanticmodel.SemanticDimension {
		return semanticmodel.SemanticDimension{Datatype: datatype, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders." + field}}}
	}
	return &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName: "orders_model", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}},
			Dimensions: fields, Columns: columns,
		}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders_model"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"region": dimension("region", semanticmodel.DataTypeString), "account": dimension("account_id", semanticmodel.DataTypeInteger),
			"amount": dimension("amount", semanticmodel.DataTypeDecimal), "approved": dimension("approved", semanticmodel.DataTypeBoolean),
			"orderDate": dimension("order_date", semanticmodel.DataTypeDate), "occurredAt": dimension("occurred_at", semanticmodel.DataTypeDateTimeTZ),
		},
		Metrics: map[string]semanticmodel.Metric{
			"revenue":        {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.amount"}},
			"orderCount":     {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.id"}},
			"doubledRevenue": {Type: "derived", Expression: "${revenue} * 2"},
			"averageRevenue": {Type: "ratio", Numerator: "revenue", Denominator: "orderCount"},
		},
		AccessPolicy: semanticmodel.SemanticAccessPolicy{
			AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{
				"canViewSales":   {UserAttribute: "department", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal("sales"), literal("finance")}},
				"canViewAccount": {UserAttribute: "accountIds", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal(json.Number("9007199254740993"))}},
				"canViewDate":    {UserAttribute: "accessDate", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal("2026-09-06")}},
				"decimalGate":    {UserAttribute: "amountLimit", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal(json.Number("9007199254740993.1250"))}},
				"booleanGate":    {UserAttribute: "approval", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal(true)}},
				"timestampGate":  {UserAttribute: "accessInstant", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal("2026-09-06T03:30:00Z")}},
			},
			Datasets: map[string]semanticmodel.SemanticDatasetAccessSpec{"orders": {
				RequiredAccessGrants: []string{"canViewSales"},
				AccessFilters: []semanticmodel.SemanticAccessFilterSpec{
					{Field: "region", UserAttribute: "regions"}, {Field: "account", UserAttribute: "accountIds"},
					{Field: "amount", UserAttribute: "amountLimit"}, {Field: "approved", UserAttribute: "approval"},
					{Field: "orderDate", UserAttribute: "accessDate"}, {Field: "occurredAt", UserAttribute: "accessInstant"},
				},
			}},
			Dimensions: map[string][]string{"region": {"canViewAccount"}},
			Metrics:    map[string][]string{"revenue": {"canViewAccount"}, "doubledRevenue": {"canViewDate"}},
		},
	}
}

func semanticAccessDefinitions() []access.SemanticAttributeDefinition {
	definition := func(id, name string, typeName semanticvalue.Type, shape access.SemanticAttributeShape) access.SemanticAttributeDefinition {
		return access.SemanticAttributeDefinition{ID: id, Name: name, Type: typeName, Shape: shape, Profile: semanticvalue.Profile,
			DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true,
			Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}}}
	}
	return []access.SemanticAttributeDefinition{
		definition("def-account", "accountIds", semanticvalue.TypeInteger, access.SemanticAttributeList),
		definition("def-date", "accessDate", semanticvalue.TypeDate, access.SemanticAttributeScalar),
		definition("def-instant", "accessInstant", semanticvalue.TypeTimestamp, access.SemanticAttributeScalar),
		definition("def-approval", "approval", semanticvalue.TypeBoolean, access.SemanticAttributeScalar),
		definition("def-amount", "amountLimit", semanticvalue.TypeDecimal, access.SemanticAttributeScalar),
		definition("def-department", "department", semanticvalue.TypeString, access.SemanticAttributeScalar),
		definition("def-regions", "regions", semanticvalue.TypeString, access.SemanticAttributeList),
	}
}

func semanticAccessRegistry(definitions []access.SemanticAttributeDefinition) access.SemanticAttributeRegistrySnapshot {
	digest, _ := access.SemanticAttributeRegistryDigest(semanticvalue.Profile, definitions)
	return access.SemanticAttributeRegistrySnapshot{State: access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 7, Digest: digest}, Definitions: definitions}
}

func semanticAccessControl(assignments []access.SemanticAttributeAssignment, mappings []access.TrustedClaimMapping) access.SemanticAttributeControlSnapshot {
	digest, _ := access.SemanticAttributeControlDigest(assignments, mappings)
	return access.SemanticAttributeControlSnapshot{State: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 11, Digest: digest}, Assignments: assignments, Mappings: mappings}
}

func compileSemanticAccessTestPolicy(t *testing.T) (*CompiledSemanticAccessPolicy, *semanticmodel.Model) {
	t.Helper()
	model := semanticAccessTestModel(t)
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatalf("CompileModel: %v", err)
	}
	policy, err := CompileSemanticAccessPolicy("instance-1", "semantic-model:sales", "generation-9", model, compiled, semanticAccessRegistry(semanticAccessDefinitions()))
	if err != nil {
		t.Fatalf("CompileSemanticAccessPolicy: %v", err)
	}
	return policy, model
}

func semanticAccessEffective(t *testing.T, definitions []access.SemanticAttributeDefinition) []access.EffectiveSemanticAttribute {
	t.Helper()
	inputs := map[string]any{
		"department": "sales", "regions": []string{"west", "east"}, "accountIds": []int64{9007199254740993, 7},
		"amountLimit": json.Number("9007199254740993.1250"), "approval": true, "accessDate": "2026-09-06",
		"accessInstant": "2026-09-06T05:30:00+02:00",
	}
	result := make([]access.EffectiveSemanticAttribute, 0, len(definitions))
	for _, definition := range definitions {
		values, digest, err := access.CanonicalSemanticAttributeValues(definition, inputs[definition.Name])
		if err != nil {
			t.Fatalf("canonicalize %s: %v", definition.Name, err)
		}
		result = append(result, access.EffectiveSemanticAttribute{DefinitionID: definition.ID, DefinitionName: definition.Name,
			DefinitionVersion: definition.DefinitionVersion, Type: definition.Type, Shape: definition.Shape,
			CanonicalValues: values, ValueDigest: digest, Source: "direct"})
	}
	return result
}

func semanticAccessSnapshot(t *testing.T, attributes []access.EffectiveSemanticAttribute) (SemanticAccessAttributeSnapshot, SemanticAccessAuthority) {
	return semanticAccessSnapshotForPrincipal(t, "principal-1", attributes)
}

func semanticAccessSnapshotForPrincipal(t *testing.T, principalID string, attributes []access.EffectiveSemanticAttribute) (SemanticAccessAttributeSnapshot, SemanticAccessAuthority) {
	t.Helper()
	digest, err := EffectiveSemanticAttributeDigest(attributes)
	if err != nil {
		t.Fatal(err)
	}
	registry := semanticAccessRegistry(semanticAccessDefinitions())
	assignments := make([]access.SemanticAttributeAssignment, 0, len(attributes))
	for _, attribute := range attributes {
		if attribute.Source != "direct" && attribute.Source != "direct+trusted_claim" {
			continue
		}
		assignments = append(assignments, access.SemanticAttributeAssignment{ID: "assignment-" + attribute.DefinitionID,
			DefinitionID: attribute.DefinitionID, DefinitionName: attribute.DefinitionName, DefinitionVersion: attribute.DefinitionVersion,
			Type: attribute.Type, Shape: attribute.Shape, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID},
			CanonicalValues: append([]string(nil), attribute.CanonicalValues...), ValueDigest: attribute.ValueDigest, AssignmentVersion: 1})
	}
	control := semanticAccessControl(assignments, nil)
	directEvidence := access.SemanticAttributeDirectEvidence{}
	if len(assignments) != 0 {
		directEvidence, err = access.NewSemanticAttributeDirectEvidence("instance-1", principalID,
			[]access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: principalID}}, control, attributes)
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot := SemanticAccessAttributeSnapshot{InstanceID: "instance-1", PrincipalID: principalID, Registry: registry, Control: control,
		EffectiveAttributes: attributes, EffectiveAttributeDigest: digest, DirectAssignmentEvidence: directEvidence}
	observedAt, err := time.Parse(time.RFC3339Nano, semanticAccessObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, SemanticAccessAuthority{InstanceID: "instance-1", Registry: registry, Control: control, ObservedAt: observedAt}
}

func reverseEffectiveAttributes(values []access.EffectiveSemanticAttribute) []access.EffectiveSemanticAttribute {
	result := append([]access.EffectiveSemanticAttribute(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func replaceDefinition(definitions []access.SemanticAttributeDefinition, name string, change func(*access.SemanticAttributeDefinition)) []access.SemanticAttributeDefinition {
	result := append([]access.SemanticAttributeDefinition(nil), definitions...)
	for index := range result {
		if result[index].Name == name {
			change(&result[index])
		}
	}
	return result
}

func requireSemanticAccessError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Fatalf("error = %v, want containing %q", err, contains)
	}
}
