package access

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/project/graph"
)

func TestResourceRefIdentityIsIndependentOfDescriptiveMetadata(t *testing.T) {
	id, err := graph.NewResourceID("model_orders")
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewResourceRef(id, graph.KindModel)
	if err != nil {
		t.Fatal(err)
	}
	resource := graph.Resource{
		ID: id, Kind: graph.KindModel, Name: "orders",
		Metadata:   graph.Metadata{DisplayName: "Orders", Domain: "commerce"},
		Provenance: graph.Provenance{Path: "models/orders.yaml", Origin: "project"},
	}
	second, err := NewResourceRef(resource.ID, resource.Kind)
	if err != nil {
		t.Fatal(err)
	}
	resource.Metadata.DisplayName = "Order facts"
	resource.Metadata.Domain = "finance"
	resource.Provenance.Path = "archive/orders.yaml"
	if first.CanonicalID() != second.CanonicalID() {
		t.Fatalf("descriptive metadata changed canonical identity: %#v %#v", first, second)
	}
	if first.CanonicalID() != "model_orders" {
		t.Fatalf("CanonicalID() = %q, want globally unique resource ID", first.CanonicalID())
	}

	otherKind, err := NewResourceRef(id, graph.KindSource)
	if err != nil {
		t.Fatal(err)
	}
	if otherKind.CanonicalID() != first.CanonicalID() {
		t.Fatal("kind became part of canonical resource identity")
	}
}

