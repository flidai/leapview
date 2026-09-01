package authoring

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestCanonicalVisualTypeSwitchMatrixAuthorsTargetQueryFamily(t *testing.T) {
	bindings := &VisualTypeFieldBindings{
		Dimensions: []string{"category", "purchase_month"},
		Metrics:    []string{"revenue", "order_count", "margin", "cost"},
		Dataset:    "sales_orders",
		Details:    []string{"category", "revenue", "order_id"},
	}
	for _, source := range CanonicalVisualCatalog() {
		for _, target := range CanonicalVisualCatalog() {
			t.Run(string(source.Type)+"_to_"+string(target.Type), func(t *testing.T) {
				_, revision := canonicalReducerFixture(t)
				visual := defaultCanonicalVisual(string(source.Type), "Base")
				revision.Document.Spec.Visuals["base"] = visual

				if err := setCanonicalVisualType(&revision.Document, SetVisualTypePayload{PageID: "overview", VisualID: "base-component", Type: target.Type, ResolvedBindings: bindings}); err != nil {
					t.Fatal(err)
				}
				switched := revision.Document.Spec.Visuals["base"]
				if switched.Type != target.Type {
					t.Fatalf("switched type = %q, want %q", switched.Type, target.Type)
				}
				expected := defaultCanonicalVisual(string(target.Type), "Base")
				if reflect.TypeOf(switched.Query.Value) != reflect.TypeOf(expected.Query.Value) {
					t.Fatalf("switched query family = %T, want %T", switched.Query.Value, expected.Query.Value)
				}
				if source.Type == target.Type {
					return
				}
				resolved := canonicalVisualSwitchBindings(switched.Query)
				counts := map[string]int{
					string(FieldRoleDimension): len(resolved.Dimensions),
					string(FieldRoleMetric):    len(resolved.Metrics),
					string(FieldRoleDetail):    len(resolved.Details),
				}
				for _, limit := range CanonicalVisualRoleLimits(target.Type) {
					if int32(counts[limit.Role]) < limit.Minimum {
						t.Fatalf("switched %s fields = %d, want at least %d", limit.Role, counts[limit.Role], limit.Minimum)
					}
					if limit.Maximum > 0 && int32(counts[limit.Role]) > limit.Maximum {
						t.Fatalf("switched %s fields = %d, want at most %d", limit.Role, counts[limit.Role], limit.Maximum)
					}
				}
			})
		}
	}
}

func TestCanonicalVisualTypeSwitchConfiguresScatterFromResolvedBindings(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	category, revenue, orders := "category", "revenue", "order_count"
	visual := revision.Document.Spec.Visuals["base"]
	visual.Query.Value = &document.AggregateDashboardQuery{
		DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate",
		Dimensions: []document.DashboardDimensionSelection{{String: &category}},
		Metrics:    []document.DashboardMetricSelection{{String: &revenue}, {String: &orders}},
	}
	revision.Document.Spec.Visuals["base"] = visual
	if err := setCanonicalVisualType(&revision.Document, SetVisualTypePayload{PageID: "overview", VisualID: "base-component", Type: document.DashboardVisualTypeScatter, ResolvedBindings: &VisualTypeFieldBindings{Dimensions: []string{category}, Metrics: []string{revenue, orders}}}); err != nil {
		t.Fatal(err)
	}
	scatter := revision.Document.Spec.Visuals["base"]
	presentation, ok := scatter.Presentation.Value.(*document.PointDashboardPresentation)
	if !ok || len(presentation.Identity) != 1 || presentation.Identity[0] != category || presentation.X != revenue || presentation.Y != orders {
		t.Fatalf("scatter presentation = %#v", scatter.Presentation)
	}
	query := scatter.Query.Value.(*document.AggregateDashboardQuery)
	if len(query.Dimensions) != 1 || len(query.Metrics) != 2 {
		t.Fatalf("scatter query lost resolved bindings: %#v", query)
	}
}

func TestCanonicalDonutWithLegacyRecordsQueryRepairsToEditableAggregateQuery(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	revenue, orderID, customerID := "revenue", "order_id", "customer_id"
	visual := defaultCanonicalVisual(string(document.DashboardVisualTypeTable), "Orders")
	// Older builder revisions could change only the renderer type and leave the
	// records query behind. Re-selecting the active type must repair that draft.
	visual.Type = document.DashboardVisualTypeDonut
	visual.Query.Value = &document.RecordsDashboardQuery{
		DashboardQueryBase: document.DashboardQueryBase{Type: "records"}, Type: "records", Dataset: "sales_orders",
		Fields: []document.DashboardRecordFieldSelection{{String: &revenue}, {String: &orderID}, {String: &customerID}},
	}
	revision.Document.Spec.Visuals["base"] = visual
	if err := setCanonicalVisualType(&revision.Document, SetVisualTypePayload{
		PageID: "overview", VisualID: "base-component", Type: document.DashboardVisualTypeDonut,
		ResolvedBindings: &VisualTypeFieldBindings{Metrics: []string{"revenue"}, Dataset: "sales_orders", Details: []string{"revenue", "order_id", "customer_id"}},
	}); err != nil {
		t.Fatal(err)
	}
	query, ok := revision.Document.Spec.Visuals["base"].Query.Value.(*document.AggregateDashboardQuery)
	if !ok || len(query.Dimensions) != 0 || len(query.Metrics) != 1 {
		t.Fatalf("donut query = %#v", revision.Document.Spec.Visuals["base"].Query)
	}
	if metric, _ := canonicalMetricSelection(query.Metrics[0]); metric != "revenue" {
		t.Fatalf("donut metric = %q, want revenue", metric)
	}
	if err := assignCanonicalField(&revision.Document, AssignFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: "category", Role: FieldRoleDimension}); err != nil {
		t.Fatalf("assign category after switch: %v", err)
	}
}

