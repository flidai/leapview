package query

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func semanticAccessDefinition(name, id string, typeName semanticvalue.Type, shape access.SemanticAttributeShape) access.SemanticAttributeDefinition {
	return access.SemanticAttributeDefinition{
		ID: id, Name: name, Type: typeName, Shape: shape, Profile: semanticvalue.Profile,
		DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true,
	}
}

func semanticAccessRegistry(definitions ...access.SemanticAttributeDefinition) access.SemanticAttributeRegistrySnapshot {
	return access.SemanticAttributeRegistrySnapshot{
		State:       access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 7, Digest: "sha256:registry"},
		Definitions: definitions,
	}
}

func semanticAccessAttribute(t testing.TB, definition access.SemanticAttributeDefinition, input any) access.EffectiveSemanticAttribute {
	t.Helper()
	values, digest, err := access.CanonicalSemanticAttributeValues(definition, input)
	if err != nil {
		t.Fatalf("canonical semantic attribute value: %v", err)
	}
	return access.EffectiveSemanticAttribute{
		DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion,
		Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: digest, Source: "direct",
	}
}

func semanticAccessContext(registry access.SemanticAttributeRegistrySnapshot, attributes ...access.EffectiveSemanticAttribute) SemanticAccessEvaluationContext {
	return SemanticAccessEvaluationContext{
		RegistryState: registry.State,
		ControlState:  access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 11, Digest: "sha256:control"},
		Attributes:    attributes,
	}
}

func protectedSemanticAccessModel(grants map[string]semanticmodel.SemanticAccessGrantSpec, required ...string) *semanticmodel.Model {
	model := testModel()
	populateFixtureTableModelNames(model)
	model.AccessGrants = grants
	orders := model.Datasets["orders"]
	orders.RequiredAccessGrants = append([]string(nil), required...)
	model.Datasets["orders"] = orders
	return model
}

func TestSemanticAccessOrdinaryCompileRequiresRegistryContext(t *testing.T) {
	model := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}},
	}, "region_grant")
	if _, err := CompileModel(model); err == nil {
		t.Fatal("CompileModel accepted a protected model without registry context")
	}
	unprotected := testModel()
	populateFixtureTableModelNames(unprotected)
	if _, err := CompileModel(unprotected); err != nil {
		t.Fatalf("unprotected CompileModel changed behavior: %v", err)
	}
	emptyCompatible := testModel()
	populateFixtureTableModelNames(emptyCompatible)
	emptyCompatible.AccessGrants = map[string]semanticmodel.SemanticAccessGrantSpec{}
	orders := emptyCompatible.Datasets["orders"]
	orders.AccessFilters = []semanticmodel.SemanticAccessFilterSpec{}
	emptyCompatible.Datasets["orders"] = orders
	if _, err := CompileModel(emptyCompatible); err != nil {
		t.Fatalf("CompileModel rejected generated-contract-compatible empty policy containers: %v", err)
	}
	compiled, err := CompileModelWithSemanticAccess(emptyCompatible, SemanticAccessCompileContext{Registry: semanticAccessRegistry()})
	if err != nil {
		t.Fatalf("context-aware compile rejected an unprotected model: %v", err)
	}
	decision, err := compiled.SemanticAccessPolicy().EvaluateDataset("orders", SemanticAccessEvaluationContext{})
	if err != nil || !decision.Allowed {
		t.Fatalf("unprotected dataset evaluation changed behavior: decision=%+v err=%v", decision, err)
	}
}

