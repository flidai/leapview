package contractversion

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/semanticvalue"
)

func registryTypesFixture() SemanticRegistryTypes {
	return SemanticRegistryTypes{InstanceID: "instance:one", ProjectID: "project:one", ControlRevision: 1, Profile: semanticvalue.Profile, Revision: 2, Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Definitions: []RegisteredSemanticType{
		{ID: "definition:department", Name: "department", Type: semanticvalue.TypeString, Shape: "scalar", Version: 1, Enabled: true},
		{ID: "definition:region", Name: "region", Type: semanticvalue.TypeString, Shape: "scalar", Version: 1, Enabled: true},
	}}
}

func TestClassifierRegistryClonePreservesEmptyDefinitionShape(t *testing.T) {
	for _, definitions := range [][]RegisteredSemanticType{nil, {}} {
		registry := registryTypesFixture()
		registry.Definitions = definitions
		clone := registry.Clone()
		if !reflect.DeepEqual(registry, clone) {
			t.Fatalf("clone changed empty registry representation: before=%#v after=%#v", registry, clone)
		}
	}
}

func TestClassifierRegisteredTypeValidation(t *testing.T) {
	for _, name := range []string{"instance-whitespace", "instance-control", "project", "control-revision", "profile", "registry-revision", "digest", "definition-id", "duplicate-id", "definition-name", "definition-version", "definition-shape", "definition-type", "definition-order"} {
		t.Run(name, func(t *testing.T) {
			registry := registryTypesFixture()
			switch name {
			case "instance-whitespace":
				registry.InstanceID = " instance:one"
			case "instance-control":
				registry.InstanceID = "instance:\x00one"
			case "project":
				registry.ProjectID = ""
			case "control-revision":
				registry.ControlRevision = 0
			case "profile":
				registry.Profile = "other"
			case "registry-revision":
				registry.Revision = 0
			case "digest":
				registry.Digest = "sha256:invalid"
			case "definition-id":
				registry.Definitions[0].ID = "invalid id"
			case "duplicate-id":
				registry.Definitions[1].ID = registry.Definitions[0].ID
			case "definition-name":
				registry.Definitions[0].Name = "invalid name"
			case "definition-version":
				registry.Definitions[0].Version = 0
			case "definition-shape":
				registry.Definitions[0].Shape = "object"
			case "definition-type":
				registry.Definitions[0].Type = "Float"
			case "definition-order":
				registry.Definitions[0], registry.Definitions[1] = registry.Definitions[1], registry.Definitions[0]
			}
			if err := registry.Validate(); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("invalid %s: %v", name, err)
			}
		})
	}
}

func TestClassifierRegisteredTypesDoNotMutateProjectionOrPermitVersionReuse(t *testing.T) {
	base := semanticDocument("1.0.0", []string{"region_access"}, []any{"2026-09-06T10:00:00+02:00"})
	next := semanticDocument("1.0.0", []string{"region_access"}, []any{"2026-09-06T08:00:00Z"})
	beforeBase, beforeNext := bytes.Clone(base), bytes.Clone(next)
	registry := registryTypesFixture()
	registry.Definitions[1].Type = semanticvalue.TypeTimestamp
	if _, err := ValidateVersionTransition(base, next, registry); !errors.Is(err, ErrVersionReuseConflict) {
		t.Fatalf("typed equivalence must not allow rewriting a published version: %v", err)
	}
	if !bytes.Equal(base, beforeBase) || !bytes.Equal(next, beforeNext) {
		t.Fatal("classifier mutated canonical projection bytes")
	}
	clone := registry.Clone()
	clone.Definitions[0].Version++
	if registry.Definitions[0].Version != 1 {
		t.Fatal("cloned type evidence aliases definitions")
	}
}

