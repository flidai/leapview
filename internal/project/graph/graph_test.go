package graph

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func portableResources() []Resource {
	return []Resource{
		{ID: "connection_warehouse", Kind: KindConnection, Name: "warehouse"},
		{ID: "source_orders", Kind: KindSource, Name: "orders"},
		{ID: "model_orders", Kind: KindModel, Name: "orders_model"},
		{ID: "semantic_sales", Kind: KindSemanticModel, Name: "sales"},
		{ID: "pipeline_refresh", Kind: KindPipeline, Name: "refresh"},
		{ID: "dashboard_main", Kind: KindDashboard, Name: "main", Metadata: Metadata{Tags: []string{"insights", "core"}}},
	}
}

func TestNewProjectGraphIsRootlessAndDeterministic(t *testing.T) {
	resources := portableResources()
	edges := []Edge{
		{From: "source_orders", To: "connection_warehouse"},
		{From: "model_orders", To: "source_orders"},
		{From: "semantic_sales", To: "model_orders"},
		{From: "dashboard_main", To: "semantic_sales"},
	}
	first, err := NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewProjectGraph([]Resource{resources[5], resources[1], resources[3], resources[0], resources[4], resources[2]}, []Edge{edges[3], edges[0], edges[2], edges[1]})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.CanonicalBytes(), second.CanonicalBytes()) || first.Digest() != second.Digest() {
		t.Fatalf("canonical graph identity changed with input ordering")
	}
	if got := string(first.CanonicalBytes()); !strings.HasPrefix(got, `{"version":2,"resources":[`) || strings.Contains(got, `"project"`) {
		t.Fatalf("canonical graph = %s, want version 2 without project identity", got)
	}
	if first.Validate() != nil || len(first.Resources()) != 6 {
		t.Fatalf("rootless graph failed validation: %#v", first.Resources())
	}
	if _, ok := first.Resource("project_uid"); ok {
		t.Fatal("graph unexpectedly exposed a project resource")
	}
}

