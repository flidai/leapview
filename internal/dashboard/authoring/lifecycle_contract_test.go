package authoring

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func contractProvenance() Provenance {
	return Provenance{Origin: OriginUI, ActorID: "principal-1", Source: &SourceMetadata{Path: "dashboards/sales.yaml", Metadata: map[string]string{"channel": "editor"}}}
}

func contractDocument() Dashboard {
	return Dashboard{ID: "sales", Title: "Sales", SemanticModel: "sales_model", Visuals: map[string]AuthoringVisualization{}, Pages: []dashboardmodel.Page{{ID: "overview", Title: "Overview"}}}
}

func contractRevisionToken() RevisionToken {
	return RevisionToken{RevisionID: "rev-1", Number: 1, ContentHash: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}
}

func contractCompilationToken() CompiledRevisionToken {
	identity, _ := projectgraph.NewServingIdentity("project-1", "production", "state-1")
	return CompiledRevisionToken{AuthoredRevision: contractRevisionToken(), DefinitionHash: "sha256:1111111111111111111111111111111111111111111111111111111111111111", SemanticIdentity: identity}
}

func contractServingIdentity() projectgraph.ServingIdentity {
	identity, _ := projectgraph.NewServingIdentity("project-1", "production", "state-1")
	return identity
}

func TestLifecycleStatusesAndPointers(t *testing.T) {
	provenance := contractProvenance()
	draft := &Draft{ID: "draft-1", DashboardID: "sales", Revision: contractRevisionToken(), Provenance: provenance}
	lifecycle, err := NewDashboardLifecycle(NewDashboardLifecycleInput{ProjectID: "project-1", ID: "sales", OwnerPrincipalID: "principal-1", Slug: "sales", Title: "Sales", SemanticModel: "sales_model", Visibility: VisibilityPrivate, Draft: draft})
	if err != nil {
		t.Fatalf("NewDashboardLifecycle() error = %v", err)
	}
	if lifecycle.ID != "sales" || lifecycle.Status != LifecycleStatusDraft {
		t.Fatalf("lifecycle = %#v", lifecycle)
	}
	if !CanTransition(LifecycleStatusDraft, LifecycleStatusPublished) || !CanTransition(LifecycleStatusPublished, LifecycleStatusArchived) {
		t.Fatal("expected valid lifecycle transitions")
	}
	for _, transition := range [][2]LifecycleStatus{{LifecycleStatusDraft, LifecycleStatusDraft}, {LifecycleStatusPublished, LifecycleStatusDraft}, {LifecycleStatusArchived, LifecycleStatusPublished}, {LifecycleStatus("unknown"), LifecycleStatusDraft}} {
		if err := ValidateTransition(transition[0], transition[1]); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("ValidateTransition(%q, %q) error = %v", transition[0], transition[1], err)
		}
	}
	if err := (DashboardLifecycle{ID: "sales", Slug: "sales", Title: "Sales", Visibility: VisibilityPrivate, Status: LifecycleStatusDraft}).Validate(); err == nil {
		t.Fatal("draft lifecycle without Draft pointer unexpectedly validated")
	}
	if err := (DashboardLifecycle{ID: "sales", Slug: "sales", Title: "Sales", Visibility: VisibilityPrivate, Status: LifecycleStatusPublished, Draft: draft}).Validate(); err == nil {
		t.Fatal("published lifecycle without Published pointer unexpectedly validated")
	}
	published := &Published{Revision: contractRevisionToken(), Compilation: contractCompilationToken(), PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Provenance: provenance}
	validPublished := lifecycle
	validPublished.Status = LifecycleStatusPublished
	validPublished.Published = published
	if err := validPublished.Validate(); err != nil {
		t.Fatalf("published lifecycle validation error = %v", err)
	}
}