func TestCanonicalPivotAssignmentBuildsRowsAndColumns(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	revision.Document.Spec.Visuals["base"] = defaultCanonicalVisual(string(document.DashboardVisualTypePivot), "Pivot")
	assign := func(field string, role FieldRole) {
		t.Helper()
		if err := assignCanonicalField(&revision.Document, AssignFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: field, Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	assign("category", FieldRoleDimension)
	assign("purchase_month", FieldRoleDimension)
	assign("revenue", FieldRoleMetric)

	query := revision.Document.Spec.Visuals["base"].Query.Value.(*document.PivotDashboardQuery)
	if len(query.Rows) != 1 || len(query.Columns) != 1 || len(query.Metrics) != 1 {
		t.Fatalf("pivot fields = rows:%#v columns:%#v metrics:%#v", query.Rows, query.Columns, query.Metrics)
	}
	row, _ := canonicalDimensionSelection(query.Rows[0])
	column, _ := canonicalDimensionSelection(query.Columns[0])
	if row != "category" || column != "purchase_month" {
		t.Fatalf("pivot axes = %q / %q", row, column)
	}
	visual := revision.Document.Spec.Visuals["base"]
	removed, err := removeFieldFromQuery(&visual.Query, FieldRoleDimension, "purchase_month")
	revision.Document.Spec.Visuals["base"] = visual
	if err != nil || !removed || len(query.Columns) != 0 {
		t.Fatalf("remove pivot column: removed=%v err=%v columns=%#v", removed, err, query.Columns)
	}
}

func TestCanonicalAddVisualSupportsEveryVisualTypeWithNonOverlappingPlacement(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	types := []document.DashboardVisualType{
		document.DashboardVisualTypeLine, document.DashboardVisualTypeArea, document.DashboardVisualTypeBar, document.DashboardVisualTypeColumn,
		document.DashboardVisualTypePie, document.DashboardVisualTypeDonut, document.DashboardVisualTypeScatter, document.DashboardVisualTypeFunnel,
		document.DashboardVisualTypeTreemap, document.DashboardVisualTypeGauge, document.DashboardVisualTypeHeatmap, document.DashboardVisualTypeSankey,
		document.DashboardVisualTypeGraph, document.DashboardVisualTypeMap, document.DashboardVisualTypeCandlestick, document.DashboardVisualTypeBoxplot,
		document.DashboardVisualTypeCombo, document.DashboardVisualTypeWaterfall, document.DashboardVisualTypeHistogram, document.DashboardVisualTypeRadar,
		document.DashboardVisualTypeTree, document.DashboardVisualTypeSunburst, document.DashboardVisualTypeKpi, document.DashboardVisualTypeTable,
		document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot,
	}
	expected := map[document.DashboardVisualType]struct{ query, presentation string }{
		document.DashboardVisualTypeHistogram: {"histogram", "cartesian"},
		document.DashboardVisualTypeBoxplot:   {"distribution", "cartesian"},
		document.DashboardVisualTypeTable:     {"records", "table"},
		document.DashboardVisualTypeMatrix:    {"aggregate", "table"},
		document.DashboardVisualTypePivot:     {"pivot", "table"},
		document.DashboardVisualTypePie:       {"aggregate", "proportional"},
		document.DashboardVisualTypeDonut:     {"aggregate", "proportional"},
		document.DashboardVisualTypeFunnel:    {"aggregate", "proportional"},
		document.DashboardVisualTypeTreemap:   {"aggregate", "hierarchy"},
		document.DashboardVisualTypeSankey:    {"aggregate", "hierarchy"},
		document.DashboardVisualTypeGraph:     {"aggregate", "hierarchy"},
		document.DashboardVisualTypeTree:      {"aggregate", "hierarchy"},
		document.DashboardVisualTypeSunburst:  {"aggregate", "hierarchy"},
		document.DashboardVisualTypeGauge:     {"aggregate", "polar"},
		document.DashboardVisualTypeRadar:     {"aggregate", "polar"},
		document.DashboardVisualTypeMap:       {"records", "geographic"},
		document.DashboardVisualTypeKpi:       {"aggregate", "kpi"},
		document.DashboardVisualTypeScatter:   {"aggregate", "point"},
	}
	for _, typ := range types {
		if _, ok := expected[typ]; !ok {
			expected[typ] = struct{ query, presentation string }{"aggregate", "cartesian"}
		}
	}
	for index, typ := range types {
		command := Command{ID: CommandID("add-" + string(typ)), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(), AddVisual: &AddVisualPayload{PageID: "overview", Type: string(typ)}}
		var next Revision
		var err error
		lifecycle, next, err = ApplyEdit(lifecycle, current, command, RevisionID("rev-"+string(rune('a'+index))), current.Number+1, time.Date(2026, 8, 18, 12, index, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("add %s: %v", typ, err)
		}
		visualID := "visual_" + strconv.Itoa(index+2)
		visual, ok := next.Document.Spec.Visuals[visualID]
		if !ok || visual.Type != typ || visual.Query.Value == nil || visual.Presentation.Value == nil {
			t.Fatalf("add %s produced %#v", typ, visual)
		}
		queryType, err := visual.Query.Type()
		if err != nil {
			t.Fatal(err)
		}
		presentationType, err := visual.Presentation.Type()
		if err != nil {
			t.Fatal(err)
		}
		want := expected[typ]
		if queryType != want.query || presentationType != want.presentation {
			t.Fatalf("add %s query/presentation = %s/%s, want %s/%s", typ, queryType, presentationType, want.query, want.presentation)
		}
		page := next.Document.Spec.Pages[0]
		added := page.Components[len(page.Components)-1]
		placement, err := added.Base()
		if err != nil {
			t.Fatalf("add %s placement: %v", typ, err)
		}
		for _, component := range page.Components[:len(page.Components)-1] {
			other, err := component.Base()
			if err != nil {
				t.Fatal(err)
			}
			if placementsOverlap(placement.Placement, other.Placement) {
				t.Fatalf("add %s overlaps %q", typ, other.ID)
			}
		}
		current = next
	}
}

func TestCanonicalAddVisualBindsGovernedInitialFieldAtomically(t *testing.T) {
	t.Run("metric KPI", func(t *testing.T) {
		lifecycle, current := canonicalReducerFixture(t)
		command := Command{
			ID: "add-metric-kpi", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID,
			ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
			AddVisual: &AddVisualPayload{PageID: "overview", Type: "kpi", Title: "Revenue", FieldID: "revenue", Role: FieldRoleMetric, FieldValidated: true},
		}
		_, next, err := ApplyEdit(lifecycle, current, command, "rev-2", 2, time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		visual := next.Document.Spec.Visuals["visual_2"]
		query, ok := visual.Query.Value.(*document.AggregateDashboardQuery)
		if !ok || visual.Type != document.DashboardVisualTypeKpi || len(query.Metrics) != 1 || query.Metrics[0].String == nil || *query.Metrics[0].String != "revenue" {
			t.Fatalf("initial KPI = %#v query=%#v", visual, query)
		}
		component, err := next.Document.Spec.Pages[0].Components[1].Base()
		if err != nil || component.Placement.ColumnSpan != 4 || component.Placement.RowSpan != 3 {
			t.Fatalf("initial KPI placement = %#v err=%v", component, err)
		}
	})

	t.Run("dimension table", func(t *testing.T) {
		lifecycle, current := canonicalReducerFixture(t)
		command := Command{
			ID: "add-dimension-table", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID,
			ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
			AddVisual: &AddVisualPayload{PageID: "overview", Type: "table", Title: "Status", FieldID: "status", Role: FieldRoleDetail, ResolvedTable: "orders", FieldValidated: true},
		}
		_, next, err := ApplyEdit(lifecycle, current, command, "rev-2", 2, time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		query, ok := next.Document.Spec.Visuals["visual_2"].Query.Value.(*document.RecordsDashboardQuery)
		if !ok || query.Dataset != "orders" || len(query.Fields) != 1 || query.Fields[0].String == nil || *query.Fields[0].String != "status" {
			t.Fatalf("initial table query = %#v", query)
		}
		component, err := next.Document.Spec.Pages[0].Components[1].Base()
		if err != nil || component.Placement.ColumnSpan != 6 || component.Placement.RowSpan != 5 {
			t.Fatalf("initial table placement = %#v err=%v", component, err)
		}
	})

	t.Run("rejects unvalidated wire field", func(t *testing.T) {
		lifecycle, current := canonicalReducerFixture(t)
		command := Command{
			ID: "add-unvalidated", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID,
			ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
			AddVisual: &AddVisualPayload{PageID: "overview", Type: "kpi", FieldID: "revenue", Role: FieldRoleMetric},
		}
		if _, _, err := ApplyEdit(lifecycle, current, command, "rev-2", 2, time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "governed validation") {
			t.Fatalf("unvalidated initial field error = %v", err)
		}
	})
}

func canonicalReducerFixture(t *testing.T) (DashboardLifecycle, Revision) {
	t.Helper()
	baseComponent := document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{
			ID: "base-component", Type: "visual",
			Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 12, RowSpan: 4},
		},
		Type: "visual", Visual: "base",
	}}
	documentValue := document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: "dashboard:test", Name: "test"},
		Spec: document.DashboardSpec{
			SemanticModel: "model",
			Filters:       []document.DashboardFilter{},
			Visuals:       map[string]document.DashboardVisual{"base": defaultCanonicalVisual("bar", "Base")},
			Pages:         []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{baseComponent}}},
		},
	}
	provenance := canonicalReducerProvenance()
	revision, err := NewRevision("rev-1", "dashboard:test", 1, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC), documentValue, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewDashboardLifecycle(NewDashboardLifecycleInput{ProjectID: "project:test", ID: "dashboard:test", OwnerPrincipalID: "owner", Slug: "test", Title: "Test", SemanticModel: "model", Visibility: VisibilityPrivate, Draft: &Draft{ID: "draft-1", DashboardID: "dashboard:test", Revision: revision.Token(), Provenance: provenance}})
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle, revision
}

