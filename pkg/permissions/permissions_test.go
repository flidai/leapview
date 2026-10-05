package permissions

import (
	"errors"
	"strings"
	"testing"
)

const testProfile = "leapview.permissions/v1"

func testCatalog(t *testing.T) *CompiledCatalog {
	t.Helper()
	definitions := []Definition{
		{Action: "semantic.consume", Scope: ScopeResource, ResourceKinds: []Kind{"semantic_model"}, CheckKinds: []Kind{"semantic_model"}},
		{Action: "semantic.query", Scope: ScopeResource, ResourceKinds: []Kind{"semantic_model"}, CheckKinds: []Kind{"semantic_model"}, Prerequisites: []Action{"semantic.consume"}},
		{Action: "dashboard.create", Scope: ScopeProject, ResourceKinds: []Kind{"dashboard"}, CheckKinds: []Kind{"project"}},
		{Action: "platform.audit", Scope: ScopeInstance},
	}
	catalog, err := CompileCatalog(testProfile, definitions)
	if err != nil {
		t.Fatalf("CompileCatalog() error = %v", err)
	}
	return catalog
}

func TestCatalogIsOpaqueAndDefensive(t *testing.T) {
	catalog := testCatalog(t)
	if got := catalog.Profile(); got != testProfile {
		t.Fatalf("Profile() = %q, want %q", got, testProfile)
	}
	if !Kind("vendor_resource").Valid() || Kind(" vendor_resource").Valid() {
		t.Fatal("Kind.Valid did not enforce canonical opaque syntax")
	}
	definitions := catalog.Definitions()
	definitions[0].Action = "forged.action"
	definitions[0].ResourceKinds[0] = "forged"
	definition, ok := catalog.Definition("semantic.consume")
	if !ok || definition.Action != "semantic.consume" || definition.ResourceKinds[0] != "semantic_model" {
		t.Fatalf("catalog was mutable through defensive copy: %#v", definition)
	}
}

func TestCatalogRejectsMalformedAndUnsatisfiedDefinitions(t *testing.T) {
	base := Definition{Action: "sample.read", Scope: ScopeResource, ResourceKinds: []Kind{"sample"}, CheckKinds: []Kind{"sample"}}
	cases := []struct {
		name string
		defs []Definition
	}{
		{"empty action", []Definition{{Scope: ScopeResource, ResourceKinds: []Kind{"sample"}, CheckKinds: []Kind{"sample"}}}},
		{"duplicate action", []Definition{base, base}},
		{"invalid kind", []Definition{{Action: "sample.read", Scope: ScopeResource, ResourceKinds: []Kind{"bad kind"}, CheckKinds: []Kind{"sample"}}}},
		{"unknown prerequisite", []Definition{{Action: "sample.read", Scope: ScopeResource, ResourceKinds: []Kind{"sample"}, CheckKinds: []Kind{"sample"}, Prerequisites: []Action{"missing.read"}}}},
		{"unsatisfied prerequisite", []Definition{
			{Action: "sample.read", Scope: ScopeResource, ResourceKinds: []Kind{"sample"}, CheckKinds: []Kind{"sample"}, Prerequisites: []Action{"project.read"}},
			{Action: "project.read", Scope: ScopeProject, ResourceKinds: []Kind{"project"}, CheckKinds: []Kind{"project"}},
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateCatalog(test.defs); !errors.Is(err, ErrInvalidCatalog) {
				t.Fatalf("ValidateCatalog() error = %v, want ErrInvalidCatalog", err)
			}
		})
	}

	cycleA := Definition{Action: "sample.one", Scope: ScopeResource, ResourceKinds: []Kind{"sample"}, CheckKinds: []Kind{"sample"}, Prerequisites: []Action{"sample.two"}}
	cycleB := Definition{Action: "sample.two", Scope: ScopeResource, ResourceKinds: []Kind{"sample"}, CheckKinds: []Kind{"sample"}, Prerequisites: []Action{"sample.one"}}
	if err := ValidateCatalog([]Definition{cycleA, cycleB}); !errors.Is(err, ErrInvalidCatalog) || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle validation error = %v", err)
	}
	projectParent := Definition{Action: "sample.create", Scope: ScopeProject, ResourceKinds: []Kind{"sample"}, CheckKinds: []Kind{"project"}, Prerequisites: []Action{"project.audit"}}
	projectPrerequisite := Definition{Action: "project.audit", Scope: ScopeProject, ResourceKinds: []Kind{"other"}, CheckKinds: []Kind{"project"}}
	if err := ValidateCatalog([]Definition{projectParent, projectPrerequisite}); err != nil {
		t.Fatalf("project prerequisites with different affected kinds were rejected: %v", err)
	}
}

