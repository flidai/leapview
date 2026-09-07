package query

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestCompileSemanticAccessPolicyQualifiesTypesAndIsDeterministic(t *testing.T) {
	model := semanticAccessTestModel(t)
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	definitions := semanticAccessDefinitions()
	first, err := CompileSemanticAccessPolicy("instance-1", "semantic-model:sales", "generation-9", model, compiled, semanticAccessRegistry(definitions))
	if err != nil {
		t.Fatal(err)
	}
	second, err := CompileSemanticAccessPolicy("instance-1", "semantic-model:sales", "generation-9", model, compiled, semanticAccessRegistry(reverseDefinitions(definitions)))
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() != second.Digest() || !reflect.DeepEqual(first.Canonical(), second.Canonical()) {
		t.Fatalf("compiled policy depends on registry ordering: %s != %s", first.Digest(), second.Digest())
	}
	account, ok := first.Grant("canViewAccount")
	if !ok || account.Type != semanticvalue.TypeInteger || account.Shape != access.SemanticAttributeList || len(account.AllowedValues) != 1 || account.AllowedValues[0] != "9007199254740993" {
		t.Fatalf("compiled exact-number grant = %#v", account)
	}
	wantGrantTypes := map[string]semanticvalue.Type{"canViewSales": semanticvalue.TypeString, "canViewAccount": semanticvalue.TypeInteger,
		"canViewDate": semanticvalue.TypeDate, "decimalGate": semanticvalue.TypeDecimal, "booleanGate": semanticvalue.TypeBoolean, "timestampGate": semanticvalue.TypeTimestamp}
	for name, wantType := range wantGrantTypes {
		grant, present := first.Grant(name)
		if !present || grant.Type != wantType || len(grant.AllowedValues) == 0 || grant.AllowedValuesDigest == "" {
			t.Fatalf("grant %q = %#v, want type %q", name, grant, wantType)
		}
	}
	dataset, ok := first.Dataset("orders")
	if !ok || len(dataset.AccessFilters) != 6 || dataset.RequiredAccessGrants[0] != "canViewSales" {
		t.Fatalf("compiled dataset policy = %#v", dataset)
	}
	wantTypes := map[string]semanticvalue.Type{"account": semanticvalue.TypeInteger, "amount": semanticvalue.TypeDecimal, "approved": semanticvalue.TypeBoolean, "occurredAt": semanticvalue.TypeTimestamp, "orderDate": semanticvalue.TypeDate, "region": semanticvalue.TypeString}
	for _, filter := range dataset.AccessFilters {
		if filter.Type != wantTypes[filter.Dimension] || filter.Field == "" {
			t.Fatalf("compiled filter = %#v", filter)
		}
	}
}

func TestCompileSemanticAccessPolicyDigestBindsTargetAndAuthority(t *testing.T) {
	model := semanticAccessTestModel(t)
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	base, err := CompileSemanticAccessPolicy("instance-1", "semantic-model:sales", "generation-9", model, compiled, semanticAccessRegistry(semanticAccessDefinitions()))
	if err != nil {
		t.Fatal(err)
	}
	otherTarget, err := CompileSemanticAccessPolicy("instance-2", "semantic-model:sales", "generation-9", model, compiled, semanticAccessRegistry(semanticAccessDefinitions()))
	if err != nil {
		t.Fatal(err)
	}
	otherGeneration, err := CompileSemanticAccessPolicy("instance-1", "semantic-model:sales", "generation-10", model, compiled, semanticAccessRegistry(semanticAccessDefinitions()))
	if err != nil {
		t.Fatal(err)
	}
	registry := semanticAccessRegistry(semanticAccessDefinitions())
	registry.State.Revision++
	registry.Definitions = replaceDefinition(registry.Definitions, "regions", func(value *access.SemanticAttributeDefinition) { value.Metadata.DisplayName = "changed" })
	registry.State.Digest, _ = access.SemanticAttributeRegistryDigest(registry.State.Profile, registry.Definitions)
	otherRegistry, err := CompileSemanticAccessPolicy("instance-1", "semantic-model:sales", "generation-9", model, compiled, registry)
	if err != nil {
		t.Fatal(err)
	}
	for name, policy := range map[string]*CompiledSemanticAccessPolicy{"target": otherTarget, "generation": otherGeneration, "registry": otherRegistry} {
		if policy.Digest() == base.Digest() || reflect.DeepEqual(policy.Canonical(), base.Canonical()) {
			t.Fatalf("%s identity is not bound into compiled policy", name)
		}
	}
}

