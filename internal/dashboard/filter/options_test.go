package filter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalizeStaticOptionsUsesTypedOrderAndRejectsConflictingLabels(t *testing.T) {
	options, err := CanonicalizeStaticOptions([]Option{
		{Value: Value{Kind: ValueInteger, Value: "10"}, Label: "ten"},
		{Value: Value{Kind: ValueInteger, Value: "2"}, Label: "two"},
		{Value: Value{Kind: ValueInteger, Value: "02"}, Label: "two"},
	}, ValueInteger)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 2 || options[0].Value.Value != "2" || options[1].Value.Value != "10" {
		t.Fatalf("canonical static options = %#v", options)
	}
	_, err = CanonicalizeStaticOptions([]Option{{Value: Value{Kind: ValueString, Value: "CA"}, Label: "California"}, {Value: Value{Kind: ValueString, Value: "CA"}, Label: "CA"}}, ValueString)
	if err == nil {
		t.Fatal("conflicting duplicate labels were accepted")
	}
}

func TestCanonicalOptionItemsOrdersNullLastBeforeLimit(t *testing.T) {
	items, err := canonicalOptionItems([]OptionItem{
		{Value: Value{Kind: ValueInteger, Value: "10"}},
		{Null: true},
		{Value: Value{Kind: ValueInteger, Value: "2"}},
	}, ValueInteger)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Value.Value != "2" || items[1].Value.Value != "10" || !items[2].Null {
		t.Fatalf("canonical option items = %#v", items)
	}
}