func TestShapeAndCatalogValidationSeparateUnknownActions(t *testing.T) {
	pair := Pair{Action: "future.read", Profile: testProfile, Target: Target{Scope: ScopeResource, ProjectID: "project_a", ResourceKind: "vendor_resource", ResourceID: "resource_a"}}
	if err := pair.ValidateShape(); err != nil {
		t.Fatalf("ValidateShape() error = %v", err)
	}
	catalog := testCatalog(t)
	if err := catalog.ValidatePair(pair); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("unknown action error = %v", err)
	}
	if err := (Pair{Action: "future.read", Profile: testProfile}).ValidateShape(); !errors.Is(err, ErrInvalidPair) {
		t.Fatalf("missing target error = %v", err)
	}
	if err := ValidatePairSetShape(nil); !errors.Is(err, ErrPermissionPairsRequired) {
		t.Fatalf("nil pair set error = %v", err)
	}
	if err := ValidatePairSetShape(PairSet{}); err != nil {
		t.Fatalf("explicit empty pair set error = %v", err)
	}
	if err := ValidatePairSetShape(PairSet{pair, pair}); !errors.Is(err, ErrInvalidPair) {
		t.Fatalf("duplicate pair-set error = %v", err)
	}
	wrongProfile := pair
	wrongProfile.Action = "semantic.consume"
	wrongProfile.Target.ResourceKind = "semantic_model"
	wrongProfile.Profile = "leapview.permissions/v999"
	if err := catalog.ValidatePair(wrongProfile); !errors.Is(err, ErrInvalidPair) {
		t.Fatalf("profile mismatch error = %v", err)
	}
}

func TestResourceIdentityShapeMatchesOpaqueProjectGraphContract(t *testing.T) {
	longID := "project_" + strings.Repeat("a", 300)
	pair, err := NewProjectPair(testProfile, "dashboard.create", longID)
	if err != nil {
		t.Fatalf("opaque resource id was given a new length limit: %v", err)
	}
	if pair.Target.ProjectID != longID {
		t.Fatalf("opaque resource id changed: %q", pair.Target.ProjectID)
	}
	if _, err := NewProjectPair(testProfile, "dashboard.create", "project with spaces"); !errors.Is(err, ErrInvalidPair) {
		t.Fatalf("invalid resource id error = %v", err)
	}
}

