package duckdb

import (
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestValidateAdmittedExtensionRequiresExactIdentity(t *testing.T) {
	valid := AdmittedExtension{Name: "httpfs", Identity: "leapview/httpfs", Version: "1.0.0", Platform: "linux-amd64", Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000", Path: "/opt/leapview/extensions/httpfs.duckdb_extension"}
	if err := validateAdmittedExtension("httpfs", valid); err != nil {
		t.Fatalf("valid admission rejected: %v", err)
	}
	for name, candidate := range map[string]AdmittedExtension{
		"wrong name":          {Name: "postgres", Identity: valid.Identity, Version: valid.Version, Platform: valid.Platform, Digest: valid.Digest, Path: valid.Path},
		"missing digest":      {Name: "httpfs", Identity: valid.Identity, Version: valid.Version, Platform: valid.Platform, Path: valid.Path},
		"relative path":       {Name: "httpfs", Identity: valid.Identity, Version: valid.Version, Platform: valid.Platform, Digest: valid.Digest, Path: "httpfs.duckdb_extension"},
		"wrong artifact name": {Name: "httpfs", Identity: valid.Identity, Version: valid.Version, Platform: valid.Platform, Digest: valid.Digest, Path: "/opt/leapview/extensions/postgres.duckdb_extension"},
	} {
		candidate := candidate
		t.Run(name, func(t *testing.T) {
			if err := validateAdmittedExtension("httpfs", candidate); err == nil {
				t.Fatal("invalid extension admission accepted")
			}
		})
	}
}

func TestLoadExtensionStatementUsesExactQuotedArtifactPath(t *testing.T) {
	got := loadExtensionStatement("/opt/leapview/extensions/httpfs'v1.duckdb_extension")
	if got != "LOAD '/opt/leapview/extensions/httpfs''v1.duckdb_extension'" {
		t.Fatalf("load statement = %q", got)
	}
}

func TestResolveSourcePlanUsesCompiledEffectiveOptionsVerbatim(t *testing.T) {
	effective := map[string]any{"header": true, "delimiter": ";"}
	model := &semanticmodel.Model{
		Connections: map[string]semanticmodel.Connection{"files": {Kind: "managed", Root: "/tmp/revision"}},
	}
	source := semanticmodel.Source{LocationType: semanticmodel.KindPath, Connection: "files", Path: "orders.csv", Format: "csv", EffectiveOptions: effective}
	plan, err := ResolveSourcePlan(model, source)
	if err != nil {
		t.Fatalf("ResolveSourcePlan() error = %v", err)
	}
	if !reflect.DeepEqual(plan.options, effective) {
		t.Fatalf("runtime plan options = %#v, want compiled effective options %#v", plan.options, effective)
	}
	empty := semanticmodel.Source{LocationType: semanticmodel.KindPath, Connection: "files", Path: "orders.vortex", Format: "vortex", EffectiveOptions: map[string]any{}}
	emptyPlan, err := ResolveSourcePlan(model, empty)
	if err != nil {
		t.Fatalf("ResolveSourcePlan(empty effective options) error = %v", err)
	}
	if emptyPlan.options == nil || len(emptyPlan.options) != 0 {
		t.Fatalf("runtime plan re-resolved compiled empty options: %#v", emptyPlan.options)
	}
}