func TestCompileSemanticAccessPolicyPropagatesRequirementsTransitively(t *testing.T) {
	policy, _ := compileSemanticAccessTestPolicy(t)
	dimension, ok := policy.Dimension("region", "orders")
	if !ok || !reflect.DeepEqual(dimension.RequiredAccessGrants, []string{"canViewAccount", "canViewSales"}) {
		t.Fatalf("dimension requirements = %#v", dimension)
	}
	revenue, ok := policy.Metric("revenue")
	if !ok || !reflect.DeepEqual(revenue.RequiredAccessGrants, []string{"canViewAccount", "canViewSales"}) || !reflect.DeepEqual(revenue.Datasets, []string{"orders"}) {
		t.Fatalf("aggregate requirements = %#v", revenue)
	}
	doubled, ok := policy.Metric("doubledRevenue")
	if !ok || !reflect.DeepEqual(doubled.RequiredAccessGrants, []string{"canViewAccount", "canViewDate", "canViewSales"}) || !reflect.DeepEqual(doubled.Datasets, []string{"orders"}) {
		t.Fatalf("derived requirements = %#v", doubled)
	}
	ratio, ok := policy.Metric("averageRevenue")
	if !ok || !reflect.DeepEqual(ratio.RequiredAccessGrants, []string{"canViewAccount", "canViewSales"}) {
		t.Fatalf("ratio requirements = %#v", ratio)
	}
}

func TestCompileSemanticAccessPolicyPropagatesFilteredDimensionRequirements(t *testing.T) {
	model := semanticAccessTestModel(t)
	delete(model.AccessPolicy.Metrics, "revenue")
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"western": {Field: "orders.region", Operator: "equals", Value: "west"},
	}
	revenue := model.Metrics["revenue"]
	revenue.Where = []string{"western"}
	model.Metrics["revenue"] = revenue
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := CompileSemanticAccessPolicy("instance-1", "semantic-model:sales", "generation-9", model, compiled, semanticAccessRegistry(semanticAccessDefinitions()))
	if err != nil {
		t.Fatal(err)
	}
	metric, _ := policy.Metric("revenue")
	if !reflect.DeepEqual(metric.RequiredAccessGrants, []string{"canViewAccount", "canViewSales"}) {
		t.Fatalf("filtered metric requirements = %#v", metric.RequiredAccessGrants)
	}
}

func TestCompileSemanticAccessPolicyRetainsRelationshipDatasetClosure(t *testing.T) {
	model := testModel()
	populateFixtureTableModelNames(model)
	state := model.Dimensions["customer_state"]
	state.Bindings["customers"] = semanticmodel.DimensionBinding{Field: "customers.state"}
	model.Dimensions["customer_state"] = state
	literal, err := semanticmodel.NewSemanticAccessLiteral("support")
	if err != nil {
		t.Fatal(err)
	}
	model.AccessPolicy = semanticmodel.SemanticAccessPolicy{
		AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{"customerScope": {UserAttribute: "department", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal}}},
		Datasets: map[string]semanticmodel.SemanticDatasetAccessSpec{"customers": {
			RequiredAccessGrants: []string{"customerScope"}, AccessFilters: []semanticmodel.SemanticAccessFilterSpec{{Field: "customer_state", UserAttribute: "regions"}},
		}},
	}
	definitions := []access.SemanticAttributeDefinition{
		{ID: "def-department", Name: "department", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile,
			DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true, Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}}},
		{ID: "def-regions", Name: "regions", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeList, Profile: semanticvalue.Profile,
			DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true, Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}}},
	}
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := CompileSemanticAccessPolicy("instance-1", "semantic-model:commerce", "generation-1", model, compiled, semanticAccessRegistry(definitions))
	if err != nil {
		t.Fatal(err)
	}
	member, ok := policy.Dimension("customer_state", "orders")
	if !ok || !reflect.DeepEqual(member.Datasets, []string{"customers", "orders"}) || !reflect.DeepEqual(member.RequiredAccessGrants, []string{"customerScope"}) {
		t.Fatalf("relationship-bound dimension access = %#v", member)
	}
}