func TestSemanticAccessScalarListAndRequirementsAreANDed(t *testing.T) {
	region := semanticAccessDefinition("region", "def-region", semanticvalue.TypeString, access.SemanticAttributeScalar)
	roles := semanticAccessDefinition("roles", "def-roles", semanticvalue.TypeString, access.SemanticAttributeList)
	registry := semanticAccessRegistry(region, roles)
	model := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}},
		"role_grant":   {UserAttribute: "roles", AllowedValues: []any{"analyst"}},
	}, "region_grant", "role_grant")
	compiled, err := CompileModelWithSemanticAccess(model, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	policy := compiled.SemanticAccessPolicy()
	context := semanticAccessContext(registry, semanticAccessAttribute(t, region, "us"), semanticAccessAttribute(t, roles, []any{"viewer", "analyst"}))
	decision, err := policy.EvaluateDataset("orders", context)
	if err != nil || !decision.Allowed {
		t.Fatalf("matching scalar/list grants denied: decision=%+v err=%v", decision, err)
	}
	denied := semanticAccessContext(registry, semanticAccessAttribute(t, region, "eu"), semanticAccessAttribute(t, roles, []any{"analyst"}))
	decision, err = policy.EvaluateDataset("orders", denied)
	if err != nil || decision.Allowed || len(decision.GrantOutcomes) != 2 || decision.GrantOutcomes[0].Satisfied || !decision.GrantOutcomes[1].Satisfied {
		t.Fatalf("multiple required grants did not use logical AND: decision=%+v err=%v", decision, err)
	}
	context.Attributes[0].CanonicalValues[0] = "eu"
	context.Attributes[0].ValueDigest = "tampered"
	decision, err = policy.EvaluateDataset("orders", context)
	if err != nil || decision.Allowed || len(decision.GrantOutcomes) != 0 {
		t.Fatalf("invalid or conflicting grant input was allowed: decision=%+v err=%v", decision, err)
	}
}

func TestSemanticAccessStateDigestLifecycleAndBoundFailClosed(t *testing.T) {
	region := semanticAccessDefinition("region", "def-region", semanticvalue.TypeString, access.SemanticAttributeScalar)
	registry := semanticAccessRegistry(region)
	model := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}},
	}, "region_grant")
	compiled, err := CompileModelWithSemanticAccess(model, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	policy := compiled.SemanticAccessPolicy()
	attribute := semanticAccessAttribute(t, region, "us")
	attribute.ValueDigest = "sha256:tampered"
	decision, err := policy.EvaluateDataset("orders", semanticAccessContext(registry, attribute))
	if err != nil || decision.Allowed {
		t.Fatalf("digest-tampered effective value was allowed: decision=%+v err=%v", decision, err)
	}
	untrusted := semanticAccessAttribute(t, region, "us")
	untrusted.Source = "request"
	decision, err = policy.EvaluateDataset("orders", semanticAccessContext(registry, untrusted))
	if err != nil || decision.Allowed {
		t.Fatalf("untrusted effective value was allowed: decision=%+v err=%v", decision, err)
	}
	valid := semanticAccessAttribute(t, region, "us")
	decision, err = policy.EvaluateDataset("orders", semanticAccessContext(registry, valid, valid))
	if err != nil || decision.Allowed {
		t.Fatalf("duplicate effective sources were allowed: decision=%+v err=%v", decision, err)
	}
	context := semanticAccessContext(registry, semanticAccessAttribute(t, region, "us"))
	context.RegistryState.Digest = "sha256:other-registry"
	decision, err = policy.EvaluateDataset("orders", context)
	if err != nil || decision.Allowed {
		t.Fatalf("registry mismatch was allowed: decision=%+v err=%v", decision, err)
	}
	context = semanticAccessContext(registry, semanticAccessAttribute(t, region, "us"))
	context.ControlState = access.SemanticAttributeControlState{}
	decision, err = policy.EvaluateDataset("orders", context)
	if err != nil || decision.Allowed {
		t.Fatalf("missing control state was allowed: decision=%+v err=%v", decision, err)
	}
	listRegion := semanticAccessDefinition("regions", "def-regions", semanticvalue.TypeString, access.SemanticAttributeList)
	listRegistry := semanticAccessRegistry(listRegion)
	listModel := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"region_grant": {UserAttribute: "regions", AllowedValues: []any{"us"}},
	}, "region_grant")
	listCompiled, err := CompileModelWithSemanticAccess(listModel, SemanticAccessCompileContext{Registry: listRegistry})
	if err != nil {
		t.Fatal(err)
	}
	tooMany := make([]string, semanticvalue.MaxSetValues+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("v%04d", index)
	}
	oversized := access.EffectiveSemanticAttribute{
		DefinitionID: listRegion.ID, DefinitionName: listRegion.Name, DefinitionVersion: listRegion.DefinitionVersion,
		Type: listRegion.Type, Shape: listRegion.Shape, CanonicalValues: tooMany, ValueDigest: "sha256:oversized", Source: "direct",
	}
	decision, err = listCompiled.SemanticAccessPolicy().EvaluateDataset("orders", semanticAccessContext(listRegistry, oversized))
	if err != nil || decision.Allowed {
		t.Fatalf("oversized effective value was allowed: decision=%+v err=%v", decision, err)
	}
	disabled := region
	disabled.Enabled = false
	disabled.LifecycleState = access.SemanticAttributeDisabled
	if _, err := CompileSemanticAccessPolicy(model, semanticAccessRegistry(disabled)); err == nil {
		t.Fatal("compile accepted a disabled grant definition")
	}
}