func TestLifecycleJSONRejectsLegacyWorkspaceScope(t *testing.T) {
	draft := &Draft{ID: "draft-1", DashboardID: "sales", Revision: contractRevisionToken(), Provenance: contractProvenance()}
	lifecycle, err := NewDashboardLifecycle(NewDashboardLifecycleInput{ProjectID: "project-1", ID: "sales", OwnerPrincipalID: "principal-1", Slug: "sales", Title: "Sales", SemanticModel: "sales_model", Visibility: VisibilityPrivate, Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "workspace") {
		t.Fatalf("lifecycle serialization leaked legacy workspace scope: %s", encoded)
	}
	var decoded DashboardLifecycle
	legacy := strings.Replace(string(encoded), "\"projectId\":\"project-1\"", "\"workspaceId\":\"legacy\",\"projectId\":\"project-1\"", 1)
	if err := json.Unmarshal([]byte(legacy), &decoded); err == nil {
		t.Fatal("lifecycle decoder accepted legacy workspaceId")
	}
}

func TestIdentifiersAndEnumsAreClosed(t *testing.T) {
	for _, value := range []string{"", "has space", "../source", "/absolute", "a/b"} {
		if err := ValidateDashboardID(DashboardID(value)); !errors.Is(err, ErrInvalidIdentifier) {
			t.Errorf("ValidateDashboardID(%q) = %v", value, err)
		}
	}
	for _, value := range []string{"draft", "published", "archived"} {
		if !LifecycleStatus(value).Valid() {
			t.Errorf("LifecycleStatus(%q) is not valid", value)
		}
	}
	if LifecycleStatus("deleted").Valid() {
		t.Fatal("unknown lifecycle status accepted")
	}
	if Visibility("team").Valid() || Origin("git").Valid() {
		t.Fatal("unknown enum accepted")
	}
}

func TestProvenanceMetadataRoundTripAndValidation(t *testing.T) {
	value := Provenance{
		Origin:               OriginAgent,
		ActorID:              "principal-1",
		ConversationID:       "conversation-1",
		ToolCallID:           "call-1",
		BaseSemanticIdentity: contractServingIdentity(),
		Source:               &SourceMetadata{Repository: "org/repo", Path: "dashboards/sales.yaml", Ref: "main", Revision: "abc123"},
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("metadata provenance validation error = %v", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Provenance
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Source == nil {
		t.Fatalf("provenance metadata did not round-trip: %#v", decoded)
	}
	if decoded.ConversationID != value.ConversationID || decoded.ToolCallID != value.ToolCallID || decoded.BaseSemanticIdentity != value.BaseSemanticIdentity || decoded.Source.Repository != value.Source.Repository {
		t.Fatalf("provenance metadata did not round-trip: %#v", decoded)
	}
	for _, invalid := range []Provenance{
		{Origin: OriginAgent, ActorID: "principal-1", ConversationID: "bad id"},
		{Origin: OriginAgent, ActorID: "principal-1", ToolCallID: "" + " bad"},
		{Origin: OriginAgent, ActorID: "principal-1", BaseSemanticIdentity: projectgraph.ServingIdentity{ProjectID: "project-1", Environment: "production", GenerationID: "state/1"}},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid provenance metadata unexpectedly validated: %#v", invalid)
		}
	}
}

func TestProvenanceIsDeepClonedIntoRevisionAndLifecycleDraft(t *testing.T) {
	document := contractDocument()
	provenance := contractProvenance()
	revision, err := NewRevision("rev-1", "sales", 1, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), document, provenance)
	if err != nil {
		t.Fatal(err)
	}
	draft := &Draft{ID: "draft-1", DashboardID: "sales", Revision: revision.Token(), Provenance: provenance}
	lifecycle, err := NewDashboardLifecycle(NewDashboardLifecycleInput{ProjectID: "project-1", ID: "sales", OwnerPrincipalID: "principal-1", Slug: "sales", Title: "Sales", SemanticModel: "sales_model", Visibility: VisibilityPrivate, Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	provenance.Source.Metadata["channel"] = "caller-mutated"
	if revision.Provenance.Source.Metadata["channel"] != "editor" {
		t.Fatalf("revision provenance aliases caller metadata: %#v", revision.Provenance.Source.Metadata)
	}
	if lifecycle.Draft.Provenance.Source.Metadata["channel"] != "editor" {
		t.Fatalf("lifecycle draft provenance aliases caller metadata: %#v", lifecycle.Draft.Provenance.Source.Metadata)
	}
	lifecycle.Draft.Provenance.Source.Metadata["channel"] = "lifecycle-mutated"
	if revision.Provenance.Source.Metadata["channel"] != "editor" {
		t.Fatal("lifecycle draft provenance aliases revision provenance")
	}
}

func TestProvenanceCloneDetachesForkEvidence(t *testing.T) {
	t.Run("instance", func(t *testing.T) {
		original := Provenance{Origin: OriginAgent, ActorID: "actor", ForkedFrom: &ForkEvidence{
			Kind:     ForkSourceInstance,
			Instance: &InstanceForkEvidence{SourceProjectID: "source-project", SourceDashboardID: "source-dashboard", SourceRevision: contractRevisionToken()},
		}}
		cloned := original.Clone()
		if cloned.ForkedFrom == original.ForkedFrom || cloned.ForkedFrom.Instance == original.ForkedFrom.Instance {
			t.Fatal("instance fork evidence was not detached")
		}
		cloned.ForkedFrom.Instance.SourceProjectID = "mutated-project"
		cloned.ForkedFrom.Instance.SourceRevision.Number = 99
		if original.ForkedFrom.Instance.SourceProjectID != "source-project" || original.ForkedFrom.Instance.SourceRevision.Number != 1 {
			t.Fatalf("original instance evidence mutated through clone: %#v", original.ForkedFrom.Instance)
		}
	})
	t.Run("project", func(t *testing.T) {
		original := Provenance{Origin: OriginAgent, ActorID: "actor", ForkedFrom: &ForkEvidence{
			Kind:    ForkSourceProject,
			Project: &ProjectForkEvidence{SourceProjectID: "project-1", SourceDashboardID: "source-dashboard", Identity: contractServingIdentity(), Path: "dashboards/source.yaml"},
		}}
		cloned := original.Clone()
		if cloned.ForkedFrom == original.ForkedFrom || cloned.ForkedFrom.Project == original.ForkedFrom.Project {
			t.Fatal("project fork evidence was not detached")
		}
		cloned.ForkedFrom.Project.SourceProjectID = "mutated-project"
		cloned.ForkedFrom.Project.Path = "mutated.yaml"
		if original.ForkedFrom.Project.SourceProjectID != "project-1" || original.ForkedFrom.Project.Path != "dashboards/source.yaml" {
			t.Fatalf("original project evidence mutated through clone: %#v", original.ForkedFrom.Project)
		}
	})
}

func TestDashboardIDIsIndependentOfSlugAndTitle(t *testing.T) {
	provenance := contractProvenance()
	draft := &Draft{ID: "draft-1", DashboardID: "stable-id", Revision: contractRevisionToken(), Provenance: provenance}
	one, err := NewDashboardLifecycle(NewDashboardLifecycleInput{ProjectID: "project-1", ID: "stable-id", OwnerPrincipalID: "principal-1", Slug: "first-slug", Title: "First title", SemanticModel: "sales_model", Visibility: VisibilityPrivate, Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	two := one
	two.Slug, two.Title = "renamed-slug", "Renamed title"
	if one.ID != two.ID {
		t.Fatalf("dashboard identity changed with metadata: %q != %q", one.ID, two.ID)
	}
}

func TestLifecycleRequiresProjectOwnerAndSemanticModelIdentity(t *testing.T) {
	base := DashboardLifecycle{
		ProjectID: "project-1", ID: "sales", OwnerPrincipalID: "principal-1", Slug: "sales",
		Title: "Sales", SemanticModel: "sales_model", Visibility: VisibilityPrivate, Status: LifecycleStatusDraft,
		Draft: &Draft{ID: "draft-1", DashboardID: "sales", Revision: contractRevisionToken(), Provenance: contractProvenance()},
	}
	for name, mutate := range map[string]func(*DashboardLifecycle){
		"project":        func(value *DashboardLifecycle) { value.ProjectID = "" },
		"owner":          func(value *DashboardLifecycle) { value.OwnerPrincipalID = "owner id" },
		"semantic model": func(value *DashboardLifecycle) { value.SemanticModel = "" },
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("lifecycle missing required identity unexpectedly validated")
			}
		})
	}
}

func TestLifecycleRequiresCanonicalProjectIdentityAndKnownVisibility(t *testing.T) {
	base := DashboardLifecycle{
		ProjectID: "project-1", ID: "sales", OwnerPrincipalID: "principal-1", Slug: "sales",
		Title: "Sales", SemanticModel: "sales_model", Visibility: VisibilityPrivate, Status: LifecycleStatusDraft,
		Draft: &Draft{ID: "draft-1", DashboardID: "sales", Revision: contractRevisionToken(), Provenance: contractProvenance()},
	}
	for _, value := range []string{"", " project-1", "project-1 ", "project/1"} {
		candidate := base
		candidate.ProjectID = projectgraph.ResourceID(value)
		if err := candidate.Validate(); err == nil {
			t.Fatalf("project identity %q unexpectedly validated", value)
		}
	}
	for _, visibility := range []Visibility{VisibilityPrivate, VisibilityRestricted, VisibilityOrganization} {
		candidate := base
		candidate.Visibility = visibility
		if err := candidate.Validate(); err != nil {
			t.Fatalf("visibility %q rejected: %v", visibility, err)
		}
	}
	for _, visibility := range []Visibility{"shared", "workspace", ""} {
		candidate := base
		candidate.Visibility = visibility
		if err := candidate.Validate(); err == nil {
			t.Fatalf("visibility %q unexpectedly validated", visibility)
		}
	}
}

func TestRevisionCopiesDocumentAndHash(t *testing.T) {
	document := contractDocument()
	document.Visuals = map[string]AuthoringVisualization{"revenue": ChartVisualization(Visual{Type: "line"})}
	document.Pages[0].Visuals = []dashboardmodel.PageVisual{{ID: "tile", Kind: "visual", Visual: "revenue"}}
	createdAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	revision, err := NewRevision("rev-1", "sales", 1, createdAt, document, contractProvenance())
	if err != nil {
		t.Fatalf("NewRevision() error = %v", err)
	}
	if err := revision.Validate(); err != nil {
		t.Fatalf("Revision.Validate() error = %v", err)
	}
	document.Pages[0].Visuals[0].ID = "mutated-source"
	cloned, err := revision.Document.Clone()
	if err != nil {
		t.Fatal(err)
	}
	cloned.Pages[0].Visuals[0].ID = "mutated-clone"
	if revision.Document.Pages[0].Visuals[0].ID != "tile" {
		t.Fatalf("revision document was aliased to source: %#v", revision.Document.Pages[0].Visuals)
	}
	if revision.ContentHash == "" || revision.Token().RevisionID != "rev-1" {
		t.Fatalf("revision token/hash = %#v", revision.Token())
	}
	mutated := revision
	mutated.Document.Title = "changed"
	if err := mutated.Validate(); err == nil {
		t.Fatal("revision with changed document/hash unexpectedly validated")
	}
}

func TestRevisionTokenMustBeZeroOrComplete(t *testing.T) {
	validHash := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	cases := []RevisionToken{{RevisionID: "rev-1"}, {RevisionID: "rev-1", Number: 1}, {RevisionID: "rev-1", Number: 1, ContentHash: "not-a-hash"}, {Number: 1, ContentHash: validHash}}
	for _, token := range cases {
		if err := token.Validate(); err == nil {
			t.Errorf("partial token %#v unexpectedly validated", token)
		}
	}
	if err := (RevisionToken{}).Validate(); err != nil {
		t.Fatalf("zero token error = %v", err)
	}
	if err := (RevisionToken{RevisionID: "rev-1", Number: 1, ContentHash: validHash}).Validate(); err != nil {
		t.Fatalf("complete token error = %v", err)
	}
}

func TestDashboardClonePreservesAuthoredOpaqueAndInterfaceValues(t *testing.T) {
	document := contractDocument()
	document.Pages[0].Width, document.Pages[0].Height = 111, 222 // json:"-" fields still belong to an authored document copy.
	document.FilterDefinitions = map[string]dashboardfilter.Definition{
		"year": {Options: dashboardfilter.OptionSource{Values: []dashboardfilter.Option{{Value: dashboardfilter.Value{Kind: dashboardfilter.ValueInteger, Value: int64(2026)}}}}},
	}
	cloned, err := document.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if cloned.Pages[0].Width != 111 || cloned.Pages[0].Height != 222 {
		t.Fatalf("clone dropped authored page dimensions: %#v", cloned.Pages[0])
	}
	value, ok := cloned.FilterDefinitions["year"].Options.Values[0].Value.Value.(int64)
	if !ok || value != 2026 {
		t.Fatalf("clone changed integer interface value: %#v (%T)", cloned.FilterDefinitions["year"].Options.Values[0].Value.Value, cloned.FilterDefinitions["year"].Options.Values[0].Value.Value)
	}
	cloned.FilterDefinitions["year"].Options.Values[0].Value.Value = int64(2030)
	if got := document.FilterDefinitions["year"].Options.Values[0].Value.Value; got != int64(2026) {
		t.Fatalf("nested interface value aliased source: %#v", got)
	}
}