func TestApplyRevisionRestoreAppendsMonotonicRevisionFromExactTarget(t *testing.T) {
	lifecycle, target := canonicalReducerFixture(t)
	edit := Command{
		ID: "add-page", DashboardID: target.DashboardID, DraftID: lifecycle.Draft.ID,
		ExpectedRevision: target.Token(), Provenance: canonicalReducerProvenance(),
		AddPage: &AddPagePayload{PageID: "details", Title: "Details"},
	}
	lifecycle, current, err := ApplyEdit(lifecycle, target, edit, "rev-2", 2, time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	restore := Command{
		ID: "restore-page", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID,
		ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
		RestoreRevision: &RestoreRevisionPayload{TargetRevision: target.Token()},
	}
	nextLifecycle, restored, err := ApplyRevisionRestore(lifecycle, current, target, restore, "rev-3", 3, time.Date(2026, 8, 18, 12, 2, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if restored.Number != 3 || len(restored.Document.Spec.Pages) != 1 || restored.Document.Spec.Pages[0].ID != "overview" {
		t.Fatalf("restored revision = number %d pages %#v", restored.Number, restored.Document.Spec.Pages)
	}
	if !sameRevisionToken(nextLifecycle.Draft.Revision, restored.Token()) {
		t.Fatalf("draft token = %#v, want %#v", nextLifecycle.Draft.Revision, restored.Token())
	}
	if restored.ContentHash != target.ContentHash {
		t.Fatalf("restored content hash = %q, want %q", restored.ContentHash, target.ContentHash)
	}
}

func canonicalReducerProvenance() Provenance {
	return Provenance{Origin: OriginUI, ActorID: "actor", ConversationID: "conversation", ToolCallID: "tool"}
}

func TestCanonicalReducerAppliesMetadataPageVisualLayoutFiltersInteractionAndFieldAssignment(t *testing.T) {
	apply := func(t *testing.T, lifecycle DashboardLifecycle, current Revision, payload authoringPayload) (DashboardLifecycle, Revision, error) {
		t.Helper()
		command := Command{ID: CommandID("command-" + strconv.FormatUint(current.Number, 10)), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance()}
		return ApplyEdit(lifecycle, current, canonicalReducerCommandWithPayload(command, payload), RevisionID("rev-"+strconv.FormatUint(current.Number+1, 10)), current.Number+1, time.Date(2026, 8, 18, 13, int(current.Number), 0, 0, time.UTC))
	}
	t.Run("metadata and page", func(t *testing.T) {
		lifecycle, current := canonicalReducerFixture(t)
		title, description, slug := "Updated", "Description", "updated"
		lifecycle, current, err := apply(t, lifecycle, current, &MetadataPatch{Title: &title, Description: &description, Slug: &slug})
		if err != nil || lifecycle.Title != title || current.Document.Metadata.DisplayName == nil || *current.Document.Metadata.DisplayName != title || current.Document.Metadata.Description == nil || *current.Document.Metadata.Description != description {
			t.Fatalf("metadata edit = %#v %#v (%v)", lifecycle, current.Document.Metadata, err)
		}
		lifecycle, current, err = apply(t, lifecycle, current, &AddPagePayload{PageID: "details", Title: "Details"})
		if err != nil || len(current.Document.Spec.Pages) != 2 || current.Document.Spec.Pages[1].ID != "details" {
			t.Fatalf("page edit = %#v (%v)", current.Document.Spec.Pages, err)
		}
	})
	t.Run("visual layout filters interaction and assign field", func(t *testing.T) {
		lifecycle, current := canonicalReducerFixture(t)
		var err error
		lifecycle, current, err = apply(t, lifecycle, current, &AddVisualPayload{PageID: "overview", VisualID: "revenue", Type: "bar", Title: "Revenue"})
		if err != nil || current.Document.Spec.Visuals["revenue"].Type != document.DashboardVisualTypeBar {
			t.Fatalf("visual edit = %#v (%v)", current.Document.Spec.Visuals, err)
		}
		columns := int32(16)
		lifecycle, current, err = apply(t, lifecycle, current, &SetLayoutPayload{PageID: "overview", Layout: &document.DashboardLayoutOverride{Columns: &columns}})
		if err != nil || current.Document.Spec.Pages[0].Layout == nil || *current.Document.Spec.Pages[0].Layout.Columns != columns {
			t.Fatalf("layout edit = %#v (%v)", current.Document.Spec.Pages[0].Layout, err)
		}
		lifecycle, current, err = apply(t, lifecycle, current, &SetFiltersPayload{Clear: true})
		if err != nil || current.Document.Spec.Filters == nil || len(current.Document.Spec.Filters) != 0 {
			t.Fatalf("filter clear = %#v (%v)", current.Document.Spec.Filters, err)
		}
		targets := []string{"base"}
		interaction := &document.DashboardInteraction{Value: &document.SelectionDashboardInteraction{Type: "selection", Mode: document.DashboardSelectionModeSingle, Toggle: true, Mappings: []document.DashboardInteractionMapping{{Field: "status", Value: "label"}}, DashboardInteractionBase: document.DashboardInteractionBase{Type: "selection", Targets: &targets}}}
		lifecycle, current, err = apply(t, lifecycle, current, &SetInteractionPayload{PageID: "overview", VisualID: "base", Interaction: interaction})
		if err != nil || current.Document.Spec.Visuals["base"].Interactions == nil || len(*current.Document.Spec.Visuals["base"].Interactions) != 1 {
			t.Fatalf("interaction edit = %#v (%v)", current.Document.Spec.Visuals["base"].Interactions, err)
		}
		lifecycle, current, err = apply(t, lifecycle, current, &AssignFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: "revenue", Role: FieldRoleMetric})
		if err != nil {
			t.Fatalf("assign field error = %v", err)
		}
		query, ok := current.Document.Spec.Visuals["base"].Query.Value.(*document.AggregateDashboardQuery)
		if !ok || len(query.Metrics) != 1 || query.Metrics[0].String == nil || *query.Metrics[0].String != "revenue" {
			t.Fatalf("assigned field query = %#v", current.Document.Spec.Visuals["base"].Query)
		}
		if lifecycle.Draft.Revision != current.Token() {
			t.Fatalf("lifecycle draft pointer = %#v, revision = %#v", lifecycle.Draft.Revision, current.Token())
		}
	})
	t.Run("qualified detail field resolves dataset and stores canonical member", func(t *testing.T) {
		lifecycle, current := canonicalReducerFixture(t)
		var err error
		lifecycle, current, err = apply(t, lifecycle, current, &AddVisualPayload{PageID: "overview", VisualID: "records", ComponentID: "records-component", Type: "table", Title: "Records"})
		if err != nil {
			t.Fatalf("add records visual: %v", err)
		}
		lifecycle, current, err = apply(t, lifecycle, current, &AssignFieldPayload{PageID: "overview", VisualID: "records-component", FieldID: "sales_orders.order_id", Role: FieldRoleDetail, ResolvedTable: "sales_orders"})
		if err != nil {
			t.Fatalf("assign qualified detail field: %v", err)
		}
		query, ok := current.Document.Spec.Visuals["records"].Query.Value.(*document.RecordsDashboardQuery)
		if !ok || query.Dataset != "sales_orders" || len(query.Fields) != 1 || query.Fields[0].String == nil || *query.Fields[0].String != "order_id" {
			t.Fatalf("assigned records query = %#v", current.Document.Spec.Visuals["records"].Query)
		}
	})
}

func TestCanonicalReducerAtomicallyUpdatesComponentPlacements(t *testing.T) {
	apply := func(t *testing.T, lifecycle DashboardLifecycle, current Revision, payload authoringPayload) (DashboardLifecycle, Revision, error) {
		t.Helper()
		command := Command{ID: CommandID("placement-command-" + strconv.FormatUint(current.Number, 10)), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance()}
		return ApplyEdit(lifecycle, current, canonicalReducerCommandWithPayload(command, payload), RevisionID("placement-rev-"+strconv.FormatUint(current.Number+1, 10)), current.Number+1, time.Date(2026, 8, 18, 14, int(current.Number), 0, 0, time.UTC))
	}
	lifecycle, current := canonicalReducerFixture(t)
	var err error
	lifecycle, current, err = apply(t, lifecycle, current, &AddVisualPayload{PageID: "overview", VisualID: "secondary", ComponentID: "secondary-component", Type: "bar", Title: "Secondary"})
	if err != nil {
		t.Fatal(err)
	}
	placements := &SetPlacementsPayload{PageID: "overview", Placements: []PlacementUpdate{
		{ComponentID: "base-component", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 4}},
		{ComponentID: "secondary-component", Placement: document.DashboardPlacement{Column: 7, Row: 1, ColumnSpan: 6, RowSpan: 4}},
	}}
	lifecycle, current, err = apply(t, lifecycle, current, placements)
	if err != nil {
		t.Fatalf("atomic placement update: %v", err)
	}
	for _, component := range current.Document.Spec.Pages[0].Components {
		base, baseErr := component.Base()
		if baseErr != nil {
			t.Fatal(baseErr)
		}
		switch base.ID {
		case "base-component":
			if base.Placement.Column != 1 || base.Placement.ColumnSpan != 6 {
				t.Fatalf("base placement = %#v", base.Placement)
			}
		case "secondary-component":
			if base.Placement.Column != 7 || base.Placement.ColumnSpan != 6 {
				t.Fatalf("secondary placement = %#v", base.Placement)
			}
		}
	}
	before := current
	_, _, err = apply(t, lifecycle, current, &SetPlacementsPayload{PageID: "overview", Placements: []PlacementUpdate{
		{ComponentID: "base-component", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 7, RowSpan: 4}},
		{ComponentID: "secondary-component", Placement: document.DashboardPlacement{Column: 7, Row: 1, ColumnSpan: 6, RowSpan: 4}},
	}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("overlapping placement error = %v, want conflict", err)
	}
	if got, _ := before.Document.Spec.Pages[0].Components[0].Base(); got.Placement.ColumnSpan != 6 {
		t.Fatalf("failed atomic update mutated base placement = %#v", got.Placement)
	}
	_, _, err = apply(t, lifecycle, current, &SetPlacementsPayload{PageID: "overview", Placements: []PlacementUpdate{{ComponentID: "base-component", Placement: document.DashboardPlacement{Column: 13, Row: 1, ColumnSpan: 1, RowSpan: 1}}}})
	if err == nil || !strings.Contains(err.Error(), "exceed grid") {
		t.Fatalf("out-of-grid placement error = %v", err)
	}
}

func TestCanonicalReducerSelectedVisualEditingCommands(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	apply := func(payload authoringPayload) error {
		command := Command{ID: CommandID("selected-edit-" + strconv.FormatUint(current.Number, 10)), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance()}
		var err error
		lifecycle, current, err = ApplyEdit(lifecycle, current, canonicalReducerCommandWithPayload(command, payload), RevisionID("selected-rev-"+strconv.FormatUint(current.Number+1, 10)), current.Number+1, time.Date(2026, 8, 18, 15, int(current.Number), 0, 0, time.UTC))
		return err
	}
	if err := apply(&AssignFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: "revenue", Role: FieldRoleMetric}); err != nil {
		t.Fatal(err)
	}
	if err := apply(&SetVisualTypePayload{PageID: "overview", VisualID: "base-component", Type: document.DashboardVisualTypeColumn}); err != nil {
		t.Fatal(err)
	}
	if cartesian, ok := current.Document.Spec.Visuals["base"].Presentation.Value.(*document.CartesianDashboardPresentation); !ok || cartesian.Orientation == nil || *cartesian.Orientation != document.DashboardOrientationVertical {
		t.Fatalf("column orientation = %#v", current.Document.Spec.Visuals["base"].Presentation)
	}
	if err := apply(&SetVisualTypePayload{PageID: "overview", VisualID: "base-component", Type: document.DashboardVisualTypeLine}); err != nil {
		t.Fatal(err)
	}
	if got := current.Document.Spec.Visuals["base"].Type; got != document.DashboardVisualTypeLine {
		t.Fatalf("visual type = %q", got)
	}
	if query, ok := current.Document.Spec.Visuals["base"].Query.Value.(*document.AggregateDashboardQuery); !ok || len(query.Metrics) != 1 {
		t.Fatalf("compatible metric was not preserved: %#v", current.Document.Spec.Visuals["base"].Query)
	}
	if err := apply(&RenameVisualPayload{PageID: "overview", VisualID: "base-component", Title: "Revenue trend"}); err != nil {
		t.Fatal(err)
	}
	showTitle, showLegend, showAxes, showLabels := false, false, false, false
	if err := apply(&UpdateVisualFormatPayload{PageID: "overview", VisualID: "base-component", TitleVisible: &showTitle, LegendVisible: &showLegend, AxisVisible: &showAxes, DataLabelsVisible: &showLabels}); err != nil {
		t.Fatal(err)
	}
	base := current.Document.Spec.Visuals["base"]
	if base.Title == nil || *base.Title != "Revenue trend" || base.TitleVisible == nil || *base.TitleVisible {
		t.Fatalf("title format = %#v", base)
	}
	presentation, ok := base.Presentation.Value.(*document.CartesianDashboardPresentation)
	if !ok || presentation.Legend == nil || *presentation.Legend != document.DashboardLegendPositionNone || presentation.Labels == nil || presentation.Labels.Density != document.DashboardLabelDensityHidden {
		t.Fatalf("visual format = %#v", base.Presentation)
	}
	if basePresentation, _ := base.Presentation.Base(); basePresentation == nil || basePresentation.AxisVisible == nil || *basePresentation.AxisVisible {
		t.Fatalf("axis format = %#v", base.Presentation)
	}
	stacking := "percent"
	if err := apply(&UpdateVisualFormatPayload{PageID: "overview", VisualID: "base-component", FormatKey: "stacking", FormatValue: &stacking}); err != nil {
		t.Fatal(err)
	}
	base = current.Document.Spec.Visuals["base"]
	presentation, ok = base.Presentation.Value.(*document.CartesianDashboardPresentation)
	if !ok || presentation.Stacking == nil || *presentation.Stacking != document.DashboardStackingModePercent {
		t.Fatalf("contract format = %#v", base.Presentation)
	}
	if err := apply(&DuplicateVisualPayload{PageID: "overview", VisualID: "base-component"}); err != nil {
		t.Fatal(err)
	}
	if len(current.Document.Spec.Visuals) != 2 || len(current.Document.Spec.Pages[0].Components) != 2 {
		t.Fatalf("duplicate counts = %d/%d", len(current.Document.Spec.Visuals), len(current.Document.Spec.Pages[0].Components))
	}
	if err := apply(&AddFilterPayload{Label: "Status", Dimension: "status", Dataset: "orders", ControlType: "multiSelect"}); err != nil {
		t.Fatal(err)
	}
	filterID := current.Document.Spec.Filters[0].ID
	if err := apply(&SetFilterScopePayload{FilterID: filterID, Scope: "page", PageID: "overview", Targets: []string{"base-component"}}); err != nil {
		t.Fatal(err)
	}
	if err := apply(&RemoveVisualPayload{PageID: "overview", VisualID: "base-component"}); err != nil {
		t.Fatal(err)
	}
	if len(current.Document.Spec.Pages[0].Components) != 1 || len(current.Document.Spec.Visuals) != 1 {
		t.Fatalf("remove counts = %d/%d", len(current.Document.Spec.Visuals), len(current.Document.Spec.Pages[0].Components))
	}
	if len(current.Document.Spec.Filters) != 0 || current.Document.Spec.Pages[0].FilterBindings != nil {
		t.Fatalf("removed visual retained attached page filter: filters=%#v bindings=%#v", current.Document.Spec.Filters, current.Document.Spec.Pages[0].FilterBindings)
	}
}