func TestExactFutureClosureContainmentAndIntersection(t *testing.T) {
	catalog := testCatalog(t)
	if _, err := catalog.NewExactPair("semantic.consume", "project_a", Kind("dashboard"), "dashboard_a"); !errors.Is(err, ErrInvalidPair) {
		t.Fatalf("catalog constructor accepted relabeled kind: %v", err)
	}
	query, err := catalog.NewExactPair("semantic.query", "project_a", Kind("semantic_model"), "semantic_a")
	if err != nil {
		t.Fatal(err)
	}
	consume, err := catalog.NewExactPair("semantic.consume", "project_a", Kind("semantic_model"), "semantic_a")
	if err != nil {
		t.Fatal(err)
	}
	required, err := catalog.Required(query)
	if err != nil || len(required) != 2 || required[0].Action != query.Action || required[1].Action != consume.Action {
		t.Fatalf("Required() = %#v, %v", required, err)
	}
	if catalog.Contains(PairSet{query}, PairSet{query}) {
		t.Fatal("query prerequisite was silently implied")
	}
	if !catalog.Contains(PairSet{query, consume}, PairSet{query}) {
		t.Fatal("independently granted prerequisite was not accepted")
	}

	future, err := catalog.NewFuturePair("semantic.consume", "project_a", Kind("semantic_model"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := catalog.NewExactPair("semantic.consume", "project_b", Kind("semantic_model"), "semantic_a")
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.Allows(future, consume) || catalog.Allows(future, other) || catalog.Allows(consume, future) {
		t.Fatal("future/exact matching crossed project or widened a future selector")
	}

	update, err := catalog.NewExactPair("semantic.consume", "project_a", Kind("semantic_model"), "semantic_b")
	if err != nil {
		t.Fatal(err)
	}
	if got := catalog.Intersect(PairSet{consume, update}, PairSet{consume, other}); len(got) != 1 || got[0] != consume {
		t.Fatalf("Intersect() = %#v, want exact paired consume", got)
	}
}

func TestCatalogJSONIsStrictAndWireCompatible(t *testing.T) {
	catalog := testCatalog(t)
	pair, err := catalog.NewExactPair("semantic.consume", "project_a", Kind("semantic_model"), "semantic_a")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := catalog.Encode(PairSet{pair})
	if err != nil {
		t.Fatal(err)
	}
	want := `[ {"action":"semantic.consume","target":{"scope":"resource","projectId":"project_a","resourceKind":"semantic_model","resourceId":"semantic_a"},"profile":"leapview.permissions/v1"} ]`
	if strings.ReplaceAll(string(encoded), " ", "") != strings.ReplaceAll(want, " ", "") {
		t.Fatalf("wire JSON = %s, want %s", encoded, want)
	}
	decoded, err := catalog.Decode(encoded)
	if err != nil || len(decoded) != 1 || decoded[0] != pair {
		t.Fatalf("Decode() = %#v, %v", decoded, err)
	}
	for _, forged := range []string{
		`[{"action":"semantic.consume","target":{"scope":"resource","projectId":"project_a","resourceKind":"semantic_model","resourceId":"semantic_a","unknown":true},"profile":"leapview.permissions/v1"}]`,
		`[{"action":null,"target":{"scope":"resource","projectId":"project_a","resourceKind":"semantic_model","resourceId":"semantic_a"},"profile":"leapview.permissions/v1"}]`,
		`[{"action":"semantic.consume","target":null,"profile":"leapview.permissions/v1"}]`,
		`[{"action":"semantic.consume","target":{"scope":"resource","instanceId":null,"projectId":"project_a","resourceKind":"semantic_model","resourceId":"semantic_a"},"profile":"leapview.permissions/v1"}]`,
		`[{"action":"semantic.consume","target":{"scope":"resource","projectId":"project_a","resourceKind":"semantic_model","resourceId":"semantic_a"},"profile":"leapview.permissions/v1"}] {"trailing":true}`,
		`null`,
	} {
		if _, err := catalog.Decode([]byte(forged)); err == nil {
			t.Fatalf("Decode(%s) accepted forged JSON", forged)
		}
	}
	if _, err := catalog.Decode([]byte(`[{"action":"semantic.consume","action":"semantic.consume","target":{"scope":"resource","projectId":"project_a","resourceKind":"semantic_model","resourceId":"semantic_a"},"profile":"leapview.permissions/v1"}]`)); err == nil {
		t.Fatal("Decode accepted duplicate object key")
	}
	project := testCatalog(t)
	if decoded, err := project.Decode([]byte(`[{"action":"dashboard.create","target":{"scope":"project","projectId":"project_a","includeFuture":false},"profile":"leapview.permissions/v1"}]`)); err != nil || len(decoded) != 1 || decoded[0].Target.IncludeFuture {
		t.Fatalf("Decode explicit false optional field = %#v, %v", decoded, err)
	}
}

func FuzzCatalogDecodeRoundTripsWithoutWidening(f *testing.F) {
	catalog, err := CompileCatalog(testProfile, []Definition{
		{Action: "semantic.consume", Scope: ScopeResource, ResourceKinds: []Kind{"semantic_model"}, CheckKinds: []Kind{"semantic_model"}},
	})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{
		`[{"action":"semantic.consume","target":{"scope":"resource","projectId":"project_a","resourceKind":"semantic_model","resourceId":"semantic_a"},"profile":"leapview.permissions/v1"}]`,
		`[]`,
		`null`,
		`[{"action":"semantic.consume","target":{"scope":"resource"},"profile":"leapview.permissions/v1"}]`,
		`[{"unknown":true}]`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, encoded []byte) {
		decoded, err := catalog.Decode(encoded)
		if err != nil {
			return
		}
		canonical, err := catalog.Encode(decoded)
		if err != nil {
			t.Fatalf("Encode(valid decoded pairs) error = %v", err)
		}
		roundTrip, err := catalog.Decode(canonical)
		if err != nil || len(roundTrip) != len(decoded) {
			t.Fatalf("Decode(canonical) = %#v, %v", roundTrip, err)
		}
		for index := range decoded {
			if roundTrip[index] != decoded[index] {
				t.Fatalf("pair %d widened or changed: %#v != %#v", index, roundTrip[index], decoded[index])
			}
		}
		reencoded, err := catalog.Encode(roundTrip)
		if err != nil || string(reencoded) != string(canonical) {
			t.Fatalf("canonical encoding is nondeterministic: %q != %q (%v)", reencoded, canonical, err)
		}
	})
}