func TestSemanticAccessListFiltersComposeAndRemainBoundLiterals(t *testing.T) {
	regions := semanticAccessDefinition("regions", "def-regions", semanticvalue.TypeString, access.SemanticAttributeList)
	department := semanticAccessDefinition("department", "def-department", semanticvalue.TypeString, access.SemanticAttributeScalar)
	registry := semanticAccessRegistry(regions, department)
	model := protectedSemanticAccessModel(nil)
	ordersTable := model.Tables["orders"]
	status := ordersTable.Dimensions["status"]
	status.Field = "orders.status"
	ordersTable.Dimensions["status"] = status
	model.Tables["orders"] = ordersTable
	model.Dimensions["order_status"] = semanticmodel.SemanticDimension{
		Type: "string", Datatype: semanticmodel.DataTypeString,
		Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}},
	}
	orders := model.Datasets["orders"]
	orders.AccessFilters = []semanticmodel.SemanticAccessFilterSpec{
		{Field: "order_status", UserAttribute: "department"},
		{Field: "customer_state", UserAttribute: "regions"},
	}
	model.Datasets["orders"] = orders

	compiled, err := CompileModelWithSemanticAccess(model, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	malicious := "sales' OR 1=1 --"
	context := semanticAccessContext(registry,
		semanticAccessAttribute(t, regions, []any{"west", "east", "west"}),
		semanticAccessAttribute(t, department, malicious),
	)
	decision, err := compiled.SemanticAccessPolicy().EvaluateDataset("orders", context)
	if err != nil || !decision.Allowed || len(decision.Predicates) != 2 {
		t.Fatalf("AND filter decision = %+v err=%v", decision, err)
	}
	if predicate := decision.Predicates[0]; predicate.Kind != planir.PredicateIn || predicate.Field != "customers.state" || len(predicate.Values) != 2 || predicate.Values[0].String != "east" || predicate.Values[1].String != "west" {
		t.Fatalf("list predicate = %#v", predicate)
	}
	if predicate := decision.Predicates[1]; predicate.Kind != planir.PredicateCompare || predicate.Field != "orders.status" || predicate.Value.String != malicious {
		t.Fatalf("scalar predicate = %#v", predicate)
	}

	decision, err = compiled.SemanticAccessPolicy().EvaluateDataset("orders", semanticAccessContext(registry, semanticAccessAttribute(t, regions, []any{"east"})))
	if err != nil || decision.Allowed || len(decision.Predicates) != 0 || len(decision.AppliedFilters) != 0 {
		t.Fatalf("missing second AND filter returned a usable partial decision: %+v err=%v", decision, err)
	}
}