func TestCanonicalReducerRemovesAndMovesSelectedFields(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	apply := func(payload authoringPayload) error {
		command := Command{ID: CommandID("field-edit-" + strconv.FormatUint(current.Number, 10)), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance()}
		var err error
		lifecycle, current, err = ApplyEdit(lifecycle, current, canonicalReducerCommandWithPayload(command, payload), RevisionID("field-rev-"+strconv.FormatUint(current.Number+1, 10)), current.Number+1, time.Date(2026, 8, 18, 16, int(current.Number), 0, 0, time.UTC))
		return err
	}
	for _, field := range []string{"revenue", "orders"} {
		if err := apply(&AssignFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: field, Role: FieldRoleMetric}); err != nil {
			t.Fatal(err)
		}
	}
	if err := apply(&MoveFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: "revenue", Role: FieldRoleMetric, Direction: "down"}); err != nil {
		t.Fatal(err)
	}
	query := current.Document.Spec.Visuals["base"].Query.Value.(*document.AggregateDashboardQuery)
	first, _ := canonicalMetricSelection(query.Metrics[0])
	if first != "orders" {
		t.Fatalf("moved metric order = %#v", query.Metrics)
	}
	if err := apply(&RemoveFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: "orders", Role: FieldRoleMetric}); err != nil {
		t.Fatal(err)
	}
	query = current.Document.Spec.Visuals["base"].Query.Value.(*document.AggregateDashboardQuery)
	if len(query.Metrics) != 1 {
		t.Fatalf("removed metric count = %d", len(query.Metrics))
	}
}

