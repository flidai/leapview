//go:build duckdb_arrow

package duckdb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/gates"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/release"
)

func TestADR0023AuthoredSourceAndModelChecksQualifyTheirOwnData(t *testing.T) {
	for _, test := range []struct {
		name          string
		csv           string
		sql           string
		sourceOutcome release.GateOutcome
		modelOutcome  release.GateOutcome
		modelRows     int64
	}{
		{
			name:          "transformation repairs invalid source rows",
			csv:           "order_id,state,note\na,valid,first\na,invalid,second\nb,valid,third\n",
			sql:           "SELECT order_id FROM source.raw_orders WHERE state = 'valid'",
			sourceOutcome: release.GateBlocking, modelOutcome: release.GateSuccess, modelRows: 2,
		},
		{
			name:          "transformation breaks valid source grain",
			csv:           "order_id,state,note\na,valid,first\nb,valid,second\n",
			sql:           "SELECT order_id FROM source.raw_orders UNION ALL SELECT order_id FROM source.raw_orders",
			sourceOutcome: release.GateSuccess, modelOutcome: release.GateBlocking, modelRows: 4,
		},
		{
			name:          "valid source and model qualify",
			csv:           "order_id,state,note\na,valid,first\nb,valid,second\n",
			sql:           "SELECT order_id FROM source.raw_orders",
			sourceOutcome: release.GateSuccess, modelOutcome: release.GateSuccess, modelRows: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{
				"connections/local.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:local, name: local}
spec: {type: managed}
`,
				"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata:
  id: source:raw_orders
  name: raw_orders
spec:
  connection: local
  location:
    type: path
    path: orders.csv
    format: csv
    options:
      header: true
  schema:
    mode: strict
  fields:
  - name: order_id
    datatype: String
  - name: state
    datatype: String
  - name: note
    datatype: String
  checks:
  - id: source_ids_unique
    type: unique
    fields:
    - order_id
    severity: error
  - id: source_state_valid
    type: accepted_values
    field: state
    values:
    - valid
    severity: error
`,
				"models/orders.yaml": fmt.Sprintf(`apiVersion: leapview.dev/v1
kind: Model
metadata:
  id: model:orders
  name: orders
spec:
  definition:
    type: sql
    sql: "%s"
  schema:
    mode: strict
  fields:
  - name: order_id
    datatype: String
  entities:
  - name: order
    type: primary
    fields:
    - order_id
  grain:
    entity: order
  checks:
  - id: output_ids_unique
    type: unique
    fields:
    - order_id
    severity: error
`, test.sql),
				"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata:
  id: semantic-model:sales
  name: sales
spec:
  datasets:
  - name: orders
    model: orders
`,
				"orders.csv": test.csv,
			}
			for path, content := range files {
				fullPath := filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			compiled, err := projectcompiler.LoadSourceRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			model := compiled.Manifest.SemanticModels["semantic-model:sales"]
			if model == nil || len(model.Sources) != 1 || len(model.Tables) != 1 {
				t.Fatalf("compiled semantic model = %#v", model)
			}
			connection := model.Connections["local"]
			connection.Root = root
			model.Connections["local"] = connection

			ctx := analyticsmaterialize.WithObservationBudget(context.Background(), analyticsmaterialize.ObservationBudget{MaxQueries: 64, MaxRows: 1000, MaxMillis: 5000})
			ctx = analyticsmaterialize.WithSourceCheckEvaluator(ctx, func(ctx context.Context, id, relation string, checks []semanticmodel.ModelCheck, refs map[string]string, budget analyticsmaterialize.ObservationBudget, query func(context.Context, semanticquery.Plan) (semanticquery.Rows, error)) ([]analyticsmaterialize.SourceCheckEvidence, error) {
				checked, err := gates.EvaluateSourceChecks(ctx, id, relation, checks, refs, gates.Bounds{MaxQueries: budget.MaxQueries, MaxRows: budget.MaxRows, MaxMillis: budget.MaxMillis}, query)
				result := make([]analyticsmaterialize.SourceCheckEvidence, len(checked))
				for index, check := range checked {
					result[index] = analyticsmaterialize.SourceCheckEvidence{Identity: check.Identity, Kind: check.Kind, ResourceID: check.ResourceID, Outcome: string(check.Outcome), Severity: check.Severity, ObservedRows: check.ObservedRows, Queries: check.Queries, ObservationDigest: check.ObservationDigest}
				}
				return result, err
			})
			ctx, environment, runtime := openSchemaTestRuntime(t, ctx, root, model)
			var captured []analyticsmaterialize.SourceObservation
			if err := runtime.RefreshProjectTablesWithObservationWriter(ctx, []string{"orders"}, func(_ context.Context, observations []analyticsmaterialize.SourceObservation) error {
				captured = observations
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(captured) != 1 || len(captured[0].CheckEvidence) != 2 {
				t.Fatalf("captured source evidence = %#v", captured)
			}
			for _, check := range captured[0].CheckEvidence {
				if check.Outcome != string(test.sourceOutcome) || check.Queries == 0 {
					t.Fatalf("source check = %#v, want %s", check, test.sourceOutcome)
				}
			}
			rows, err := environment.Query(ctx, semanticquery.Plan{SQL: `SELECT COUNT(*) AS value FROM model."orders"`, Columns: []string{"value"}})
			if err != nil || len(rows) != 1 || rows[0]["value"] != test.modelRows {
				t.Fatalf("transformed rows = %#v, err=%v, want %d", rows, err, test.modelRows)
			}
			preflight := make([]release.GateCheckEvidence, len(captured[0].CheckEvidence))
			for index, check := range captured[0].CheckEvidence {
				preflight[index] = release.GateCheckEvidence{Identity: check.Identity, Kind: check.Kind, ResourceID: check.ResourceID, Origin: "source", Outcome: release.GateOutcome(check.Outcome), Severity: check.Severity, ObservedRows: check.ObservedRows, Queries: check.Queries, ObservationDigest: check.ObservationDigest}
			}
			input := gates.Input{
				CandidateID: "candidate-adr-0023", SourceDigest: "sha256:" + strings.Repeat("a", 64), BindingGeneration: "sha256:" + strings.Repeat("b", 64),
				RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Now: time.Now().UTC(), Query: environment.Query,
				Sources: []gates.SourceInput{{ID: captured[0].ID, Source: runtime.materializationModel.Sources[captured[0].ID], Observed: captured[0].Schema,
					ObservationQueries: captured[0].ObservationQueries, ObservationRows: captured[0].ObservationRows, ObservationMillis: captured[0].ObservationMillis, PreflightChecks: preflight}},
				Models:           []gates.ModelInput{{ID: "orders", Model: runtime.materializationModel.Tables["orders"]}},
				PreflightQueries: captured[0].ObservationQueries, PreflightRows: captured[0].ObservationRows, PreflightMillis: captured[0].ObservationMillis,
			}
			if test.sourceOutcome == release.GateBlocking {
				// A repaired Model can pass its own rules, but cannot erase a
				// blocking contract on the Source that supplied it.
				modelOnly := input
				modelOnly.Sources = nil
				modelOnly.PreflightQueries, modelOnly.PreflightRows, modelOnly.PreflightMillis = 0, 0, 0
				if evidence, err := gates.Evaluate(ctx, modelOnly); err != nil || evidence.Outcome != release.GateSuccess {
					t.Fatalf("repaired Model qualification = %#v, %v", evidence, err)
				}
			}
			evidence, err := gates.Evaluate(ctx, input)
			if test.sourceOutcome == release.GateSuccess && test.modelOutcome == release.GateSuccess {
				if err != nil || evidence.Outcome != release.GateSuccess {
					t.Fatalf("valid candidate qualification = %#v, %v", evidence, err)
				}
				return
			}
			var gateError *gates.EvaluationError
			if !errors.As(err, &gateError) || gateError.Outcome != release.GateBlocking || evidence.Outcome != release.GateBlocking {
				t.Fatalf("candidate qualification = %#v, %v", evidence, err)
			}
			if test.sourceOutcome == release.GateSuccess {
				seenModelFailure := false
				for _, check := range evidence.Checks {
					seenModelFailure = seenModelFailure || check.ResourceID == "orders" && check.Origin != "source" && check.Outcome == test.modelOutcome
				}
				if !seenModelFailure {
					t.Fatalf("missing transformed Model failure: %#v", evidence.Checks)
				}
			}
		})
	}
}