func TestNullOptionWireShapeOmitsFabricatedTypedValue(t *testing.T) {
	encoded, err := json.Marshal(OptionItem{Null: true, Label: "(null)", Available: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"value"`) || !strings.Contains(string(encoded), `"null":true`) {
		t.Fatalf("null option wire shape = %s", encoded)
	}
}

func TestOptionEngineUsesIncomingDependenciesAndExcludesSelf(t *testing.T) {
	var captured OptionQuery
	engine := NewOptionEngine([]byte("01234567890123456789012345678901"), func(_ context.Context, query OptionQuery) (OptionResult, error) {
		captured = query
		return OptionResult{Items: []OptionItem{{Value: Value{Kind: ValueString, Value: "WA"}, Label: "Washington"}}, Complete: true}, nil
	})
	state := State{
		Revision: 4,
		AppliedControls: map[string]AppliedState{
			"fb_state":  {Expression: Expression{Kind: ExpressionSet, Operator: OperatorIn, Values: []Value{{Kind: ValueString, Value: "WA"}}}},
			"fb_year":   {Expression: Expression{Kind: ExpressionComparison, Operator: OperatorEquals, Value: &Value{Kind: ValueInteger, Value: "2026"}}},
			"fb_region": {Expression: Expression{Kind: ExpressionSet, Operator: OperatorIn, Values: []Value{{Kind: ValueString, Value: "west"}}}},
		},
	}
	page, err := engine.Page(context.Background(), OptionContext{
		ServingStateID: "ss-1", PolicyIdentity: "policy-1", State: state,
		Binding:          Binding{Key: "fb_state", Filter: "state", OptionDependencies: []BindingRef{{Scope: ScopePage, ID: "year"}}},
		Definition:       Definition{ValueKind: ValueString, Options: OptionSource{Kind: OptionSourceDistinct}},
		BindingKeysByRef: map[BindingRef]string{{Scope: ScopePage, ID: "year"}: "fb_year"},
	}, OptionRequest{BindingKey: "fb_state", FilterRevision: 4, ServingStateID: "ss-1", Limit: 20, RequestGeneration: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.Dependencies) != 1 || captured.Dependencies["fb_year"].Kind != ExpressionComparison {
		t.Fatalf("dependencies = %#v", captured.Dependencies)
	}
	if _, exists := captured.Dependencies["fb_state"]; exists {
		t.Fatal("option query included the target binding itself")
	}
	if len(page.Items) != 1 || !page.Items[0].Selected || !page.Items[0].Available {
		t.Fatalf("option page = %#v", page)
	}
	if page.ConsumerIdentity != "option:fb_state" {
		t.Fatalf("consumer identity = %q, want option:fb_state", page.ConsumerIdentity)
	}
}

func TestOptionEngineRetainsSelectedUnavailableValues(t *testing.T) {
	engine := NewOptionEngine([]byte("01234567890123456789012345678901"), func(context.Context, OptionQuery) (OptionResult, error) {
		return OptionResult{Complete: true}, nil
	})
	state := State{Revision: 8, AppliedControls: map[string]AppliedState{
		"fb_state": {Expression: Expression{Kind: ExpressionSet, Operator: OperatorIn, Values: []Value{{Kind: ValueString, Value: "OR"}}}},
	}}
	page, err := engine.Page(context.Background(), OptionContext{
		ServingStateID: "ss", PolicyIdentity: "policy", State: state,
		Binding: Binding{Key: "fb_state"}, Definition: Definition{ValueKind: ValueString, Options: OptionSource{Kind: OptionSourceDistinct}},
	}, OptionRequest{BindingKey: "fb_state", FilterRevision: 8, ServingStateID: "ss", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Available || !page.Items[0].Selected {
		t.Fatalf("unavailable selection = %#v", page.Items)
	}
}

func TestOptionEngineRejectsStaleRequestAndTamperedCursor(t *testing.T) {
	engine := NewOptionEngine([]byte("01234567890123456789012345678901"), func(context.Context, OptionQuery) (OptionResult, error) {
		return OptionResult{}, nil
	})
	optionContext := OptionContext{
		ServingStateID: "ss", PolicyIdentity: "policy", State: State{Revision: 2, AppliedControls: map[string]AppliedState{}},
		Binding: Binding{Key: "fb"}, Definition: Definition{ValueKind: ValueString, Options: OptionSource{Kind: OptionSourceDistinct}},
	}
	if _, err := engine.Page(context.Background(), optionContext, OptionRequest{BindingKey: "fb", ServingStateID: "ss", FilterRevision: 1, Limit: 20}); err == nil {
		t.Fatal("stale revision was accepted")
	}
	if _, err := engine.Page(context.Background(), optionContext, OptionRequest{BindingKey: "fb", ServingStateID: "ss", FilterRevision: 2, Limit: 20, Cursor: "tampered"}); err == nil {
		t.Fatal("tampered cursor was accepted")
	}
}

func TestOptionEngineSharesBoundedCacheAcrossRequests(t *testing.T) {
	cache := NewOptionCache(8)
	calls := 0
	query := func(context.Context, OptionQuery) (OptionResult, error) {
		calls++
		return OptionResult{Items: []OptionItem{{Value: Value{Kind: ValueString, Value: "WA"}}}, Complete: true}, nil
	}
	optionContext := OptionContext{
		ServingStateID: "ss", PolicyIdentity: "policy",
		State: State{Revision: 2, AppliedControls: map[string]AppliedState{"fb": {
			Expression: Expression{Kind: ExpressionUnfiltered},
		}}},
		Binding: Binding{Key: "fb"},
		Definition: Definition{
			ValueKind: ValueString, Options: OptionSource{Kind: OptionSourceDistinct},
		},
	}
	request := OptionRequest{BindingKey: "fb", ServingStateID: "ss", FilterRevision: 2, Limit: 20}
	for range 2 {
		engine := NewOptionEngineWithCache([]byte("01234567890123456789012345678901"), cache, query)
		if _, err := engine.Page(context.Background(), optionContext, request); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("query calls = %d, want one shared-cache load", calls)
	}
}

func TestOptionEngineReusesCacheAcrossUnrelatedFilterRevisions(t *testing.T) {
	cache := NewOptionCache(8)
	calls := 0
	query := func(context.Context, OptionQuery) (OptionResult, error) {
		calls++
		return OptionResult{Items: []OptionItem{{Value: Value{Kind: ValueString, Value: "WA"}}}, Complete: true}, nil
	}
	optionContext := OptionContext{
		ServingStateID: "ss", PolicyIdentity: "policy",
		State: State{Revision: 2, AppliedControls: map[string]AppliedState{
			"fb_year": {Expression: Expression{Kind: ExpressionComparison, Operator: OperatorEquals, Value: &Value{Kind: ValueInteger, Value: "2025"}}},
		}},
		Binding:          Binding{Key: "fb", OptionDependencies: []BindingRef{{Scope: ScopePage, ID: "year"}}},
		BindingKeysByRef: map[BindingRef]string{{Scope: ScopePage, ID: "year"}: "fb_year"},
		Definition: Definition{
			ValueKind: ValueString, Options: OptionSource{Kind: OptionSourceDistinct},
		},
	}
	engine := NewOptionEngineWithCache([]byte("01234567890123456789012345678901"), cache, query)
	if _, err := engine.Page(context.Background(), optionContext, OptionRequest{
		BindingKey: "fb", ServingStateID: "ss", FilterRevision: 2, Limit: 20,
	}); err != nil {
		t.Fatal(err)
	}
	optionContext.State.Revision = 3
	if _, err := engine.Page(context.Background(), optionContext, OptionRequest{
		BindingKey: "fb", ServingStateID: "ss", FilterRevision: 3, Limit: 20,
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("query calls = %d, want one load when effective dependencies are unchanged", calls)
	}
	optionContext.State.Revision = 4
	optionContext.State.AppliedControls["fb_year"] = AppliedState{Expression: Expression{
		Kind: ExpressionComparison, Operator: OperatorEquals, Value: &Value{Kind: ValueInteger, Value: "2026"},
	}}
	if _, err := engine.Page(context.Background(), optionContext, OptionRequest{
		BindingKey: "fb", ServingStateID: "ss", FilterRevision: 4, Limit: 20,
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("query calls = %d, want a new load when an effective dependency changes", calls)
	}
}
