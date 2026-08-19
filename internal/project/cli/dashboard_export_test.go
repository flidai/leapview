package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
)

func TestDashboardExportRequiresLocalProjectAndPreservesExistingOutputs(t *testing.T) {
	command := DashboardExportCommand(context.Background())
	projectPath := filepath.Join(t.TempDir(), "missing.yaml")
	command.SetArgs([]string{"sales", "--layout", "fragmented", "--project", projectPath})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("missing project error = %v", err)
	}

	root := t.TempDir()
	existing := filepath.Join(root, "dashboard.yaml")
	if err := os.WriteFile(existing, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeExpandedExport(existing, []byte("replacement")); err == nil {
		t.Fatal("expanded writer overwrote an existing file")
	}
	content, err := os.ReadFile(existing)
	if err != nil || string(content) != "original" {
		t.Fatalf("existing output changed: %q / %v", content, err)
	}
	outputFile := filepath.Join(root, "fragments")
	if err := os.WriteFile(outputFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	export := projectcompiler.DashboardSourceExport{Files: []projectcompiler.DashboardSourceFile{{Path: "dashboards/sales.yaml", Content: []byte("source")}}}
	if err := writeFragmentedExport(outputFile, export); err == nil {
		t.Fatal("fragmented writer accepted a file output")
	}
}

func TestDashboardExportFragmentedCLIWritesReviewableSources(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("connections/warehouse.yaml", "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:warehouse, name: warehouse}\nspec: {type: managed}\n")
	write("sources/orders.yaml", "apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:orders, name: orders}\nspec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}\n")
	write("models/orders.yaml", "apiVersion: leapview.dev/v1\nkind: Model\nmetadata: {id: model:orders, name: orders_model}\nspec: {definition: {type: direct, source: orders}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}\n")
	write("semantic-models/sales.yaml", "apiVersion: leapview.dev/v1\nkind: SemanticModel\nmetadata: {id: semantic:sales, name: sales}\nspec: {datasets: {orders: {model: orders_model}}, metrics: {order_count: {type: aggregate, dataset: orders, aggregation: count, input: {field: orders.id}, empty: zero}}}\n")
	write("dashboards/fragments/visuals.yaml", "visuals:\n  order_count:\n    type: kpi\n    query: {type: aggregate, dimensions: [], metrics: [order_count]}\n    presentation: {type: kpi}\n")
	write("dashboards/fragments/pages.yaml", "pages:\n  - id: overview\n    title: Overview\n    components: []\n")
	write("dashboards/sales.yaml", "apiVersion: leapview.dev/v1\nkind: Dashboard\nmetadata: {id: dashboard:sales, name: sales_dashboard}\nspec:\n  semanticModel: sales\n  filters: []\n  includes: {visuals: [fragments/visuals.yaml], pages: [fragments/pages.yaml]}\n  visuals: {}\n  pages: []\n")
	write("leapview.yaml", "apiVersion: leapview.dev/v1\nkind: Project\nmetadata: {id: project:test, name: test}\nspec:\n  connections: {include: [connections/*.yaml]}\n  sources: {include: [sources/*.yaml]}\n  models: {include: [models/*.yaml]}\n  semanticModels: {include: [semantic-models/*.yaml]}\n  pipelines: {include: []}\n  dashboards: {include: [dashboards/*.yaml]}\n  access: {include: []}\n  publications: {include: []}\n")
	output := filepath.Join(root, "export")
	var stdout bytes.Buffer
	command := DashboardExportCommand(context.Background())
	command.SetOut(&stdout)
	command.SetArgs([]string{"dashboard:sales", "--project", filepath.Join(root, "leapview.yaml"), "--layout", "fragmented", "--out", output})
	if err := command.Execute(); err != nil {
		t.Fatalf("fragmented dashboard export: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("fragmented export wrote stdout: %s", stdout.String())
	}
	for _, path := range []string{"dashboards/sales.yaml", "dashboards/fragments/visuals.yaml", "dashboards/fragments/pages.yaml"} {
		if _, err := os.Stat(filepath.Join(output, filepath.FromSlash(path))); err != nil {
			t.Fatalf("fragmented export omitted %s: %v", path, err)
		}
	}
}
