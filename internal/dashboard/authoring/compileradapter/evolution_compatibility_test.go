package compileradapter_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	configschema "github.com/flidai/leapview/internal/project/schema"
)

// This serialized v1 revision is captured once, with its original authored hash.
// Generating a fresh fixture in the test would conceal persisted-contract drift.
const retainedV1ContentHash = "sha256:82bfec0cf143f474503705bce510d011aa59428702e2a13ba0773dc7be020310"

func loadRetainedV1Revision(t *testing.T) authoring.Revision {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "retained-v1-revision.json"))
	if err != nil {
		t.Fatal(err)
	}
	var revision authoring.Revision
	if err := json.Unmarshal(content, &revision); err != nil {
		t.Fatal(err)
	}
	hash, err := authoring.DashboardContentHash(revision.Document)
	if err != nil {
		t.Fatal(err)
	}
	if hash != retainedV1ContentHash || revision.ContentHash != retainedV1ContentHash {
		t.Fatalf("retained authored content hash = %s, stored = %s, want %s", hash, revision.ContentHash, retainedV1ContentHash)
	}
	if err := revision.Validate(); err != nil {
		t.Fatal(err)
	}
	return revision
}

func compileRetainedV1Document(t *testing.T, doc document.DashboardDocument) definition.Definition {
	t.Helper()
	f := newCompilerFixture(t)
	table := f.model.Tables["orders"]
	table.Dimensions["ordered_at"] = semanticmodel.MetricDimension{Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ}
	f.model.Tables["orders"] = table
	f.model.Dimensions["purchaseDate"] = semanticmodel.SemanticDimension{
		Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, NativeGrain: "day", Grains: []string{"day", "month"},
		Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.ordered_at"}},
	}
	result, err := f.adapter.Compile(t.Context(), "project", "sales_model", doc)
	if err != nil {
		t.Fatal(err)
	}
	return result.Definition
}

func exportRetainedV1Document(t *testing.T, doc document.DashboardDocument) document.DashboardDocument {
	t.Helper()
	content, err := document.EncodeYAML(doc)
	if err != nil {
		t.Fatal(err)
	}
	var decoded document.DashboardDocument
	if err := configschema.DecodeResource(configschema.KindDashboard, "exported-v1.yaml", content, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func assertRetainedV1Meaning(t *testing.T, compiled definition.Definition) {
	t.Helper()
	binding := compiled.Visualizations["trend"].Query
	want := visualizationdefinition.AggregateQueryBinding{
		TableID:    "orders",
		Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "purchaseDate", Alias: "purchaseMonth", Grain: "month"}},
		Series:     &visualizationdefinition.FieldBinding{FieldID: "status", Alias: "orderStatus"},
		Metrics:    []visualizationdefinition.FieldBinding{{FieldID: "order_count", Alias: "orders"}},
		Sort:       []visualizationdefinition.Sort{{FieldID: "purchaseMonth", Direction: "asc"}}, Limit: 24,
	}
	if binding.Kind != visualizationdefinition.QueryAggregate || !reflect.DeepEqual(binding.Aggregate, &want) {
		t.Fatalf("retained query meaning = %#v, want %#v", binding, want)
	}
	spec, ok := compiled.Visualizations["trend"].Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if !ok || spec.Presentation.Smooth || spec.Presentation.Legend != visualizationir.VisualizationLegendPositionRight ||
		spec.X.Field != "purchaseMonth" || len(spec.Y) != 1 || spec.Y[0].Field != "orders" || spec.Series == nil || spec.Series.Field != "orderStatus" {
		t.Fatalf("retained presentation meaning = %#v", compiled.Visualizations["trend"].Spec.Value)
	}
	if compiled.Layout == nil || compiled.Layout.Defaults != (definition.LayoutDefaults{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16}) {
		t.Fatalf("retained default grid changed: %#v", compiled.Layout)
	}
	if len(compiled.Pages) != 2 || compiled.Pages[0].ID != "overview" || compiled.Pages[1].ID != "detail" {
		t.Fatalf("retained page order changed: %#v", compiled.Pages)
	}
	overview, detail := compiled.Pages[0], compiled.Pages[1]
	if len(overview.Visuals) != 2 || overview.Visuals[0].ID != "trend-card" || overview.Visuals[1].ID != "total-card" ||
		overview.Visuals[0].Visual != "trend" || overview.Visuals[1].Visual != "total" ||
		overview.Visuals[0].Placement != (dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 8, RowSpan: 4}) ||
		overview.Visuals[1].Placement != (dashboard.PagePlacement{Col: 9, Row: 1, ColSpan: 4, RowSpan: 2}) ||
		overview.Grid != (dashboard.PageGrid{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16}) || overview.Height != 272 {
		t.Fatalf("retained overview placements changed: %#v", overview)
	}
	if len(detail.Visuals) != 1 || detail.Visuals[0].ID != "trend-detail" || detail.Visuals[0].Visual != "trend" ||
		detail.Visuals[0].Placement != (dashboard.PagePlacement{Col: 1, Row: 6, ColSpan: 12, RowSpan: 3}) ||
		detail.Grid != (dashboard.PageGrid{Columns: 12, RowHeight: 48, Gap: 0, Padding: 0}) || detail.Height != 384 {
		t.Fatalf("retained reuse/zero overrides/empty rows changed: %#v", detail)
	}
}