func TestSemanticAccessCompilationRejectsInvalidRegistryBindingsAndValues(t *testing.T) {
	stringDefinition := semanticAccessDefinition("region", "def-region", semanticvalue.TypeString, access.SemanticAttributeScalar)
	integerDefinition := semanticAccessDefinition("region", "def-region", semanticvalue.TypeInteger, access.SemanticAttributeScalar)
	departmentDefinition := semanticAccessDefinition("department", "def-department", semanticvalue.TypeString, access.SemanticAttributeScalar)

	unknown := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"region_grant": {UserAttribute: "missing", AllowedValues: []any{"us"}},
	}, "region_grant")
	if _, err := CompileSemanticAccessPolicy(unknown, semanticAccessRegistry(stringDefinition)); err == nil {
		t.Fatal("compile accepted an unknown grant attribute")
	}

	typeMismatch := protectedSemanticAccessModel(nil)
	orders := typeMismatch.Datasets["orders"]
	orders.AccessFilters = []semanticmodel.SemanticAccessFilterSpec{{Field: "customer_state", UserAttribute: "region"}}
	typeMismatch.Datasets["orders"] = orders
	if _, err := CompileSemanticAccessPolicy(typeMismatch, semanticAccessRegistry(integerDefinition)); err == nil {
		t.Fatal("compile accepted an access-filter type mismatch")
	}

	sharedField := protectedSemanticAccessModel(nil)
	orders = sharedField.Datasets["orders"]
	orders.AccessFilters = []semanticmodel.SemanticAccessFilterSpec{
		{Field: "customer_state", UserAttribute: "region"},
		{Field: "customer_state", UserAttribute: "department"},
	}
	sharedField.Datasets["orders"] = orders
	policy, err := CompileSemanticAccessPolicy(sharedField, semanticAccessRegistry(stringDefinition, departmentDefinition))
	if err != nil || len(policy.Filters("orders")) != 2 {
		t.Fatalf("compile rejected distinct AND filters on one field: policy=%#v err=%v", policy, err)
	}
	orders.AccessFilters = append(orders.AccessFilters, orders.AccessFilters[0])
	sharedField.Datasets["orders"] = orders
	if _, err := CompileSemanticAccessPolicy(sharedField, semanticAccessRegistry(stringDefinition, departmentDefinition)); err == nil {
		t.Fatal("compile accepted an exact duplicate access filter")
	}

	tooMany := make([]any, semanticvalue.MaxSetValues+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("v%04d", index)
	}
	oversized := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"region_grant": {UserAttribute: "region", AllowedValues: tooMany},
	}, "region_grant")
	if _, err := CompileSemanticAccessPolicy(oversized, semanticAccessRegistry(stringDefinition)); err == nil {
		t.Fatal("compile accepted more than 1024 normalized grant values")
	}

	emptyRequirements := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}},
	})
	orders = emptyRequirements.Datasets["orders"]
	orders.RequiredAccessGrants = []string{}
	emptyRequirements.Datasets["orders"] = orders
	if _, err := CompileSemanticAccessPolicy(emptyRequirements, semanticAccessRegistry(stringDefinition)); err == nil {
		t.Fatal("compile accepted an explicitly empty required access grant list")
	}
}

func TestSemanticAccessNoImplicitBypassAndRedactedGrantOutcomes(t *testing.T) {
	region := semanticAccessDefinition("region", "def-region", semanticvalue.TypeString, access.SemanticAttributeScalar)
	registry := semanticAccessRegistry(region)
	model := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}},
	}, "region_grant")
	compiled, err := CompileModelWithSemanticAccess(model, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := compiled.SemanticAccessPolicy().EvaluateDataset("orders", semanticAccessContext(registry))
	if err != nil || decision.Allowed || len(decision.GrantOutcomes) != 1 {
		t.Fatalf("missing attribute received an implicit bypass: decision=%+v err=%v", decision, err)
	}
	outcome := decision.GrantOutcomes[0]
	if outcome.Grant != "region_grant" || outcome.Satisfied || outcome.AttributeDefinitionID != region.ID || outcome.AttributeDefinitionVersion != region.DefinitionVersion {
		t.Fatalf("redacted grant outcome = %+v", outcome)
	}
}

func TestSemanticAccessExactDecimalGrantAndTypedFilterLiteral(t *testing.T) {
	const decimal = "9007199254740993.125"
	threshold := semanticAccessDefinition("threshold", "def-threshold", semanticvalue.TypeDecimal, access.SemanticAttributeScalar)
	registry := semanticAccessRegistry(threshold)
	model := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"threshold_grant": {UserAttribute: "threshold", AllowedValues: []any{json.Number(decimal)}},
	}, "threshold_grant")
	model.Dimensions["order_revenue"] = semanticmodel.SemanticDimension{
		Type: "number", Datatype: semanticmodel.DataTypeDecimal,
		Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.revenue"}},
	}
	orders := model.Datasets["orders"]
	orders.AccessFilters = []semanticmodel.SemanticAccessFilterSpec{{Field: "order_revenue", UserAttribute: "threshold"}}
	model.Datasets["orders"] = orders

	compiled, err := CompileModelWithSemanticAccess(model, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	policy := compiled.SemanticAccessPolicy()
	grant, ok := policy.Grant("threshold_grant")
	if !ok || len(grant.AllowedValues) != 1 || grant.AllowedValues[0] != decimal {
		t.Fatalf("compiled exact decimal grant = %#v", grant)
	}
	decision, err := policy.EvaluateDataset("orders", semanticAccessContext(registry, semanticAccessAttribute(t, threshold, decimal)))
	if err != nil || !decision.Allowed || len(decision.Predicates) != 1 {
		t.Fatalf("decimal decision = %+v err=%v", decision, err)
	}
	literal := decision.Predicates[0].Value
	if literal.Kind != planir.LiteralNumber || literal.NumberKind != planir.NumberDecimal || literal.NumberText != decimal {
		t.Fatalf("decimal predicate literal = %#v", literal)
	}
}