func TestResourceIDDoesNotDependOnMetadataPathOrProvenance(t *testing.T) {
	base := Resource{
		ID: "model_orders", Kind: KindModel, Name: "orders",
		Metadata:   Metadata{DisplayName: "Orders", Owner: "team-data", Domain: "commerce"},
		Provenance: Provenance{Origin: "project", Path: "models/orders.yaml", Source: "git:abc"},
	}
	changed := base
	changed.Metadata.DisplayName = "Order facts"
	changed.Metadata.Owner = "team-analytics"
	changed.Metadata.Domain = "finance"
	changed.Provenance = Provenance{Origin: "agent", Path: "archive/orders.yml", Source: "builder"}
	first, err := NewProjectGraph([]Resource{base}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewProjectGraph([]Resource{changed}, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstResource, firstOK := first.Resource(base.ID)
	secondResource, secondOK := second.Resource(changed.ID)
	if base.ID != changed.ID || !firstOK || !secondOK || firstResource.ID != changed.ID || secondResource.ID != base.ID {
		t.Fatalf("metadata/path/provenance changed resource ID: %q != %q", base.ID, changed.ID)
	}
}

func TestResourceIDsAreOpaqueAndNamesRemainSymbolic(t *testing.T) {
	for _, value := range []string{"01J8N3YQ6F7T8V9W0X1Y2Z3A4B", "semantic_model:orders", "7f3a.orders-v2"} {
		if id, err := NewResourceID(value); err != nil || id.String() != value {
			t.Fatalf("NewResourceID(%q) = %q, %v", value, id, err)
		}
	}
	for _, value := range []string{"orders/view", " semantic_model:orders", "semantic model:orders"} {
		if _, err := NewResourceID(value); !errors.Is(err, ErrInvalidResourceID) {
			t.Fatalf("NewResourceID(%q) error = %v, want malformed ID", value, err)
		}
	}
	if _, err := NewProjectGraph([]Resource{{ID: "semantic_model:orders", Kind: KindModel, Name: "orders"}}, nil); err != nil {
		t.Fatalf("NewProjectGraph() rejected opaque ID: %v", err)
	}
	if _, err := NewProjectGraph([]Resource{{ID: "semantic_model:orders", Kind: KindModel, Name: "semantic_model:orders"}}, nil); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("NewProjectGraph() error = %v, want symbolic name rejection", err)
	}
}

func TestSharedResourceAppearsOnceAcrossSemanticDomains(t *testing.T) {
	resources := []Resource{
		{ID: "model_date", Kind: KindModel, Name: "date", Metadata: Metadata{Domain: "shared"}},
		{ID: "semantic_sales", Kind: KindSemanticModel, Name: "sales", Metadata: Metadata{Domain: "sales"}},
		{ID: "semantic_finance", Kind: KindSemanticModel, Name: "finance", Metadata: Metadata{Domain: "finance"}},
	}
	project, err := NewProjectGraph(resources, []Edge{{From: "semantic_sales", To: "model_date"}, {From: "semantic_finance", To: "model_date"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(project.Resources()); got != len(resources) {
		t.Fatalf("resource count = %d, want shared model represented once in %d resources", got, len(resources))
	}
	shared, ok := project.Resource("model_date")
	if !ok || shared.Metadata.Domain != "shared" {
		t.Fatalf("shared resource = %#v, want one shared model", shared)
	}
	if got := len(project.Edges()); got != 2 {
		t.Fatalf("edge count = %d, want two cross-domain references", got)
	}
}

func TestNewProjectGraphRejectsControlPlaneProject(t *testing.T) {
	for _, resources := range [][]Resource{
		{{ID: "project_demo", Kind: KindProjectNamespace, Name: "demo"}},
		{{ID: "model_orders", Kind: KindModel, Name: "orders"}, {ID: "project_demo", Kind: KindProjectNamespace, Name: "demo"}},
	} {
		if _, err := NewProjectGraph(resources, nil); !errors.Is(err, ErrProjectRoot) {
			t.Fatalf("NewProjectGraph() error = %v, want control-plane Project rejection", err)
		}
	}
	// Access/configuration parsing may still recognize the control-plane kind;
	// recognition does not make it portable.
	if kind, err := ParseKind("project"); err != nil || kind != KindProjectNamespace || kind.Portable() {
		t.Fatalf("ParseKind(project) = %q, %v; want recognized non-portable kind", kind, err)
	}
}

func TestCanonicalRoundTripAndDigest(t *testing.T) {
	original, err := NewProjectGraph([]Resource{
		{ID: "b", Kind: KindModel, Name: "b"},
		{ID: "a", Kind: KindSource, Name: "a", Metadata: Metadata{Tags: []string{"z", "a"}}},
	}, []Edge{{From: "b", To: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(original.CanonicalBytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original.CanonicalBytes(), decoded.CanonicalBytes()) || original.Digest() != decoded.Digest() {
		t.Fatal("decoded graph changed canonical identity")
	}
	var roundTrip ProjectGraph
	if err := roundTrip.UnmarshalJSON(original.CanonicalBytes()); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Digest() != original.Digest() {
		t.Fatal("JSON roundtrip changed graph digest")
	}
	if _, err := Decode([]byte(`{"version":1,"resources":[],"edges":[]}`)); err == nil {
		t.Fatal("Decode accepted legacy graph version")
	} else {
		var unsupported UnsupportedVersionError
		if !errors.As(err, &unsupported) || unsupported.Version != 1 {
			t.Fatalf("Decode legacy error = %v, want unsupported version", err)
		}
	}
}

func TestGraphRejectsMalformedAndTamperedInputs(t *testing.T) {
	if err := Validate([]Resource{{ID: "orders/view", Kind: KindModel, Name: "orders"}}, nil); !errors.Is(err, ErrInvalidResourceID) {
		t.Fatalf("Validate malformed ID = %v", err)
	}
	if err := Validate([]Resource{{ID: "a", Kind: KindModel, Name: "a"}, {ID: "b", Kind: KindModel, Name: "b"}}, []Edge{{From: "a", To: "b"}, {From: "a", To: "b", Relation: "duplicate"}}); !errors.Is(err, ErrDuplicateEdge) {
		t.Fatalf("Validate duplicate edge = %v", err)
	}
	if err := Validate([]Resource{{ID: "a", Kind: KindModel, Name: "a"}, {ID: "b", Kind: KindModel, Name: "b"}}, []Edge{{From: "a", To: "b"}, {From: "b", To: "a"}}); !errors.Is(err, ErrCycle) {
		t.Fatalf("Validate cycle = %v", err)
	}
	original, err := NewProjectGraph([]Resource{{ID: "orders", Kind: KindModel, Name: "orders"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(original.CanonicalBytes(), []byte(`"orders"`), []byte(`"tampered"`), 1)
	decoded, err := Decode(tampered)
	if err != nil {
		t.Fatalf("Decode tampered graph = %v", err)
	}
	if decoded.Digest() == original.Digest() {
		t.Fatal("tampered graph retained original digest")
	}
	if bytes.Equal(original.CanonicalBytes(), tampered) {
		t.Fatal("test failed to tamper graph bytes")
	}
}

func TestGraphDefensivelyCopiesValuesAndComputesDependencies(t *testing.T) {
	resources := []Resource{
		{ID: "dashboard_sales", Kind: KindDashboard, Name: "sales"},
		{ID: "semantic_sales", Kind: KindSemanticModel, Name: "sales_model"},
		{ID: "model_orders", Kind: KindModel, Name: "orders", Metadata: Metadata{Tags: []string{"orders"}}},
	}
	edges := []Edge{{From: "dashboard_sales", To: "semantic_sales"}, {From: "semantic_sales", To: "model_orders"}}
	g, err := NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}
	resources[2].Metadata.Tags[0] = "changed"
	edges[0].From = "model_orders"
	got, _ := g.Resource("model_orders")
	if got.Metadata.Tags[0] != "orders" {
		t.Fatalf("graph retained mutable input: %#v", got)
	}
	if got, want := g.Dependencies("dashboard_sales"), []ResourceID{"dashboard_sales", "model_orders", "semantic_sales"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dependencies = %v, want %v", got, want)
	}
	if got, want := g.AffectedDashboards([]ResourceID{"model_orders"}), []ResourceID{"dashboard_sales"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("affected dashboards = %v, want %v", got, want)
	}
	gotResource, _ := g.Resource("model_orders")
	gotResource.Metadata.Tags[0] = "changed again"
	if again, _ := g.Resource("model_orders"); again.Metadata.Tags[0] != "orders" {
		t.Fatalf("resource lookup escaped immutable graph: %#v", again)
	}
	returned := g.Resources()
	for index := range returned {
		if returned[index].ID == "model_orders" {
			returned[index].Metadata.Tags[0] = "changed yet again"
		}
	}
	if again, _ := g.Resource("model_orders"); again.Metadata.Tags[0] != "orders" {
		t.Fatalf("resources escaped immutable graph: %#v", again)
	}
}

func TestArtifactEnvelopeBindsExternalProjectUIDWithoutGraphComparison(t *testing.T) {
	graphValue, err := NewProjectGraph([]Resource{{ID: "model_orders", Kind: KindModel, Name: "orders"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := NewServingIdentity("project_external", "production", "generation_7")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := NewArtifactEnvelope(identity, graphValue)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Identity() != identity || envelope.Graph().Digest() != graphValue.Digest() {
		t.Fatalf("envelope binding = %#v, %q", envelope.Identity(), envelope.Graph().Digest())
	}
	if !strings.Contains(string(envelope.CanonicalBytes()), `"version":2`) || strings.Contains(string(envelope.CanonicalBytes()), `"project":"`) {
		t.Fatalf("envelope canonical bytes = %s", envelope.CanonicalBytes())
	}
	decoded, err := DecodeArtifactEnvelope(envelope.CanonicalBytes())
	if err != nil || decoded.Digest() != envelope.Digest() {
		t.Fatalf("envelope roundtrip = %v", err)
	}
	if _, err := DecodeArtifactEnvelope(bytes.Replace(envelope.CanonicalBytes(), []byte(`"version":2`), []byte(`"version":1`), 1)); err == nil {
		t.Fatal("DecodeArtifactEnvelope accepted legacy envelope version")
	}
}

func TestServingScopesValidateIndependently(t *testing.T) {
	if err := ValidateServingScope("project_demo", "production"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateServingScope("", "production"); !errors.Is(err, ErrInvalidServingIdentity) {
		t.Fatalf("empty project scope error = %v", err)
	}
	initial := CandidateScope{ProjectID: "project_demo", Environment: "production"}
	if base, err := initial.BaseIdentity(); err != nil || base != nil {
		t.Fatalf("initial base identity = %#v, %v", base, err)
	}
}

func TestAffectedDashboardsUsesTransitiveResourceIDs(t *testing.T) {
	project, err := NewProjectGraph([]Resource{
		{ID: "dashboard_sales", Kind: KindDashboard, Name: "sales_dashboard"},
		{ID: "dashboard_ops", Kind: KindDashboard, Name: "ops_dashboard"},
		{ID: "semantic_sales", Kind: KindSemanticModel, Name: "sales"},
		{ID: "semantic_ops", Kind: KindSemanticModel, Name: "ops"},
		{ID: "model_orders", Kind: KindModel, Name: "orders"},
		{ID: "model_inventory", Kind: KindModel, Name: "inventory"},
	}, []Edge{
		{From: "dashboard_sales", To: "semantic_sales"}, {From: "semantic_sales", To: "model_orders"},
		{From: "dashboard_ops", To: "semantic_ops"}, {From: "semantic_ops", To: "model_inventory"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := project.Dependencies("dashboard_sales"), []ResourceID{"dashboard_sales", "model_orders", "semantic_sales"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dependencies = %v, want %v", got, want)
	}
	if got, want := project.AffectedDashboards([]ResourceID{"model_orders", "model_inventory", "model_orders"}), []ResourceID{"dashboard_ops", "dashboard_sales"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("affected dashboards = %v, want %v", got, want)
	}
	if got := project.AffectedDashboards([]ResourceID{"connection_unrelated"}); len(got) != 0 {
		t.Fatalf("unrelated changed IDs affected dashboards: %v", got)
	}
}

func TestValidationRejectsMalformedIDsAndKinds(t *testing.T) {
	tests := []struct {
		name      string
		resources []Resource
		want      error
	}{
		{name: "empty id", resources: []Resource{{Kind: KindModel}}, want: ErrInvalidResourceID},
		{name: "path id", resources: []Resource{{ID: "models/orders", Kind: KindModel}}, want: ErrInvalidResourceID},
		{name: "whitespace id", resources: []Resource{{ID: " orders", Kind: KindModel}}, want: ErrInvalidResourceID},
		{name: "invalid kind", resources: []Resource{{ID: "orders", Kind: "table", Name: "orders"}}, want: ErrInvalidKind},
		{name: "missing name", resources: []Resource{{ID: "orders", Kind: KindModel}}, want: ErrInvalidName},
		{name: "invalid name", resources: []Resource{{ID: "orders", Kind: KindModel, Name: "orders/view"}}, want: ErrInvalidName},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := Validate(test.resources, nil); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestValidationRejectsDuplicateIDsNamesAndEdges(t *testing.T) {
	base := []Resource{{ID: "model_one", Kind: KindModel, Name: "one"}, {ID: "model_two", Kind: KindModel, Name: "two"}}
	tests := []struct {
		name      string
		resources []Resource
		edges     []Edge
		want      error
	}{
		{name: "duplicate id", resources: append(append([]Resource(nil), base...), Resource{ID: "model_one", Kind: KindSource, Name: "three"}), want: ErrDuplicateResourceID},
		{name: "duplicate name", resources: []Resource{{ID: "model_one", Kind: KindModel, Name: "same"}, {ID: "model_two", Kind: KindSource, Name: "same"}}, want: ErrDuplicateName},
		{name: "missing from", resources: base, edges: []Edge{{From: "missing", To: "model_one"}}, want: ErrMissingEndpoint},
		{name: "missing to", resources: base, edges: []Edge{{From: "model_one", To: "missing"}}, want: ErrMissingEndpoint},
		{name: "duplicate edge", resources: base, edges: []Edge{{From: "model_one", To: "model_two"}, {From: "model_one", To: "model_two", Relation: "refresh"}}, want: ErrDuplicateEdge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := Validate(test.resources, test.edges); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestValidationRejectsCycles(t *testing.T) {
	resources := []Resource{{ID: "a", Kind: KindModel, Name: "a"}, {ID: "b", Kind: KindModel, Name: "b"}, {ID: "c", Kind: KindModel, Name: "c"}}
	edges := []Edge{{From: "a", To: "b"}, {From: "b", To: "c"}, {From: "c", To: "a"}}
	if err := Validate(resources, edges); !errors.Is(err, ErrCycle) {
		t.Fatalf("Validate() error = %v, want cycle", err)
	}
	if _, err := NewProjectGraph([]Resource{{ID: "a", Kind: KindModel, Name: "a"}}, []Edge{{From: "a", To: "a"}}); !errors.Is(err, ErrCycle) {
		t.Fatalf("NewProjectGraph() error = %v, want self-cycle", err)
	}
}

func TestDecodersRejectDuplicateCaseFoldedAndTrailingJSON(t *testing.T) {
	graph := `{"version":2,"resources":[],"edges":[]}`
	for _, input := range []string{
		`{"version":2,"version":2,"resources":[],"edges":[]}`,
		`{"version":2,"Version":2,"resources":[],"edges":[]}`,
		graph + graph,
	} {
		if _, err := Decode([]byte(input)); err == nil {
			t.Fatalf("Decode accepted malformed JSON: %s", input)
		}
	}
	artifact := `{"version":2,"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_1"},"graph":` + graph + `}`
	for _, input := range []string{
		strings.Replace(artifact, `"version":2,"identity"`, `"version":2,"version":2,"identity"`, 1),
		strings.Replace(artifact, `"projectId":"project_demo"`, `"projectId":"project_demo","ProjectID":"project_demo"`, 1),
		artifact + artifact,
	} {
		if _, err := DecodeArtifactEnvelope([]byte(input)); err == nil {
			t.Fatalf("DecodeArtifactEnvelope accepted malformed JSON: %s", input)
		}
	}
}