func TestRetainedV1RevisionExportEditAndRestorePreserveCompiledMeaning(t *testing.T) {
	retained := loadRetainedV1Revision(t)
	before := compileRetainedV1Document(t, retained.Document)
	assertRetainedV1Meaning(t, before)
	exported := exportRetainedV1Document(t, retained.Document)
	hash, err := authoring.DashboardContentHash(exported)
	if err != nil || hash != retained.ContentHash {
		t.Fatalf("exported authored hash = %s (%v)", hash, err)
	}
	if after := compileRetainedV1Document(t, exported); !reflect.DeepEqual(after, before) {
		t.Fatal("retained YAML export changed compiled meaning")
	}
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "fixture-editor"}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: "project", ID: retained.DashboardID, OwnerPrincipalID: "fixture-editor", Slug: "sales", Title: "Sales",
		SemanticModel: "sales_model", Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: "draft", DashboardID: retained.DashboardID, Revision: retained.Token(), Provenance: provenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	edit := authoring.Command{ID: "rename-overview", DashboardID: retained.DashboardID, DraftID: "draft", ExpectedRevision: retained.Token(), Provenance: provenance,
		RenamePage: &authoring.RenamePagePayload{PageID: "overview", Title: "Executive overview"}}
	lifecycle, edited, err := authoring.ApplyEdit(lifecycle, retained, edit, "revision-edited", 8, retained.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := retained.Document.Clone()
	if err != nil {
		t.Fatal(err)
	}
	expected.Spec.Pages[0].Title = "Executive overview"
	if !reflect.DeepEqual(edited.Document, expected) {
		t.Fatal("unrelated edit changed retained authored meaning")
	}
	assertRetainedV1Meaning(t, compileRetainedV1Document(t, edited.Document))
	restore := authoring.Command{ID: "restore-v1", DashboardID: retained.DashboardID, DraftID: "draft", ExpectedRevision: edited.Token(), Provenance: provenance,
		RestoreRevision: &authoring.RestoreRevisionPayload{TargetRevision: retained.Token()}}
	lifecycle, restored, err := authoring.ApplyRevisionRestore(lifecycle, edited, retained, restore, "revision-restored", 9, retained.CreatedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if restored.ContentHash != retained.ContentHash || restored.Number != 9 || restored.ID == retained.ID || lifecycle.Draft.Revision != restored.Token() {
		t.Fatalf("restore did not append exact retained content: %#v", restored)
	}
	serialized, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	var reread authoring.Revision
	if err := json.Unmarshal(serialized, &reread); err != nil {
		t.Fatal(err)
	}
	if err := reread.Validate(); err != nil {
		t.Fatal(err)
	}
	if after := compileRetainedV1Document(t, exportRetainedV1Document(t, reread.Document)); !reflect.DeepEqual(after, before) {
		t.Fatal("restored persisted/exported v1 revision changed compiled meaning")
	}
}

func TestRetainedV1OmittedAndExplicitDefaultsPreserveDistinctAuthoredIdentity(t *testing.T) {
	retained := loadRetainedV1Revision(t)
	omitted := retained.Document
	explicit, err := omitted.Clone()
	if err != nil {
		t.Fatal(err)
	}
	explicit.Spec.Layout = &document.DashboardLayoutDefaults{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16}
	explicitRevision, err := authoring.NewRevision("revision-explicit", retained.DashboardID, 8, retained.CreatedAt.Add(time.Minute), explicit, retained.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	if explicitRevision.ContentHash == retained.ContentHash {
		t.Fatal("explicit defaults erased authored identity distinction")
	}
	before := compileRetainedV1Document(t, omitted)
	beforeHash, err := authoring.DefinitionHash(before)
	if err != nil {
		t.Fatal(err)
	}
	for name, revision := range map[string]authoring.Revision{"omitted": retained, "explicit": explicitRevision} {
		t.Run(name, func(t *testing.T) {
			serialized, err := json.Marshal(revision)
			if err != nil {
				t.Fatal(err)
			}
			var reread authoring.Revision
			if err := json.Unmarshal(serialized, &reread); err != nil {
				t.Fatal(err)
			}
			if err := reread.Validate(); err != nil {
				t.Fatal(err)
			}
			exported := exportRetainedV1Document(t, reread.Document)
			if (exported.Spec.Layout == nil) != (name == "omitted") {
				t.Fatal("export materialized or erased authored defaults")
			}
			hash, err := authoring.DashboardContentHash(exported)
			if err != nil || hash != revision.ContentHash {
				t.Fatalf("authored hash changed: %s (%v)", hash, err)
			}
			after := compileRetainedV1Document(t, exported)
			afterHash, err := authoring.DefinitionHash(after)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) || afterHash != beforeHash {
				t.Fatal("equivalent omitted/explicit defaults changed serving meaning")
			}
		})
	}
}