func TestClassifierRegisteredTypesPreserveSecurityDimensions(t *testing.T) {
	base := semanticDocument("1.0.0", []string{"region_access"}, []any{"emea"})
	next := semanticDocument("1.1.0", []string{"region_access"}, []any{"emea", "amer"})
	registry := registryTypesFixture()
	legacy, err := Classify(base, next)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Classify(base, next, registry)
	if err != nil || !reflect.DeepEqual(got, legacy) || !got.RequiresSecurityApproval {
		t.Fatalf("typed result=%+v err=%v", got, err)
	}
	registry.Definitions[1].Type = semanticvalue.TypeBoolean
	if _, err := Classify(base, next, registry); err == nil {
		t.Fatal("registered Boolean accepted string grant")
	}
	registry.Definitions = registry.Definitions[:1]
	if _, err := Classify(base, next, registry); err == nil {
		t.Fatal("missing registered attribute accepted")
	}
}

func TestClassifierRegisteredTimestampEquivalenceUsesSemanticValue(t *testing.T) {
	base := semanticDocument("1.0.0", []string{"region_access"}, []any{"2026-09-06T10:00:00+02:00"})
	next := semanticDocument("1.1.0", []string{"region_access"}, []any{"2026-09-06T08:00:00Z"})
	registry := registryTypesFixture()
	registry.Definitions[1].Type = semanticvalue.TypeTimestamp
	got, err := Classify(base, next, registry)
	if err != nil || len(got.Changes) != 0 || got.SecurityImpact != SecurityNone {
		t.Fatalf("equivalent timestamps: %+v %v", got, err)
	}
}

func TestClassifierRegisteredValueVocabulary(t *testing.T) {
	for _, tc := range []struct {
		kind          semanticvalue.Type
		literal       string
		before, after any
	}{
		{semanticvalue.TypeString, "String", "e\u0301", "é"},
		{semanticvalue.TypeBoolean, "Boolean", true, true},
		{semanticvalue.TypeInteger, "Number", "2", "2"},
		{semanticvalue.TypeDecimal, "Number", "2.00", "2"},
		{semanticvalue.TypeDate, "String", "2026-09-06", "2026-09-06"},
		{semanticvalue.TypeTimestamp, "String", "2026-09-06T10:00:00+02:00", "2026-09-06T08:00:00Z"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			document := func(version string, value any) []byte {
				return mutateDocument(semanticDocument(version, []string{"region_access"}, []any{"placeholder"}), func(d map[string]any) {
					grant := d["contract"].(map[string]any)["accessGrants"].(map[string]any)["region_access"].(map[string]any)
					grant["allowedValues"] = []any{map[string]any{"type": tc.literal, "value": value}}
				})
			}
			registry := registryTypesFixture()
			registry.Definitions[1].Type = tc.kind
			got, err := Classify(document("1.0.0", tc.before), document("1.1.0", tc.after), registry)
			if err != nil || len(got.Changes) != 0 || got.SecurityImpact != SecurityNone {
				t.Fatalf("equivalent %s values: result=%+v error=%v", tc.kind, got, err)
			}
		})
	}
}

func TestClassifierRegisteredFilterTypeAndLifecycle(t *testing.T) {
	base := semanticDocument("1.0.0", []string{"region_access"}, []any{"us"})
	next := mutateDocument(semanticDocument("1.1.0", []string{"region_access"}, []any{"us"}), func(d map[string]any) {
		contract := d["contract"].(map[string]any)
		contract["dimensions"] = map[string]any{"region": map[string]any{"datatype": "String"}}
		contract["datasets"].(map[string]any)["orders"].(map[string]any)["accessFilters"] = []any{map[string]any{"field": "region", "userAttribute": "region"}}
	})
	registry := registryTypesFixture()
	if _, err := Classify(base, next, registry); err != nil {
		t.Fatal(err)
	}
	wrongType := mutateDocument(next, func(d map[string]any) {
		d["contract"].(map[string]any)["dimensions"].(map[string]any)["region"].(map[string]any)["datatype"] = "Integer"
	})
	if _, err := Classify(base, wrongType, registry); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("filter type mismatch accepted: %v", err)
	}
	registry.Definitions[1].Enabled = false
	if _, err := Classify(base, next, registry); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("disabled candidate attribute accepted: %v", err)
	}
	names, err := ReferencedSemanticAttributes(next)
	if err != nil || !reflect.DeepEqual(names, []string{"department", "region"}) {
		t.Fatalf("referenced type union: %v %v", names, err)
	}
}