func TestCanonicalReducerAuthorsFiltersWithoutReplacingCodeOnlyProperties(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	apply := func(payload authoringPayload) error {
		command := Command{ID: CommandID("filter-edit-" + strconv.FormatUint(current.Number, 10)), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance()}
		var err error
		lifecycle, current, err = ApplyEdit(lifecycle, current, canonicalReducerCommandWithPayload(command, payload), RevisionID("filter-rev-"+strconv.FormatUint(current.Number+1, 10)), current.Number+1, time.Date(2026, 8, 18, 17, int(current.Number), 0, 0, time.UTC))
		return err
	}
	if err := apply(&AddFilterPayload{Label: "Status", Dimension: "status", Dataset: "orders", ControlType: "multiSelect"}); err != nil {
		t.Fatal(err)
	}
	if len(current.Document.Spec.Filters) != 1 {
		t.Fatalf("filter count = %d", len(current.Document.Spec.Filters))
	}
	filter := current.Document.Spec.Filters[0]
	filterID := filter.ID
	defaultExpression := document.DashboardFilterExpression{Value: &document.NullCheckDashboardFilterExpression{Type: "nullCheck", Operator: document.DashboardFilterOperatorIsNotNull}}
	operators := []document.DashboardFilterOperator{document.DashboardFilterOperatorIn}
	targets := []string{"base"}
	filter.Default, filter.Operators, filter.Targets = &defaultExpression, &operators, &targets
	if err := apply(&SetFiltersPayload{Filters: []document.DashboardFilter{filter}}); err != nil {
		t.Fatal(err)
	}
	if err := apply(&UpdateFilterPayload{FilterID: filterID, Label: "Order status", Dataset: "orders", ControlType: "singleSelect", Required: true, ReaderEditable: false, URLParameter: "status"}); err != nil {
		t.Fatal(err)
	}
	updated := &current.Document.Spec.Filters[0]
	if updated.Label != "Order status" || updated.Default == nil || updated.Operators == nil || updated.Targets == nil || updated.URLParameter == nil || !*updated.Required || *updated.ReaderEditable {
		t.Fatalf("updated filter lost canonical properties: %#v", updated)
	}
	if controlType, _ := updated.Control.Type(); controlType != "singleSelect" {
		t.Fatalf("control type = %q", controlType)
	}
	if err := apply(&AddFilterComponentPayload{PageID: "overview", FilterID: filterID, ComponentID: "status-slicer"}); err != nil {
		t.Fatal(err)
	}
	components := current.Document.Spec.Pages[0].Components
	placed, ok := components[len(components)-1].Value.(*document.FilterDashboardPageComponent)
	if !ok || placed.ID != "status-slicer" || placed.Filter != filterID || placed.Placement.ColumnSpan != 3 || placed.Placement.RowSpan != 2 {
		t.Fatalf("placed filter component = %#v", components[len(components)-1])
	}
	if err := apply(&RemoveFilterComponentPayload{PageID: "overview", ComponentID: "status-slicer"}); err != nil {
		t.Fatal(err)
	}
	if len(current.Document.Spec.Pages[0].Components) != len(components)-1 {
		t.Fatalf("component count after remove = %d", len(current.Document.Spec.Pages[0].Components))
	}
	if err := apply(&AddFilterComponentPayload{PageID: "overview", FilterID: filterID, ComponentID: "status-slicer"}); err != nil {
		t.Fatal(err)
	}
	if err := apply(&SetFiltersPayload{Clear: true}); err != nil {
		t.Fatal(err)
	}
	if len(current.Document.Spec.Filters) != 0 {
		t.Fatalf("filter count after clear = %d", len(current.Document.Spec.Filters))
	}
	for _, component := range current.Document.Spec.Pages[0].Components {
		if filter, ok := component.Value.(*document.FilterDashboardPageComponent); ok && filter.Filter == filterID {
			t.Fatalf("filter replacement left placed component %#v", filter)
		}
	}
	if err := apply(&AddFilterPayload{Label: "Status", Dimension: "status", Dataset: "orders", ControlType: "multiSelect"}); err != nil {
		t.Fatal(err)
	}
	filterID = current.Document.Spec.Filters[0].ID
	if err := apply(&AddFilterComponentPayload{PageID: "overview", FilterID: filterID, ComponentID: "status-slicer"}); err != nil {
		t.Fatal(err)
	}
	if err := apply(&RemoveFilterPayload{FilterID: filterID}); err != nil {
		t.Fatal(err)
	}
	if len(current.Document.Spec.Filters) != 0 {
		t.Fatalf("filter count after remove = %d", len(current.Document.Spec.Filters))
	}
	for _, component := range current.Document.Spec.Pages[0].Components {
		if filter, ok := component.Value.(*document.FilterDashboardPageComponent); ok && filter.Filter == filterID {
			t.Fatalf("filter removal left placed component %#v", filter)
		}
	}
}

