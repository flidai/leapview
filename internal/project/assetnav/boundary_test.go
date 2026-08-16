package assetnav

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	project "github.com/flidai/leapview/internal/project"
)

func TestAssetNavDoesNotImportHeadlessAPIContract(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob assetnav: %v", err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatalf("stat %s: %v", file, err)
		}
		if info.IsDir() {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse imports in %s: %v", file, err)
		}
		for _, imported := range parsed.Imports {
			if strings.Trim(imported.Path.Value, "\"") == "github.com/flidai/leapview/internal/app/api/gen" {
				t.Fatalf("%s imports forbidden package internal/app/api/gen", file)
			}
		}
	}
}

func TestCanonicalAssetSectionHrefUsesResourceAreas(t *testing.T) {
	tests := []struct {
		name    string
		asset   project.DevelopAssetView
		section string
		want    string
	}{
		{name: "source", asset: project.DevelopAssetView{ID: "source:orders", Type: string(project.AssetTypeSource)}, section: "details", want: "/data/source:orders/details"},
		{name: "model", asset: project.DevelopAssetView{ID: "model_table:orders", Type: string(project.AssetTypeModelTable)}, section: "details", want: "/models/model_table:orders/details"},
		{name: "semantic model", asset: project.DevelopAssetView{ID: "semantic_model:sales", Type: string(project.AssetTypeSemanticModel)}, section: "lineage", want: "/semantic-models/semantic_model:sales/lineage"},
		{name: "pipeline", asset: project.DevelopAssetView{ID: "refresh_pipeline:daily", Type: string(project.AssetTypeRefreshPipeline)}, section: "refreshes", want: "/pipelines/refresh_pipeline:daily/refreshes"},
		{name: "connection", asset: project.DevelopAssetView{ID: "connection:warehouse", Type: string(project.AssetTypeConnection)}, section: "details", want: "/connections/connection:warehouse/details"},
		{name: "dashboard", asset: project.DevelopAssetView{ID: "dashboard:exec", Type: string(project.AssetTypeDashboard), Href: "/dashboards/exec"}, section: "details", want: "/dashboards/exec"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanonicalAssetSectionHref(tt.asset, tt.section); got != tt.want {
				t.Fatalf("href = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanonicalSourceHrefStaysInDataArea(t *testing.T) {
	asset := project.DevelopAssetView{ID: "source:orders", Type: string(project.AssetTypeSource)}
	if got := CanonicalAssetSectionHref(asset, "details"); got != "/data/source:orders/details" {
		t.Fatalf("href = %q, want /data/source:orders/details", got)
	}
}

func TestConnectionAssetHrefEscapesIDs(t *testing.T) {
	if got := ConnectionAssetSectionHref("connection:warehouse/primary", "details"); got != "/connections/connection:warehouse%2Fprimary/details" {
		t.Fatalf("href = %q, want escaped connection ID", got)
	}
}

func TestCanonicalPipelineHrefEscapesIDs(t *testing.T) {
	asset := project.DevelopAssetView{ID: "refresh_pipeline:daily/primary", Type: string(project.AssetTypeRefreshPipeline)}
	if got := CanonicalAssetSectionHref(asset, "refreshes"); got != "/pipelines/refresh_pipeline:daily%2Fprimary/refreshes" {
		t.Fatalf("href = %q, want escaped pipeline ID", got)
	}
}
