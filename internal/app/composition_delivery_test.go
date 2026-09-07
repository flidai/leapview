package app

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
)

func TestBindCandidateManagedDataRootsUsesCanonicalNameIndexOnDetachedModels(t *testing.T) {
	models := map[string]*semanticmodel.Model{
		"semantic:sales": {Connections: map[string]semanticmodel.Connection{
			"olist":     {Kind: "managed", Scope: "authored-scope"},
			"warehouse": {Kind: "s3", Scope: "s3://warehouse/"},
		}},
	}
	if err := analyticsmodule.BindCandidateManagedDataRoots(models, map[string]string{"olist": "connection:olist", "warehouse": "connection:warehouse"}, map[string]string{"connection:olist": "/managed/olist/revision"}); err != nil {
		t.Fatal(err)
	}
	managed := models["semantic:sales"].Connections["olist"]
	if managed.Root != "/managed/olist/revision" || managed.Scope != "" {
		t.Fatalf("managed candidate binding = %#v, want root with empty scope", managed)
	}
	if got := models["semantic:sales"].Connections["warehouse"].Scope; got != "s3://warehouse/" {
		t.Fatalf("authored connection scope = %q, want unchanged", got)
	}
}