func TestCanonicalReducerSetsFilterTargetsAndRestoresAllPages(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	apply := func(payload authoringPayload) error {
		command := Command{ID: CommandID("filter-target-edit-" + strconv.FormatUint(current.Number, 10)), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance()}
		var err error
		lifecycle, current, err = ApplyEdit(lifecycle, current, canonicalReducerCommandWithPayload(command, payload), RevisionID("filter-target-rev-"+strconv.FormatUint(current.Number+1, 10)), current.Number+1, time.Date(2026, 8, 18, 18, int(current.Number), 0, 0, time.UTC))
		return err
	}
	if err := apply(&AddFilterPayload{Label: "Status", Dimension: "status", Dataset: "orders", ControlType: "multiSelect"}); err != nil {
		t.Fatal(err)
	}
	filter := current.Document.Spec.Filters[0]
	// Seed code-owned properties, then narrow targets. The dedicated payload
	// must not replace defaults/operators or any other authored fields.
	defaultExpression := document.DashboardFilterExpression{Value: &document.NullCheckDashboardFilterExpression{Type: "nullCheck", Operator: document.DashboardFilterOperatorIsNotNull}}
	operators := []document.DashboardFilterOperator{document.DashboardFilterOperatorIn}
	targets := []string{"base"}
	filter.Default, filter.Operators, filter.Targets = &defaultExpression, &operators, &targets
	if err := apply(&SetFiltersPayload{Filters: []document.DashboardFilter{filter}}); err != nil {
		t.Fatal(err)
	}
	if err := apply(&SetFilterTargetsPayload{FilterID: filter.ID, Targets: []string{"base"}}); err != nil {
		t.Fatal(err)
	}
	if err := apply(&SetFilterTargetsPayload{FilterID: filter.ID, Targets: []string{"overview/base"}}); err != nil {
		t.Fatalf("qualified report target: %v", err)
	}
	if got := (*current.Document.Spec.Filters[0].Targets)[0]; got != "overview/base" {
		t.Fatalf("qualified report target = %q", got)
	}
	updated := current.Document.Spec.Filters[0]
	if updated.Targets == nil || len(*updated.Targets) != 1 || (*updated.Targets)[0] != "overview/base" {
		t.Fatalf("narrowed targets = %#v", updated.Targets)
	}
	if updated.Default == nil || updated.Operators == nil {
		t.Fatalf("set targets replaced code-owned fields: %#v", updated)
	}
	if err := apply(&SetFilterTargetsPayload{FilterID: filter.ID, Targets: nil}); err != nil {
		t.Fatal(err)
	}
	if current.Document.Spec.Filters[0].Targets != nil {
		t.Fatalf("all-pages target policy = %#v, want nil", current.Document.Spec.Filters[0].Targets)
	}
}

