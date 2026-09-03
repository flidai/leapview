package query

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestPlanResultDependenciesUseValidatedPlanIR(t *testing.T) {
	model := testModel()
	digest, err := SemanticModelDigest(model)
	if err != nil || len(digest) != len("sha256:")+64 || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("SemanticModelDigest() = %q, %v", digest, err)
	}
	plan, err := mustNewCompiledPlanner(t, model).Plan(Request{Metrics: []Field{
		{Field: "revenue"}, {Field: "tag_count"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := plan.ResultDependencies()
	if err != nil {
		t.Fatalf("ResultDependencies() error = %v", err)
	}
	if got := strings.Join(projection.Datasets, ","); got != "orders,tags" {
		t.Fatalf("datasets = %q, want orders,tags", got)
	}
	if len(projection.PlannerDigest) != len("sha256:")+64 || !strings.HasPrefix(projection.PlannerDigest, "sha256:") {
		t.Fatalf("planner digest = %q", projection.PlannerDigest)
	}
	projection.Datasets[0] = "mutated"
	again, err := plan.ResultDependencies()
	if err != nil || again.Datasets[0] == "mutated" {
		t.Fatalf("ResultDependencies() exposed mutable plan state: %#v, %v", again, err)
	}
}

func TestSemanticModelDigestFailsClosedWithoutExecutionSnapshot(t *testing.T) {
	if _, err := SemanticModelDigest(nil); err == nil {
		t.Fatal("SemanticModelDigest(nil) error = nil")
	}
}

func TestSemanticModelDigestIgnoresPresentationOnlyChanges(t *testing.T) {
	base := testModel()
	baseTable := base.Tables["orders"]
	baseTable.Columns = map[string]semanticmodel.ModelColumn{
		"revenue": {Name: "revenue", Datatype: semanticmodel.DataTypeDecimal},
	}
	baseTable.Schema.Columns = []semanticmodel.ColumnSchema{{Name: "revenue"}}
	base.Tables["orders"] = baseTable
	changed := base.ExecutionSnapshot()
	changed.Title = "Commerce dashboard"
	changed.Description = "Presentation copy"
	dataset := changed.Datasets["orders"]
	dataset.DisplayName = "Order facts"
	dataset.Description = "Displayed to dashboard authors"
	changed.Datasets["orders"] = dataset
	table := changed.Tables["orders"]
	table.Description = "Order table help"
	dimension := table.Dimensions["revenue"]
	dimension.Label = "Revenue"
	dimension.Description = "Formatted revenue"
	table.Dimensions["revenue"] = dimension
	entity := table.Entities["order"]
	entity.Description = "Business order"
	table.Entities["order"] = entity
	column := table.Columns["revenue"]
	column.Description = "Column help"
	table.Columns["revenue"] = column
	table.Schema.Columns[0].Comment = "Warehouse comment"
	changed.Tables["orders"] = table
	semanticDimension := changed.Dimensions["customer_state"]
	semanticDimension.Label = "Customer state"
	semanticDimension.Description = "Displayed dimension help"
	changed.Dimensions["customer_state"] = semanticDimension
	metric := changed.Metrics["revenue"]
	metric.Label = "Net revenue"
	metric.Description = "Displayed metric help"
	metric.Unit = "USD"
	metric.Format = "currency"
	metric.Hidden = true
	changed.Metrics["revenue"] = metric
	changed.Relationships[0].Description = "Displayed relationship help"

	baseDigest, err := SemanticModelDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	changedDigest, err := SemanticModelDigest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedDigest != baseDigest {
		t.Fatalf("presentation-only changes rotated digest: %q != %q", changedDigest, baseDigest)
	}
}

func TestSemanticModelDigestCanonicalizesNilAndEmptyOptionalCollections(t *testing.T) {
	nilCollections := testModel().ExecutionSnapshot()
	emptyCollections := nilCollections.ExecutionSnapshot()

	nilCollections.Filters = nil
	emptyCollections.Filters = map[string]semanticmodel.SemanticFilterSpec{}

	nilMetric := nilCollections.Metrics["revenue"]
	nilMetric.Where = nil
	nilCollections.Metrics["revenue"] = nilMetric
	emptyMetric := emptyCollections.Metrics["revenue"]
	emptyMetric.Where = []string{}
	emptyCollections.Metrics["revenue"] = emptyMetric

	nilTable := nilCollections.Tables["orders"]
	nilTable.Schema.Columns = nil
	nilTable.SourceDependencies = nil
	nilCollections.Tables["orders"] = nilTable
	emptyTable := emptyCollections.Tables["orders"]
	emptyTable.Schema.Columns = []semanticmodel.ColumnSchema{}
	emptyTable.SourceDependencies = []string{}
	emptyCollections.Tables["orders"] = emptyTable

	nilDigest, err := SemanticModelDigest(nilCollections)
	if err != nil {
		t.Fatal(err)
	}
	emptyDigest, err := SemanticModelDigest(emptyCollections)
	if err != nil {
		t.Fatal(err)
	}
	if nilDigest != emptyDigest {
		t.Fatalf("nil and empty optional collections produced different digests: %q != %q", nilDigest, emptyDigest)
	}
}

func TestSemanticModelDigestIsDeterministicForSameNormalizedExecutionModel(t *testing.T) {
	base := testModel()
	detachedExecution := base.ExecutionSnapshot()
	first, err := SemanticModelDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SemanticModelDigest(detachedExecution)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same normalized execution model produced different digests: %q != %q", first, second)
	}
}

func TestSemanticModelDigestRotatesForMeaningfulExecutionChange(t *testing.T) {
	base := testModel()
	changed := base.ExecutionSnapshot()
	metric := changed.Metrics["revenue"]
	metric.Aggregation = "avg"
	changed.Metrics["revenue"] = metric

	baseDigest, err := SemanticModelDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	changedDigest, err := SemanticModelDigest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedDigest == baseDigest {
		t.Fatal("meaningful metric aggregation change did not rotate semantic model digest")
	}
}

func TestPlanResultDependenciesFailsClosedWithoutPlanIR(t *testing.T) {
	if _, err := (Plan{SQL: "select 1"}).ResultDependencies(); err == nil {
		t.Fatal("ResultDependencies() error = nil")
	}
}

func TestPlanDeterminismRequiresPlannerProducedPlanIR(t *testing.T) {
	model := testModel()
	planner := mustNewCompiledPlanner(t, model)
	plan, err := planner.Plan(Request{Dataset: "orders", Dimensions: []Field{{Field: "orders.order_id"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Deterministic {
		t.Fatal("planner-produced PlanIR was not marked deterministic")
	}
	if (Plan{SQL: "SELECT random()"}).Deterministic {
		t.Fatal("opaque SQL plan was marked deterministic")
	}
}

func TestPlanResultDependenciesIgnoreSnapshotQualifiedRelations(t *testing.T) {
	request := Request{
		Dimensions: []Field{{Field: "customer_state"}},
		Metrics:    []Field{{Field: "order_count"}},
	}
	planForSnapshot := func(snapshot string) Plan {
		planner := mustNewCompiledPlanner(t, testModel(), WithTableRelation(func(table string) (string, error) {
			return "snapshot_" + snapshot + "." + table, nil
		}))
		plan, err := planner.Plan(request)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	first := planForSnapshot("17")
	second := planForSnapshot("18")
	if first.SQL == second.SQL {
		t.Fatal("executable SQL did not retain distinct snapshot targets")
	}
	firstProjection, err := first.ResultDependencies()
	if err != nil {
		t.Fatal(err)
	}
	secondProjection, err := second.ResultDependencies()
	if err != nil {
		t.Fatal(err)
	}
	if firstProjection.PlannerDigest != secondProjection.PlannerDigest {
		t.Fatalf("snapshot target rotated planner identity: %q != %q", firstProjection.PlannerDigest, secondProjection.PlannerDigest)
	}
	firstExecution, err := first.IR.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	secondExecution, err := second.IR.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if firstExecution == secondExecution {
		t.Fatal("executable PlanIR fingerprints no longer distinguish targets")
	}
}

func TestBundleBranchesExposeIndependentDependencyProjections(t *testing.T) {
	bundle, err := mustNewCompiledPlanner(t, executableMultiDatasetModel()).PlanBundle([]BundleRequest{
		{ID: "orders", Request: Request{Dataset: "orders", Metrics: []Field{{Field: "order_count"}}}},
		{ID: "tags", Request: Request{Dataset: "tags", Metrics: []Field{{Field: "tag_count"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Branches) != 2 {
		t.Fatalf("branches = %d, want 2", len(bundle.Branches))
	}
	if got := strings.Join(bundle.Branches[0].DependencyProjection.Datasets, ","); got != "orders" {
		t.Fatalf("orders datasets = %q", got)
	}
	if got := strings.Join(bundle.Branches[1].DependencyProjection.Datasets, ","); got != "tags" {
		t.Fatalf("tags datasets = %q", got)
	}
	for _, branch := range bundle.Branches {
		if branch.Fingerprint == "" || !strings.HasPrefix(branch.DependencyProjection.PlannerDigest, "sha256:") {
			t.Fatalf("branch %q is missing execution or dependency identity", branch.ID)
		}
	}
}