func TestSemanticAccessTransitiveFilterRouteAndPhysicalPredicate(t *testing.T) {
	region := semanticAccessDefinition("region", "def-region", semanticvalue.TypeString, access.SemanticAttributeScalar)
	registry := semanticAccessRegistry(region)
	model := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}},
		"tag_grant":    {UserAttribute: "region", AllowedValues: []any{"us"}},
	}, "region_grant")
	orders := model.Datasets["orders"]
	orders.AccessFilters = []semanticmodel.SemanticAccessFilterSpec{{Field: "customer_state", UserAttribute: "region"}}
	model.Datasets["orders"] = orders
	dimension := model.Dimensions["customer_state"]
	dimension.RequiredAccessGrants = []string{"region_grant"}
	model.Dimensions["customer_state"] = dimension
	orderCount := model.Metrics["order_count"]
	orderCount.RequiredAccessGrants = []string{"region_grant"}
	model.Metrics["order_count"] = orderCount
	tagCount := model.Metrics["tag_count"]
	tagCount.RequiredAccessGrants = []string{"tag_grant"}
	model.Metrics["tag_count"] = tagCount
	compiled, err := CompileModelWithSemanticAccess(model, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	policy := compiled.SemanticAccessPolicy()
	requirements, ok := policy.MetricRequirements("tags_per_order")
	if !ok || !reflect.DeepEqual(requirements.Grants, []string{"region_grant", "tag_grant"}) {
		t.Fatalf("transitive metric requirements = %#v", requirements.Grants)
	}
	filters := policy.Filters("orders")
	if len(filters) != 1 || filters[0].PhysicalField != "customers.state" || len(filters[0].Route) != 1 || len(filters[0].Route[0].Edges) != 1 || filters[0].Route[0].Edges[0].Name != "orders_customers" {
		t.Fatalf("compiled access filter binding = %#v", filters)
	}
	decision, err := policy.EvaluateDataset("orders", semanticAccessContext(registry, semanticAccessAttribute(t, region, "us")))
	if err != nil || !decision.Allowed || len(decision.Predicates) != 1 || decision.Predicates[0].Kind != planir.PredicateCompare || decision.Predicates[0].Field != "customers.state" || len(decision.AppliedFilters) != 1 || decision.AppliedFilters[0].Identity != "orders.customer_state@region" || decision.AppliedFilters[0].AttributeDefinitionID != region.ID {
		t.Fatalf("filter decision = %+v err=%v", decision, err)
	}
}