func TestCanonicalReducerMovesFilterBetweenPageAndReportScope(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	apply := func(payload authoringPayload) error {
		command := Command{ID: CommandID("filter-scope-edit-" + strconv.FormatUint(current.Number, 10)), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance()}
		var err error
		lifecycle, current, err = ApplyEdit(lifecycle, current, canonicalReducerCommandWithPayload(command, payload), RevisionID("filter-scope-rev-"+strconv.FormatUint(current.Number+1, 10)), current.Number+1, time.Date(2026, 8, 18, 19, int(current.Number), 0, 0, time.UTC))
		return err
	}
	if err := apply(&AddPagePayload{PageID: "details", Title: "Details"}); err != nil {
		t.Fatal(err)
	}
	if err := apply(&AddFilterPayload{Label: "Status", Dimension: "status", Dataset: "orders", ControlType: "multiSelect"}); err != nil {
		t.Fatal(err)
	}
	filterID := current.Document.Spec.Filters[0].ID
	if err := apply(&SetFilterScopePayload{FilterID: filterID, Scope: "page", PageID: "overview"}); err != nil {
		t.Fatal(err)
	}
	bindings := current.Document.Spec.Pages[0].FilterBindings
	if bindings == nil || len(*bindings) != 1 || (*bindings)[0].ID != filterID || (*bindings)[0].Filter != filterID {
		t.Fatalf("page bindings = %#v", bindings)
	}
	if err := apply(&SetFilterScopePayload{FilterID: filterID, Scope: "page", PageID: "details"}); err != nil {
		t.Fatal(err)
	}
	if current.Document.Spec.Pages[0].FilterBindings == nil || current.Document.Spec.Pages[1].FilterBindings == nil {
		t.Fatalf("page binding was moved instead of reused across pages: %#v", current.Document.Spec.Pages)
	}
	if err := apply(&SetFilterScopePayload{FilterID: filterID, Scope: "report"}); err != nil {
		t.Fatal(err)
	}
	if current.Document.Spec.Pages[0].FilterBindings != nil || current.Document.Spec.Pages[1].FilterBindings != nil {
		t.Fatalf("report scope retained page bindings: %#v", current.Document.Spec.Pages)
	}
	if err := apply(&SetFilterScopePayload{FilterID: filterID, Scope: "page", PageID: "details"}); err != nil {
		t.Fatal(err)
	}
	if err := apply(&RemovePagePayload{PageID: "details"}); err != nil {
		t.Fatal(err)
	}
	if len(current.Document.Spec.Filters) != 0 {
		t.Fatalf("removed page retained its page-scoped filter: %#v", current.Document.Spec.Filters)
	}
}