func TestCompileSemanticAccessPolicyRejectsUnknownDisabledAndIncompatibleAttributes(t *testing.T) {
	cases := []struct {
		name        string
		definitions func([]access.SemanticAttributeDefinition) []access.SemanticAttributeDefinition
		change      func(*semanticmodel.Model)
		want        string
	}{
		{name: "unknown", definitions: func(values []access.SemanticAttributeDefinition) []access.SemanticAttributeDefinition {
			return values[:len(values)-1]
		}, want: "unknown attribute"},
		{name: "disabled", definitions: func(values []access.SemanticAttributeDefinition) []access.SemanticAttributeDefinition {
			return replaceDefinition(values, "regions", func(value *access.SemanticAttributeDefinition) {
				value.Enabled = false
				value.LifecycleState = access.SemanticAttributeDisabled
				value.DisabledAt = "2026-09-06T12:00:00Z"
			})
		}, want: "is disabled"},
		{name: "incompatible", definitions: func(values []access.SemanticAttributeDefinition) []access.SemanticAttributeDefinition {
			return replaceDefinition(values, "regions", func(value *access.SemanticAttributeDefinition) { value.Type = semanticvalue.TypeBoolean })
		}, want: "incompatible"},
		{name: "invalid ownership", definitions: func(values []access.SemanticAttributeDefinition) []access.SemanticAttributeDefinition {
			return replaceDefinition(values, "regions", func(value *access.SemanticAttributeDefinition) {
				value.Metadata.Owner.Kind = access.SemanticAttributeOwnerPrincipal
				value.Metadata.Owner.ID = ""
			})
		}, want: "owner is invalid"},
		{name: "invalid instance ownership", definitions: func(values []access.SemanticAttributeDefinition) []access.SemanticAttributeDefinition {
			return replaceDefinition(values, "regions", func(value *access.SemanticAttributeDefinition) { value.Metadata.Owner.ID = "instance-1" })
		}, want: "owner is invalid"},
		{name: "unknown grant", definitions: func(values []access.SemanticAttributeDefinition) []access.SemanticAttributeDefinition { return values }, change: func(model *semanticmodel.Model) {
			model.AccessPolicy.Datasets["orders"] = semanticmodel.SemanticDatasetAccessSpec{RequiredAccessGrants: []string{"missing"}}
		}, want: "unknown access grant"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			model := semanticAccessTestModel(t)
			if test.change != nil {
				test.change(model)
			}
			compiled, err := CompileModel(model)
			if err != nil {
				t.Fatal(err)
			}
			_, err = CompileSemanticAccessPolicy("instance-1", "semantic-model:sales", "generation-9", model, compiled, semanticAccessRegistry(test.definitions(semanticAccessDefinitions())))
			requireSemanticAccessError(t, err, test.want)
		})
	}
}

