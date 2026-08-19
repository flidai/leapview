package authoring

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
)

func canonicalStringPtr(value string) *string { return &value }

func canonicalAuthoringDocument() document.DashboardDocument {
	return document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: "dashboard:sales", Name: "sales"},
		Spec: document.DashboardSpec{
			SemanticModel: "sales",
			Filters:       []document.DashboardFilter{},
			Visuals: map[string]document.DashboardVisual{
				"revenue": {
					Type:         document.DashboardVisualTypeBar,
					Query:        document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: canonicalStringPtr("month")}}, Metrics: []document.DashboardMetricSelection{{String: canonicalStringPtr("revenue")}}}},
					Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian"}},
				},
			},
			Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{}}},
		},
	}
}

func TestRevisionRetainsCanonicalDocumentAndHash(t *testing.T) {
	doc := canonicalAuthoringDocument()
	created, err := NewRevision("revision-1", "dashboard:sales", 1, time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), doc, Provenance{Origin: OriginUI, ActorID: "actor"})
	if err != nil {
		t.Fatalf("NewRevision() error = %v", err)
	}
	if err := created.Validate(); err != nil {
		t.Fatalf("Revision.Validate() error = %v", err)
	}
	if created.ContentHash == "" || created.Document.Metadata.ID != "dashboard:sales" || created.Document.Spec.SemanticModel != "sales" {
		t.Fatalf("revision lost canonical identity: %#v", created)
	}
	if created.Token().ContentHash != created.ContentHash {
		t.Fatalf("revision token hash = %q, want %q", created.Token().ContentHash, created.ContentHash)
	}
}

func TestValidateCanonicalDocumentRejectsMissingVisualPresentation(t *testing.T) {
	doc := canonicalAuthoringDocument()
	visual := doc.Spec.Visuals["revenue"]
	visual.Presentation = document.DashboardPresentation{}
	doc.Spec.Visuals["revenue"] = visual
	if err := ValidateCanonicalDocument(doc); err == nil {
		t.Fatal("canonical document without presentation unexpectedly validated")
	}
}

func TestLifecycleTransitionsPointersAndProvenanceIsolation(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	if lifecycle.Status != LifecycleStatusDraft || lifecycle.Draft == nil || lifecycle.Draft.Revision != current.Token() {
		t.Fatalf("draft lifecycle pointers = %#v", lifecycle)
	}
	for _, transition := range [][2]LifecycleStatus{{LifecycleStatusDraft, LifecycleStatusPublished}, {LifecycleStatusPublished, LifecycleStatusArchived}} {
		if err := ValidateTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("ValidateTransition(%q, %q): %v", transition[0], transition[1], err)
		}
	}
	if err := ValidateTransition(LifecycleStatusArchived, LifecycleStatusDraft); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("invalid archive transition error = %v", err)
	}
	provenance := Provenance{Origin: OriginAgent, ActorID: "actor", Source: &SourceMetadata{Path: "dashboards/sales.yaml", Metadata: map[string]string{"channel": "agent"}}, ForkedFrom: &ForkEvidence{Kind: ForkSourceProject, Project: &ProjectForkEvidence{SourceProjectID: "project:test", SourceDashboardID: "dashboard:source", Identity: servingIdentityForTest()}}}
	revision, err := NewRevision("rev-isolated", "dashboard:test", 2, time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC), current.Document, provenance)
	if err != nil {
		t.Fatal(err)
	}
	provenance.Source.Metadata["channel"] = "mutated"
	provenance.ForkedFrom.Project.Path = "mutated"
	if revision.Provenance.Source.Metadata["channel"] != "agent" || revision.Provenance.ForkedFrom.Project.Path != "" {
		t.Fatalf("revision provenance aliases input: %#v", revision.Provenance)
	}
	if revision.ContentHash == "" || revision.Token().ContentHash != revision.ContentHash {
		t.Fatalf("revision hash/token mismatch: %#v", revision)
	}
	clone, err := revision.Document.Clone()
	if err != nil {
		t.Fatal(err)
	}
	clone.Metadata.Name = "mutated"
	if revision.Document.Metadata.Name == "mutated" {
		t.Fatal("revision document clone aliases stored document")
	}
}

func TestLifecycleJSONIsClosedAndContentHashStable(t *testing.T) {
	lifecycle, revision := canonicalReducerFixture(t)
	encoded, err := json.Marshal(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DashboardLifecycle
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, lifecycle) {
		t.Fatalf("lifecycle JSON round trip changed value: %#v != %#v", decoded, lifecycle)
	}
	legacy := append([]byte(`{"workspaceId":"legacy",`), encoded[1:]...)
	if err := json.Unmarshal(legacy, &DashboardLifecycle{}); err == nil {
		t.Fatal("legacy workspace scope unexpectedly accepted")
	}
	first, err := DashboardContentHash(revision.Document)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DashboardContentHash(revision.Document)
	if err != nil || first != second {
		t.Fatalf("content hash is not deterministic: %q/%q (%v)", first, second, err)
	}
}

func servingIdentityForTest() graph.ServingIdentity {
	identity, _ := graph.NewServingIdentity("project:test", "test", "generation:test")
	return identity
}