func TestCanonicalReducerIsolatesRepeatedVisualBeforeComponentScopedFilter(t *testing.T) {
	_, current := canonicalReducerFixture(t)
	doc := current.Document
	doc.Spec.Pages[0].Components = append(doc.Spec.Pages[0].Components, document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "base-copy", Type: "visual", Placement: document.DashboardPlacement{Column: 7, Row: 1, ColumnSpan: 6, RowSpan: 4}}, Type: "visual", Visual: "base",
	}})
	if err := addCanonicalFilter(&doc, AddFilterPayload{FilterID: "status", Label: "Status", Dimension: "status", Dataset: "orders", ControlType: "multiSelect"}); err != nil {
		t.Fatal(err)
	}
	if err := setCanonicalFilterScope(&doc, SetFilterScopePayload{FilterID: "status", Scope: "page", PageID: "overview", Targets: []string{"base-component"}}); err != nil {
		t.Fatal(err)
	}
	first := doc.Spec.Pages[0].Components[0].Value.(*document.VisualDashboardPageComponent).Visual
	second := doc.Spec.Pages[0].Components[1].Value.(*document.VisualDashboardPageComponent).Visual
	if first == second || len(doc.Spec.Visuals) != 2 {
		t.Fatalf("component-scoped filter did not isolate repeated visual: %q/%q visuals=%#v", first, second, doc.Spec.Visuals)
	}
}

func canonicalReducerCommandWithPayload(command Command, payload authoringPayload) Command {
	switch value := payload.(type) {
	case *MetadataPatch:
		command.Metadata = value
	case *AddPagePayload:
		command.AddPage = value
	case *RenamePagePayload:
		command.RenamePage = value
	case *DuplicatePagePayload:
		command.DuplicatePage = value
	case *MovePagePayload:
		command.MovePage = value
	case *UpdatePageLayoutPayload:
		command.UpdatePageLayout = value
	case *RemovePagePayload:
		command.RemovePage = value
	case *AddVisualPayload:
		command.AddVisual = value
	case *SetPlacementsPayload:
		command.SetPlacements = value
	case *AssignFieldPayload:
		command.AssignField = value
	case *SetVisualTypePayload:
		command.SetVisualType = value
	case *RenameVisualPayload:
		command.RenameVisual = value
	case *DuplicateVisualPayload:
		command.DuplicateVisual = value
	case *UpdateVisualFormatPayload:
		command.UpdateVisualFormat = value
	case *RemoveFieldPayload:
		command.RemoveField = value
	case *MoveFieldPayload:
		command.MoveField = value
	case *RemoveVisualPayload:
		command.RemoveVisual = value
	case *SetLayoutPayload:
		command.SetLayout = value
	case *SetFiltersPayload:
		command.SetFilters = value
	case *AddFilterPayload:
		command.AddFilter = value
	case *UpdateFilterPayload:
		command.UpdateFilter = value
	case *SetFilterTargetsPayload:
		command.SetFilterTargets = value
	case *SetFilterScopePayload:
		command.SetFilterScope = value
	case *RemoveFilterPayload:
		command.RemoveFilter = value
	case *AddFilterComponentPayload:
		command.AddFilterComponent = value
	case *RemoveFilterComponentPayload:
		command.RemoveFilterComponent = value
	case *SetInteractionPayload:
		command.SetInteraction = value
	case *SetInteractionTargetPayload:
		command.SetInteractionTarget = value
	default:
		panic("unsupported canonical reducer test payload")
	}
	return command
}