func TestSemanticAccessReversesOneToOneFilterRoute(t *testing.T) {
	region := semanticAccessDefinition("region", "def-region", semanticvalue.TypeString, access.SemanticAttributeScalar)
	registry := semanticAccessRegistry(region)
	model := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}},
	}, "region_grant")
	model.Datasets["profiles"] = semanticmodel.SemanticDatasetSpec{Model: "profiles", AccessFilters: []semanticmodel.SemanticAccessFilterSpec{{Field: "customer_state", UserAttribute: "region"}}}
	model.Tables["profiles"] = semanticmodel.Table{
		GrainEntity: "profile",
		Entities: map[string]semanticmodel.EntityDefinition{
			"profile": {Type: "primary", Fields: []string{"profile_customer_id"}},
		},
		Dimensions: map[string]semanticmodel.MetricDimension{
			"profile_customer_id": {Datatype: semanticmodel.DataTypeInteger},
		},
	}
	model.Relationships = append(model.Relationships, semanticmodel.Relationship{
		ID: "customers_profiles", FromDataset: "customers", FromFields: []string{"customer_id"},
		ToDataset: "profiles", ToFields: []string{"profile_customer_id"}, Cardinality: "one_to_one",
	})
	customerState := model.Dimensions["customer_state"]
	customerState.Bindings["profiles"] = semanticmodel.DimensionBinding{Field: "customers.state", Path: []string{"customers_profiles"}}
	model.Dimensions["customer_state"] = customerState
	populateFixtureTableModelNames(model)

	compiled, err := CompileModelWithSemanticAccess(model, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	filters := compiled.SemanticAccessPolicy().Filters("profiles")
	if len(filters) != 1 || len(filters[0].Route) != 1 || len(filters[0].Route[0].Edges) != 1 {
		t.Fatalf("compiled one-to-one access filter route = %#v", filters)
	}
	edge := filters[0].Route[0].Edges[0]
	if edge.FromDataset != "profiles" || edge.ToDataset != "customers" || len(edge.JoinKeys) != 1 || edge.JoinKeys[0].From != "profile_customer_id" || edge.JoinKeys[0].To != "customer_id" {
		t.Fatalf("compiled reversed one-to-one route edge = %#v", edge)
	}
}

func TestSemanticAccessIncludesSiblingAliasGrantsAndFilters(t *testing.T) {
	region := semanticAccessDefinition("region", "def-region", semanticvalue.TypeString, access.SemanticAttributeScalar)
	tenant := semanticAccessDefinition("tenant", "def-tenant", semanticvalue.TypeString, access.SemanticAttributeScalar)
	department := semanticAccessDefinition("department", "def-department", semanticvalue.TypeString, access.SemanticAttributeScalar)
	registry := semanticAccessRegistry(region, tenant, department)
	model := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}},
		"tenant_grant": {UserAttribute: "tenant", AllowedValues: []any{"acme"}},
	}, "region_grant")
	model.Datasets["customers_restricted"] = semanticmodel.SemanticDatasetSpec{
		Model: "customers", RequiredAccessGrants: []string{"tenant_grant"},
		AccessFilters: []semanticmodel.SemanticAccessFilterSpec{{Field: "customer_state", UserAttribute: "department"}},
	}
	model.Tables["customers_restricted"] = model.Tables["customers"]
	customerState := model.Dimensions["customer_state"]
	customerState.Bindings["customers_restricted"] = semanticmodel.DimensionBinding{Field: "customers_restricted.state"}
	model.Dimensions["customer_state"] = customerState
	populateFixtureTableModelNames(model)

	compiled, err := CompileModelWithSemanticAccess(model, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	policy := compiled.SemanticAccessPolicy()
	requirements, ok := policy.DimensionRequirements("orders", "customer_state")
	if !ok || !reflect.DeepEqual(requirements.Grants, []string{"region_grant", "tenant_grant"}) {
		t.Fatalf("sibling alias grant requirements = %#v", requirements.Grants)
	}
	context := semanticAccessContext(registry,
		semanticAccessAttribute(t, region, "us"), semanticAccessAttribute(t, tenant, "acme"),
	)
	decision, err := policy.EvaluateDimension("orders", "customer_state", context)
	if err != nil || decision.Allowed {
		t.Fatalf("sibling alias access filter was omitted: decision=%+v err=%v", decision, err)
	}
	if len(policy.Filters("customers_restricted")) != 1 {
		t.Fatalf("sibling alias filters = %#v", policy.Filters("customers_restricted"))
	}
}