func TestCompileSemanticAccessPolicyRejectsUnsafeTypeCoercionAndIndirectFilter(t *testing.T) {
	model := semanticAccessTestModel(t)
	grant := model.AccessPolicy.AccessGrants["canViewAccount"]
	grant.AllowedValues = []semanticmodel.SemanticAccessLiteral{{Kind: semanticmodel.SemanticAccessString, Text: "9007199254740993"}}
	model.AccessPolicy.AccessGrants["canViewAccount"] = grant
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	_, err = CompileSemanticAccessPolicy("instance-1", "semantic-model:sales", "generation-9", model, compiled, semanticAccessRegistry(semanticAccessDefinitions()))
	requireSemanticAccessError(t, err, "not an integer")

	model = semanticAccessTestModel(t)
	dimension := model.Dimensions["region"]
	dimension.Bindings["orders"] = semanticmodel.DimensionBinding{Field: "orders.region", Path: []string{"invented"}}
	model.Dimensions["region"] = dimension
	if _, err := CompileModel(model); err == nil {
		// The semantic compiler itself must reject an unsafe route before the
		// access compiler can ever create a predicate from it.
		t.Fatal("indirect invalid binding unexpectedly compiled")
	}
}

func TestCompileSemanticAccessPolicyRejectsCanonicalDuplicateGrantValues(t *testing.T) {
	model := semanticAccessTestModel(t)
	grant := model.AccessPolicy.AccessGrants["decimalGate"]
	grant.AllowedValues = []semanticmodel.SemanticAccessLiteral{
		{Kind: semanticmodel.SemanticAccessNumber, Text: "1"},
		{Kind: semanticmodel.SemanticAccessNumber, Text: "1.0"},
	}
	model.AccessPolicy.AccessGrants["decimalGate"] = grant
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	_, err = CompileSemanticAccessPolicy("instance-1", "semantic-model:sales", "generation-9", model, compiled, semanticAccessRegistry(semanticAccessDefinitions()))
	requireSemanticAccessError(t, err, "canonical duplicates")
}

func TestCompileSemanticAccessPolicyRejectsStaleLineageAndMalformedRegistryIdentity(t *testing.T) {
	model := semanticAccessTestModel(t)
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	mutated := *model
	mutated.AccessPolicy = model.AccessPolicy
	mutated.AccessPolicy.AccessGrants = make(map[string]semanticmodel.SemanticAccessGrantSpec, len(model.AccessPolicy.AccessGrants))
	for name, grant := range model.AccessPolicy.AccessGrants {
		mutated.AccessPolicy.AccessGrants[name] = grant
	}
	grant := mutated.AccessPolicy.AccessGrants["canViewSales"]
	grant.AllowedValues = []semanticmodel.SemanticAccessLiteral{{Kind: semanticmodel.SemanticAccessString, Text: "engineering"}}
	mutated.AccessPolicy.AccessGrants["canViewSales"] = grant
	_, err = CompileSemanticAccessPolicy("instance-1", "semantic-model:sales", "generation-9", &mutated, compiled, semanticAccessRegistry(semanticAccessDefinitions()))
	requireSemanticAccessError(t, err, "inconsistent")

	registry := semanticAccessRegistry(semanticAccessDefinitions())
	registry.State.Digest = "not-a-digest"
	_, err = CompileSemanticAccessPolicy("instance-1", "semantic-model:sales", "generation-9", model, compiled, registry)
	requireSemanticAccessError(t, err, "registry state identity")
}

func TestSemanticAccessLiteralRoundTripPreservesExactNumber(t *testing.T) {
	literal, err := semanticmodel.NewSemanticAccessLiteral(json.Number("9007199254740993.1250"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(literal)
	if err != nil {
		t.Fatal(err)
	}
	var decoded semanticmodel.SemanticAccessLiteral
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	value, err := decoded.Value()
	if err != nil {
		t.Fatal(err)
	}
	if number, ok := value.(json.Number); !ok || number.String() != "9007199254740993.1250" {
		t.Fatalf("round-trip value = %#v", value)
	}
}

func reverseDefinitions(values []access.SemanticAttributeDefinition) []access.SemanticAttributeDefinition {
	result := append([]access.SemanticAttributeDefinition(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}