func TestCanonicalGrantHasNoDomainOrHierarchyIdentity(t *testing.T) {
	project := canonicalTestProject(t)
	resource, err := NewResourceRef("dashboard_main", graph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := NewSubjectRef(SubjectKindPrincipal, "alice")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := NewCanonicalGrant(project, subject, resource, CapabilityResourceRead)
	if err != nil {
		t.Fatal(err)
	}
	if grant.GrantKey() == "" || grant.Resource().CanonicalID() != resource.CanonicalID() {
		t.Fatalf("grant key = %q, resource key = %q", grant.GrantKey(), resource.CanonicalID())
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"domain", "parent", "path", "displayName", "workspace"} {
		if containsJSONField(string(encoded), forbidden) {
			t.Fatalf("canonical grant JSON contains forbidden identity field %q: %s", forbidden, encoded)
		}
	}

	decoded, err := DecodeCanonicalGrant(encoded, project)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.GrantKey() != grant.GrantKey() {
		t.Fatalf("grant key changed after JSON round trip: %q != %q", decoded.GrantKey(), grant.GrantKey())
	}
}

func TestCanonicalGrantKeysIncludeSubjectKind(t *testing.T) {
	project := canonicalTestProject(t)
	resource, err := NewResourceRef("dashboard_main", graph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := NewSubjectRef(SubjectKindPrincipal, "shared-id")
	if err != nil {
		t.Fatal(err)
	}
	group, err := NewSubjectRef(SubjectKindGroup, "shared-id")
	if err != nil {
		t.Fatal(err)
	}
	principalGrant, err := NewCanonicalGrant(project, principal, resource, CapabilityResourceRead)
	if err != nil {
		t.Fatal(err)
	}
	groupGrant, err := NewCanonicalGrant(project, group, resource, CapabilityResourceRead)
	if err != nil {
		t.Fatal(err)
	}
	if principalGrant.GrantKey() == groupGrant.GrantKey() {
		t.Fatal("principal and group grants collided in canonical key")
	}
}

func TestCanonicalCapabilitiesRoundTripAndMatrixIsDefensive(t *testing.T) {
	want := []Capability{
		CapabilityProjectAdmin,
		CapabilityResourceUse,
		CapabilityResourceRead,
		CapabilityResourceEdit,
		CapabilityResourceManage,
		CapabilityResourceShare,
		CapabilityResourcePublish,
	}
	for _, capability := range want {
		parsed, err := ParseCapability(string(capability))
		if err != nil || parsed != capability {
			t.Fatalf("ParseCapability(%q) = %q, %v", capability, parsed, err)
		}
		wire, err := json.Marshal(capability)
		if err != nil {
			t.Fatal(err)
		}
		var roundTripped Capability
		if err := json.Unmarshal(wire, &roundTripped); err != nil || roundTripped != capability {
			t.Fatalf("Capability JSON round trip = %q, %v", roundTripped, err)
		}
	}
	for _, invalid := range []string{"", "RESOURCE_DELETE", "resource.read", " USE_RESOURCE"} {
		if _, err := ParseCapability(invalid); !errors.Is(err, ErrInvalidCapability) {
			t.Fatalf("ParseCapability(%q) error = %v, want invalid capability", invalid, err)
		}
	}

	first := CanonicalCapabilities()
	second := CanonicalCapabilities()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("canonical capabilities are not deterministic: %#v != %#v", first, second)
	}
	first[0] = CapabilityResourcePublish
	third := CanonicalCapabilities()
	if third[0] == CapabilityResourcePublish {
		t.Fatal("canonical capabilities leaked mutable caller state")
	}
	for _, kind := range []graph.Kind{graph.KindProject, graph.KindConnection, graph.KindSource, graph.KindModel, graph.KindSemanticModel, graph.KindPipeline, graph.KindDashboard} {
		got := CapabilitiesForKind(kind)
		if len(got) == 0 {
			t.Fatalf("CapabilitiesForKind(%q) is empty", kind)
		}
		got[0] = CapabilityResourcePublish
		if CapabilitiesForKind(kind)[0] == CapabilityResourcePublish && kind != graph.KindDashboard {
			t.Fatalf("CapabilitiesForKind(%q) leaked mutable caller state", kind)
		}
	}
}

func TestCanonicalCapabilityMatrixEnforcesKinds(t *testing.T) {
	for _, kind := range []graph.Kind{
		graph.KindConnection, graph.KindSource, graph.KindModel,
		graph.KindSemanticModel, graph.KindPipeline,
	} {
		if !SupportsCapability(kind, CapabilityResourceRead) {
			t.Errorf("kind %q does not support resource read", kind)
		}
		for _, capability := range []Capability{CapabilityResourceShare, CapabilityResourcePublish, CapabilityProjectAdmin} {
			if SupportsCapability(kind, capability) {
				t.Errorf("kind %q unexpectedly supports %q", kind, capability)
			}
		}
	}
	if !SupportsCapability(graph.KindDashboard, CapabilityResourceShare) || !SupportsCapability(graph.KindDashboard, CapabilityResourcePublish) {
		t.Fatal("dashboard does not support sharing and publishing")
	}
	if !SupportsCapability(graph.KindProject, CapabilityProjectAdmin) {
		t.Fatal("project does not support project admin")
	}
	for _, capability := range []Capability{
		CapabilityResourceUse, CapabilityResourceRead, CapabilityResourceEdit,
		CapabilityResourceManage, CapabilityResourceShare, CapabilityResourcePublish,
	} {
		if SupportsCapability(graph.KindProject, capability) {
			t.Errorf("project unexpectedly supports resource capability %q", capability)
		}
	}
}

func TestCanonicalValidationRejectsInvalidInputs(t *testing.T) {
	project := canonicalTestProject(t)
	if _, err := NewResourceRef("models/orders", graph.KindModel); !errors.Is(err, graph.ErrInvalidResourceID) {
		t.Fatalf("invalid resource ID error = %v", err)
	}
	if _, err := NewResourceRef("model_orders", graph.Kind("table")); !errors.Is(err, graph.ErrInvalidKind) {
		t.Fatalf("invalid resource kind error = %v", err)
	}
	resource, err := NewResourceRef("model_orders", graph.KindModel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCanonicalGrant(project, SubjectRef{}, resource, CapabilityResourceRead); !errors.Is(err, ErrInvalidCanonicalGrant) {
		t.Fatalf("empty subject error = %v", err)
	}
	subject, err := NewSubjectRef(SubjectKindPrincipal, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCanonicalGrant(project, subject, resource, CapabilityResourceShare); !errors.Is(err, ErrCapabilityNotAllowed) {
		t.Fatalf("unsupported capability error = %v", err)
	}
	if _, err := NewCanonicalGrant(project, subject, resource, Capability("RESOURCE_DELETE")); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("invalid capability error = %v", err)
	}
}

func TestCanonicalReferencesMustMatchAuthoritativeGraph(t *testing.T) {
	project, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "project_demo", Kind: graph.KindProject, Name: "demo"},
		{ID: "model_orders", Kind: graph.KindModel, Name: "orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wrongKind, err := NewResourceRef("model_orders", graph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(wrongKind.ValidateAgainst(project), ErrResourceKindMismatch) {
		t.Fatalf("wrong-kind reference validation = %v", wrongKind.ValidateAgainst(project))
	}
	subject, err := NewSubjectRef(SubjectKindPrincipal, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCanonicalGrant(project, subject, wrongKind, CapabilityResourcePublish); !errors.Is(err, ErrResourceKindMismatch) {
		t.Fatalf("wrong-kind grant construction = %v", err)
	}
}

func TestCanonicalJSONRejectsDuplicateAndTrailingValues(t *testing.T) {
	var resource ResourceRef
	if err := json.Unmarshal([]byte(`{"id":"model_orders","id":"model_orders","kind":"model"}`), &resource); err == nil {
		t.Fatal("ResourceRef JSON accepted duplicate ID")
	}
	if err := json.Unmarshal([]byte(`{"id":"model_orders","kind":"model"}{}`), &resource); err == nil {
		t.Fatal("ResourceRef JSON accepted trailing value")
	}
	if err := json.Unmarshal([]byte(`{"id":"model_orders","kind":"model","domain":"finance"}`), &resource); err == nil {
		t.Fatal("ResourceRef JSON accepted unknown descriptive field")
	}
	var grant CanonicalGrant
	duplicateGrant := `{"subject":{"kind":"principal","id":"alice"},"resource":{"id":"dashboard_main","kind":"dashboard"},"resource":{"id":"dashboard_main","kind":"dashboard"},"capability":"RESOURCE_READ"}`
	if _, err := DecodeCanonicalGrant([]byte(duplicateGrant), canonicalTestProject(t)); err == nil {
		t.Fatal("CanonicalGrant JSON accepted duplicate resource")
	}
	unknownGrant := `{"subject":{"kind":"principal","id":"alice"},"resource":{"id":"dashboard_main","kind":"dashboard"},"capability":"RESOURCE_READ","domain":"finance"}`
	if _, err := DecodeCanonicalGrant([]byte(unknownGrant), canonicalTestProject(t)); err == nil {
		t.Fatal("CanonicalGrant JSON accepted unknown domain field")
	}
	invalidGrant := `{"subject":{"kind":"principal","id":"alice"},"resource":{"id":"model_orders","kind":"model"},"capability":"RESOURCE_SHARE"}`
	if _, err := DecodeCanonicalGrant([]byte(invalidGrant), canonicalTestProject(t)); !errors.Is(err, ErrCapabilityNotAllowed) {
		t.Fatalf("CanonicalGrant JSON error = %v, want unsupported capability", err)
	}
	if err := json.Unmarshal([]byte(`{"subject":{"kind":"principal","id":"alice"},"resource":{"id":"dashboard_main","kind":"dashboard"},"capability":"RESOURCE_READ"}`), &grant); !errors.Is(err, ErrUnboundCanonicalGrant) {
		t.Fatalf("graph-free CanonicalGrant JSON error = %v, want unbound grant", err)
	}
	if _, err := DecodeCanonicalGrant([]byte(`{"subject":{"kind":"principal","id":"alice"},"resource":{"id":"dashboard_main","ID":"model_orders","kind":"dashboard"},"capability":"RESOURCE_READ"}`), canonicalTestProject(t)); err == nil {
		t.Fatal("CanonicalGrant JSON accepted case-folded duplicate resource ID")
	}
}

func TestCanonicalGrantCannotProduceAuthorizationKeyWithoutGraphBinding(t *testing.T) {
	unbound := CanonicalGrant{}
	if key := unbound.GrantKey(); key != "" {
		t.Fatalf("unbound grant produced authorization key %q", key)
	}
	if _, err := json.Marshal(unbound); !errors.Is(err, ErrUnboundCanonicalGrant) {
		t.Fatalf("unbound grant marshal error = %v, want unbound grant", err)
	}
}

func canonicalTestProject(t *testing.T) graph.ProjectGraph {
	t.Helper()
	project, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "project_demo", Kind: graph.KindProject, Name: "demo"},
		{ID: "dashboard_main", Kind: graph.KindDashboard, Name: "main"},
		{ID: "model_orders", Kind: graph.KindModel, Name: "orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func containsJSONField(encoded, field string) bool {
	return len(encoded) > 0 && (encoded == field || strings.Contains(encoded, `"`+field+`"`))
}