func TestSemanticAccessMetricPropagatesCompiledLineageDimensionGrants(t *testing.T) {
	region := semanticAccessDefinition("region", "def-region", semanticvalue.TypeString, access.SemanticAttributeScalar)
	registry := semanticAccessRegistry(region)
	model := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"time_grant":   {UserAttribute: "region", AllowedValues: []any{"us"}},
		"filter_grant": {UserAttribute: "region", AllowedValues: []any{"us"}},
	})
	activityDate := model.Dimensions["activity_date"]
	activityDate.RequiredAccessGrants = []string{"time_grant"}
	model.Dimensions["activity_date"] = activityDate
	customerState := model.Dimensions["customer_state"]
	customerState.RequiredAccessGrants = []string{"filter_grant"}
	model.Dimensions["customer_state"] = customerState
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"customer_state_filter": {Field: "customers.state", Operator: "equals", Value: "us"},
	}
	orderCount := model.Metrics["order_count"]
	orderCount.TimeDimension = "activity_date"
	orderCount.Where = []string{"customer_state_filter"}
	model.Metrics["order_count"] = orderCount

	compiled, err := CompileModelWithSemanticAccess(model, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	policy := compiled.SemanticAccessPolicy()
	requirements, ok := policy.MetricRequirements("order_count")
	if !ok || !reflect.DeepEqual(requirements.Grants, []string{"filter_grant", "time_grant"}) {
		t.Fatalf("compiled metric lineage requirements = %#v", requirements.Grants)
	}
	decision, err := policy.EvaluateMetric("order_count", semanticAccessContext(registry, semanticAccessAttribute(t, region, "us")))
	if err != nil || !decision.Allowed || len(decision.GrantOutcomes) != 2 {
		t.Fatalf("metric lineage grant decision = %+v err=%v", decision, err)
	}
}

func TestSemanticAccessAppliesFiltersAcrossCompiledDimensionAndMetricRoutes(t *testing.T) {
	region := semanticAccessDefinition("region", "def-region", semanticvalue.TypeString, access.SemanticAttributeScalar)
	tenant := semanticAccessDefinition("tenant", "def-tenant", semanticvalue.TypeString, access.SemanticAttributeScalar)
	registry := semanticAccessRegistry(region, tenant)
	model := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{
		"customer_grant": {UserAttribute: "tenant", AllowedValues: []any{"acme"}},
	})
	customerState := model.Dimensions["customer_state"]
	customerState.Bindings["customers"] = semanticmodel.DimensionBinding{Field: "customers.state"}
	model.Dimensions["customer_state"] = customerState
	customers := model.Datasets["customers"]
	customers.RequiredAccessGrants = []string{"customer_grant"}
	customers.AccessFilters = []semanticmodel.SemanticAccessFilterSpec{{Field: "customer_state", UserAttribute: "region"}}
	model.Datasets["customers"] = customers
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"customer_state_filter": {Field: "customers.state", Operator: "equals", Value: "us"},
	}
	orderCount := model.Metrics["order_count"]
	orderCount.Where = []string{"customer_state_filter"}
	model.Metrics["order_count"] = orderCount

	compiled, err := CompileModelWithSemanticAccess(model, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	policy := compiled.SemanticAccessPolicy()
	if requirements, ok := policy.DimensionRequirements("orders", "customer_state"); !ok || !reflect.DeepEqual(requirements.Grants, []string{"customer_grant"}) {
		t.Fatalf("dimension route requirements = %#v", requirements.Grants)
	}
	if requirements, ok := policy.MetricRequirements("order_count"); !ok || !reflect.DeepEqual(requirements.Grants, []string{"customer_grant"}) {
		t.Fatalf("metric route requirements = %#v", requirements.Grants)
	}
	deniedContext := semanticAccessContext(registry, semanticAccessAttribute(t, region, "us"))
	if decision, err := policy.EvaluateDimension("orders", "customer_state", deniedContext); err != nil || decision.Allowed {
		t.Fatalf("dimension route omitted traversed dataset grant: decision=%+v err=%v", decision, err)
	}
	if decision, err := policy.EvaluateMetric("order_count", deniedContext); err != nil || decision.Allowed {
		t.Fatalf("metric route omitted traversed dataset grant: decision=%+v err=%v", decision, err)
	}
	context := semanticAccessContext(registry, semanticAccessAttribute(t, region, "us"), semanticAccessAttribute(t, tenant, "acme"))

	dimensionDecision, err := policy.EvaluateDimension("orders", "customer_state", context)
	if err != nil || !dimensionDecision.Allowed || len(dimensionDecision.Predicates) != 1 || dimensionDecision.Predicates[0].Field != "customers.state" {
		t.Fatalf("dimension route filter decision = %+v err=%v", dimensionDecision, err)
	}
	metricDecision, err := policy.EvaluateMetric("order_count", context)
	if err != nil || !metricDecision.Allowed || len(metricDecision.Predicates) != 1 || metricDecision.Predicates[0].Field != "customers.state" {
		t.Fatalf("metric lineage filter decision = %+v err=%v", metricDecision, err)
	}
}
