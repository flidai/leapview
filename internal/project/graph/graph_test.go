package graph

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestResourceIDDoesNotDependOnMetadataPathOrProvenance(t *testing.T) {
	base := Resource{
		ID:   "model_orders",
		Kind: KindModel,
		Name: "orders",
		Metadata: Metadata{
			DisplayName: "Orders",
			Owner:       "team-data",
			Domain:      "commerce",
		},
		Provenance: Provenance{Origin: "project", Path: "models/orders.yaml", Source: "git:abc"},
	}
	changed := base
	changed.Metadata.DisplayName = "Order facts"
	changed.Metadata.Owner = "team-analytics"
	changed.Metadata.Domain = "finance"
	changed.Provenance = Provenance{Origin: "agent", Path: "archive/orders.yml", Source: "builder"}

	if base.ID != changed.ID {
		t.Fatalf("metadata/path/provenance changed resource ID: %q != %q", base.ID, changed.ID)
	}
	first, err := NewProjectGraph([]Resource{{ID: "project_demo", Kind: KindProject, Name: "demo"}, base}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewProjectGraph([]Resource{{ID: "project_demo", Kind: KindProject, Name: "demo"}, changed}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := first.Resource(base.ID)
	if !ok || got.ID != changed.ID {
		t.Fatalf("graph lookup changed resource ID: %#v", got)
	}
	if _, ok := second.Resource(changed.ID); !ok {
		t.Fatalf("changed resource is missing from graph")
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
	if _, err := NewProjectGraph([]Resource{
		{ID: "semantic_model:demo", Kind: KindProject, Name: "demo"},
		{ID: "semantic_model:orders", Kind: KindModel, Name: "orders"},
	}, nil); err != nil {
		t.Fatalf("NewProjectGraph() rejected opaque IDs: %v", err)
	}
	if _, err := NewProjectGraph([]Resource{
		{ID: "project_demo", Kind: KindProject, Name: "demo"},
		{ID: "semantic_model:orders", Kind: KindModel, Name: "semantic_model:orders"},
	}, nil); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("NewProjectGraph() error = %v, want symbolic name rejection", err)
	}
}

func TestCanonicalBytesAndDigestAreDeterministic(t *testing.T) {
	resources := []Resource{
		{ID: "project_demo", Kind: KindProject, Name: "demo"},
		{ID: "dashboard_main", Kind: KindDashboard, Name: "main", Metadata: Metadata{Tags: []string{"insights", "core"}}},
		{ID: "model_orders", Kind: KindModel, Name: "orders"},
		{ID: "source_warehouse", Kind: KindSource, Name: "warehouse"},
	}
	edges := []Edge{
		{From: "model_orders", To: "source_warehouse"},
		{From: "dashboard_main", To: "model_orders"},
	}
	first, err := NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewProjectGraph([]Resource{resources[2], resources[0], resources[1], resources[3]}, []Edge{edges[1], edges[0]})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.CanonicalBytes(), second.CanonicalBytes()) {
		t.Fatalf("canonical bytes differ:\n%s\n%s", first.CanonicalBytes(), second.CanonicalBytes())
	}
	if first.Digest() != second.Digest() || !strings.HasPrefix(first.Digest(), "sha256:") {
		t.Fatalf("digest = %q and %q", first.Digest(), second.Digest())
	}
	if got := string(first.CanonicalBytes()); got != `{"version":1,"resources":[{"id":"dashboard_main","kind":"dashboard","name":"main","metadata":{"tags":["core","insights"]},"provenance":{}},{"id":"model_orders","kind":"model","name":"orders","metadata":{},"provenance":{}},{"id":"project_demo","kind":"project","name":"demo","metadata":{},"provenance":{}},{"id":"source_warehouse","kind":"source","name":"warehouse","metadata":{},"provenance":{}}],"edges":[{"from":"dashboard_main","to":"model_orders"},{"from":"model_orders","to":"source_warehouse"}]}` {
		t.Fatalf("canonical bytes = %s", got)
	}
}

func TestSharedResourceAppearsOnceAcrossSemanticDomains(t *testing.T) {
	resources := []Resource{
		{ID: "project_demo", Kind: KindProject, Name: "demo"},
		{ID: "model_date", Kind: KindModel, Name: "date", Metadata: Metadata{Domain: "shared"}},
		{ID: "semantic_sales", Kind: KindSemanticModel, Name: "sales", Metadata: Metadata{Domain: "sales"}},
		{ID: "semantic_finance", Kind: KindSemanticModel, Name: "finance", Metadata: Metadata{Domain: "finance"}},
	}
	edges := []Edge{
		{From: "semantic_sales", To: "model_date"},
		{From: "semantic_finance", To: "model_date"},
	}
	project, err := NewProjectGraph(resources, edges)
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
	for _, edge := range project.Edges() {
		if edge.To != "model_date" {
			t.Fatalf("edge = %#v, want shared model endpoint", edge)
		}
		if _, ok := project.Resource(edge.From); !ok {
			t.Fatalf("edge source %q is not in graph", edge.From)
		}
	}
}

func TestGraphDefensivelyCopiesInputsAndOutputs(t *testing.T) {
	resources := []Resource{{ID: "project_demo", Kind: KindProject, Name: "demo"}, {ID: "model_orders", Kind: KindModel, Name: "orders", Metadata: Metadata{Tags: []string{"orders"}}}}
	edges := []Edge{{From: "model_orders", To: "model_orders"}}
	// A self-edge is a cycle, so construct with an acyclic edge below.
	edges[0].To = "model_orders_view"
	resources = append(resources, Resource{ID: "model_orders_view", Kind: KindModel, Name: "orders_view"})
	g, err := NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}
	resources[1].Metadata.Tags[0] = "changed"
	edges[0].From = "model_orders_view"
	got, ok := g.Resource("model_orders")
	if !ok || got.Metadata.Tags[0] != "orders" {
		t.Fatalf("graph retained mutable input: %#v", got)
	}
	got.Metadata.Tags[0] = "changed again"
	again, _ := g.Resource("model_orders")
	if again.Metadata.Tags[0] != "orders" {
		t.Fatalf("resource lookup escaped immutable graph: %#v", again)
	}
	returned := g.Resources()
	for index := range returned {
		if returned[index].ID == "model_orders" {
			returned[index].Metadata.Tags[0] = "changed yet again"
		}
	}
	if got, _ := g.Resource("model_orders"); got.Metadata.Tags[0] != "orders" {
		t.Fatalf("resources escaped immutable graph: %#v", got)
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
		{name: "invalid kind", resources: []Resource{{ID: "orders", Kind: "table"}}, want: ErrInvalidKind},
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
	base := []Resource{
		{ID: "model_one", Kind: KindModel, Name: "one"},
		{ID: "model_two", Kind: KindModel, Name: "two"},
	}
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
	resources := []Resource{
		{ID: "a", Kind: KindModel, Name: "a"},
		{ID: "b", Kind: KindModel, Name: "b"},
		{ID: "c", Kind: KindModel, Name: "c"},
	}
	edges := []Edge{{From: "a", To: "b"}, {From: "b", To: "c"}, {From: "c", To: "a"}}
	if err := Validate(resources, edges); !errors.Is(err, ErrCycle) {
		t.Fatalf("Validate() error = %v, want cycle", err)
	}

	if _, err := NewProjectGraph([]Resource{{ID: "project_demo", Kind: KindProject, Name: "demo"}, {ID: "a", Kind: KindModel, Name: "a"}}, []Edge{{From: "a", To: "a"}}); !errors.Is(err, ErrCycle) {
		t.Fatalf("NewProjectGraph() error = %v, want self-cycle", err)
	}
}

func TestDecodeCanonicalGraph(t *testing.T) {
	original, err := NewProjectGraph([]Resource{{ID: "project_demo", Kind: KindProject, Name: "demo"}, {ID: "source_orders", Kind: KindSource, Name: "orders"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(original.CanonicalBytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original.CanonicalBytes(), decoded.CanonicalBytes()) || original.Digest() != decoded.Digest() {
		t.Fatalf("decoded graph changed canonical artifact")
	}
	_, err = Decode([]byte(`{"version":99,"resources":[{"id":"project_demo","kind":"project","name":"demo"}],"edges":[]}`))
	var unsupported UnsupportedVersionError
	if !errors.As(err, &unsupported) || unsupported.Contract != "project graph" || unsupported.Version != 99 {
		t.Fatalf("Decode() error = %v, want graph UnsupportedVersionError", err)
	}
	if err == nil {
		t.Fatal("Decode() accepted unsupported version")
	}
	artifactBytes := `{"version":99,"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_1"},"graph":{"version":1,"resources":[{"id":"project_demo","kind":"project","name":"demo"}],"edges":[]}}`
	_, err = DecodeArtifactEnvelope([]byte(artifactBytes))
	unsupported = UnsupportedVersionError{}
	if !errors.As(err, &unsupported) || unsupported.Contract != "project artifact" || unsupported.Version != 99 {
		t.Fatalf("DecodeArtifactEnvelope() error = %v, want artifact UnsupportedVersionError", err)
	}
}

func TestProjectGraphRequiresExactlyOneProjectRoot(t *testing.T) {
	resources := []Resource{{ID: "model_orders", Kind: KindModel, Name: "orders"}}
	if _, err := NewProjectGraph(resources, nil); !errors.Is(err, ErrProjectRoot) {
		t.Fatalf("NewProjectGraph() error = %v, want missing project root", err)
	}
	resources = append(resources, Resource{ID: "project_one", Kind: KindProject, Name: "one"}, Resource{ID: "project_two", Kind: KindProject, Name: "two"})
	if _, err := NewProjectGraph(resources, nil); !errors.Is(err, ErrProjectRoot) {
		t.Fatalf("NewProjectGraph() error = %v, want multiple project roots", err)
	}
}

func TestArtifactEnvelopeBindsPortableGraphToServingIdentity(t *testing.T) {
	project, err := NewProjectGraph([]Resource{
		{ID: "project_demo", Kind: KindProject, Name: "demo"},
		{ID: "model_orders", Kind: KindModel, Name: "orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "generation_7"}
	envelope, err := NewArtifactEnvelope(identity, project)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Identity().ProjectID != project.ProjectID() || envelope.Identity().Environment != "production" || envelope.Identity().GenerationID != "generation_7" {
		t.Fatalf("serving identity = %#v", envelope.Identity())
	}
	wantCanonical := `{"version":1,"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_7"},"graph":{"version":1,"resources":[{"id":"model_orders","kind":"model","name":"orders","metadata":{},"provenance":{}},{"id":"project_demo","kind":"project","name":"demo","metadata":{},"provenance":{}}],"edges":[]}}`
	if got := string(envelope.CanonicalBytes()); got != wantCanonical {
		t.Fatalf("canonical artifact = %s, want %s", got, wantCanonical)
	}
	if bytes.Equal(envelope.CanonicalBytes(), project.CanonicalBytes()) {
		t.Fatal("serving envelope unexpectedly equals portable graph bytes")
	}
	if strings.Contains(string(project.CanonicalBytes()), "workspace") || strings.Contains(string(envelope.CanonicalBytes()), "workspace") {
		t.Fatalf("canonical contract contains workspace identity: %s", envelope.CanonicalBytes())
	}
	decoded, err := DecodeArtifactEnvelope(envelope.CanonicalBytes())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Digest() != envelope.Digest() || decoded.Graph().Digest() != project.Digest() {
		t.Fatalf("decoded artifact changed canonical identity")
	}
	if _, err := NewArtifactEnvelope(ServingIdentity{ProjectID: "other_project", Environment: "production", GenerationID: "generation_7"}, project); !errors.Is(err, ErrProjectIdentityMismatch) {
		t.Fatalf("NewArtifactEnvelope() error = %v, want project mismatch", err)
	}
	if _, err := NewArtifactEnvelope(ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "01J8N3YQ6F7T8V9W0X1Y2Z3A4B:serve"}, project); err != nil {
		t.Fatalf("NewArtifactEnvelope() rejected opaque generation ID: %v", err)
	}
}

func TestArtifactEnvelopeRejectsMalformedServingIdentity(t *testing.T) {
	project, err := NewProjectGraph([]Resource{{ID: "project_demo", Kind: KindProject, Name: "demo"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []ServingIdentity{
		{ProjectID: "project_demo", Environment: "", GenerationID: "generation_1"},
		{ProjectID: "project_demo", Environment: "production", GenerationID: ""},
		{ProjectID: "project_demo", Environment: "production/env", GenerationID: "generation_1"},
		{ProjectID: "project_demo", Environment: "production", GenerationID: "generation 1"},
		{ProjectID: "bad/project", Environment: "production", GenerationID: "generation_1"},
	}
	for _, identity := range tests {
		if _, err := NewArtifactEnvelope(identity, project); !errors.Is(err, ErrInvalidServingIdentity) {
			t.Fatalf("NewArtifactEnvelope(%#v) error = %v, want malformed identity", identity, err)
		}
	}
}

func TestServingIdentityCanBeValidatedBeforeArtifactBinding(t *testing.T) {
	identity, err := NewServingIdentity("project_demo", "production", "generation_7")
	if err != nil {
		t.Fatal(err)
	}
	if err := identity.Validate(); err != nil {
		t.Fatal(err)
	}
	if identity.ProjectID != "project_demo" || identity.Environment != "production" || identity.GenerationID != "generation_7" {
		t.Fatalf("identity = %#v", identity)
	}
	if _, err := NewServingIdentity("project_demo", "production/env", "generation_7"); !errors.Is(err, ErrInvalidServingIdentity) {
		t.Fatalf("invalid environment error = %v", err)
	}
	if err := (ServingIdentity{}).Validate(); !errors.Is(err, ErrInvalidServingIdentity) {
		t.Fatalf("zero identity error = %v", err)
	}
}

func TestCandidateScopeSupportsInitialAndExactBaseGenerations(t *testing.T) {
	initial := CandidateScope{ProjectID: "project_demo", Environment: "production"}
	base, err := initial.BaseIdentity()
	if err != nil || base != nil {
		t.Fatalf("initial BaseIdentity() = %#v, err=%v; want nil", base, err)
	}
	exact := CandidateScope{ProjectID: "project_demo", Environment: "production", BaseGenerationID: "generation_7"}
	identity, err := exact.BaseIdentity()
	if err != nil || identity == nil || identity.GenerationID != "generation_7" {
		t.Fatalf("exact BaseIdentity() = %#v, err=%v", identity, err)
	}
	for _, invalid := range []CandidateScope{
		{ProjectID: " project_demo", Environment: "production"},
		{ProjectID: "project_demo", Environment: " production"},
		{ProjectID: "project_demo", Environment: "production", BaseGenerationID: " generation_7"},
	} {
		if _, err := invalid.BaseIdentity(); err == nil {
			t.Fatalf("BaseIdentity(%#v) succeeded", invalid)
		}
	}
}

func TestDecodeRejectsWorkspaceFieldsAndKinds(t *testing.T) {
	workspaceField := `{"version":1,"resources":[{"id":"project_demo","kind":"project","name":"demo","workspace":"legacy"}],"edges":[]}`
	if _, err := Decode([]byte(workspaceField)); err == nil {
		t.Fatal("Decode() accepted workspace field")
	}
	workspaceKind := `{"version":1,"resources":[{"id":"project_demo","kind":"workspace","name":"demo"}],"edges":[]}`
	if _, err := Decode([]byte(workspaceKind)); !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("Decode() error = %v, want invalid workspace kind", err)
	}

	artifactWorkspaceField := `{"version":1,"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_1","workspaceId":"legacy"},"graph":{"version":1,"resources":[{"id":"project_demo","kind":"project","name":"demo"}],"edges":[]}}`
	if _, err := DecodeArtifactEnvelope([]byte(artifactWorkspaceField)); err == nil {
		t.Fatal("DecodeArtifactEnvelope() accepted workspaceId field")
	}
	artifactWorkspaceKind := `{"version":1,"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_1"},"graph":{"version":1,"resources":[{"id":"project_demo","kind":"workspace","name":"demo"}],"edges":[]}}`
	if _, err := DecodeArtifactEnvelope([]byte(artifactWorkspaceKind)); !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("DecodeArtifactEnvelope() error = %v, want invalid workspace kind", err)
	}
}

func TestDecodersRejectDuplicateJSONKeysRecursively(t *testing.T) {
	graphCases := []string{
		`{"version":1,"version":1,"resources":[{"id":"project_demo","kind":"project","name":"demo"}],"edges":[]}`,
		`{"version":1,"resources":[{"id":"project_demo","id":"project_demo","kind":"project","name":"demo"}],"edges":[]}`,
		`{"version":1,"resources":[{"id":"project_demo","kind":"project","kind":"project","name":"demo"}],"edges":[]}`,
		`{"version":1,"resources":[{"id":"project_demo","kind":"project","name":"demo","metadata":{"domain":"a","domain":"b"}}],"edges":[]}`,
	}
	for _, input := range graphCases {
		if _, err := Decode([]byte(input)); err == nil {
			t.Fatalf("Decode() accepted duplicate key: %s", input)
		}
	}
	artifactCases := []string{
		`{"version":1,"version":1,"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_1"},"graph":{"version":1,"resources":[{"id":"project_demo","kind":"project","name":"demo"}],"edges":[]}}`,
		`{"version":1,"identity":{"projectId":"project_demo","projectId":"other","environment":"production","generationId":"generation_1"},"graph":{"version":1,"resources":[{"id":"project_demo","kind":"project","name":"demo"}],"edges":[]}}`,
	}
	for _, input := range artifactCases {
		if _, err := DecodeArtifactEnvelope([]byte(input)); err == nil {
			t.Fatalf("DecodeArtifactEnvelope() accepted duplicate key: %s", input)
		}
	}
}

func TestDecodersRejectCaseFoldedDuplicateJSONKeys(t *testing.T) {
	graph := `{"version":1,"Version":1,"resources":[{"id":"project_demo","kind":"project","name":"demo"}],"edges":[]}`
	if _, err := Decode([]byte(graph)); err == nil {
		t.Fatal("Decode() accepted case-folded duplicate version key")
	}

	artifact := `{"version":1,"identity":{"projectId":"project_demo","ProjectID":"project_demo","environment":"production","generationId":"generation_one"},"graph":{"version":1,"resources":[{"id":"project_demo","kind":"project","name":"demo"}],"edges":[]}}`
	if _, err := DecodeArtifactEnvelope([]byte(artifact)); err == nil {
		t.Fatal("DecodeArtifactEnvelope() accepted case-folded duplicate project ID key")
	}
}

func TestDecodeRejectsTrailingJSONValues(t *testing.T) {
	graph := `{"version":1,"resources":[{"id":"project_demo","kind":"project","name":"demo"}],"edges":[]}`
	if _, err := Decode([]byte(graph + graph)); err == nil {
		t.Fatal("Decode() accepted trailing JSON")
	}
	artifact := `{"version":1,"identity":{"projectId":"project_demo","environment":"production","generationId":"generation_1"},"graph":` + graph + `}`
	if _, err := DecodeArtifactEnvelope([]byte(artifact + artifact)); err == nil {
		t.Fatal("DecodeArtifactEnvelope() accepted trailing JSON")
	}
}
