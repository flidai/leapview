package releasecontract

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

func compatibilityFixture(schema int) SourceCompatibility {
	source := SourceCompatibility{
		PermissionProfile: Current().PermissionProfile,
		Schema:            schema,
		Migrations:        map[string]string{},
		Engines:           map[string]string{"github.com/duckdb/duckdb-go/v2": "v2.1.0", "github.com/riverqueue/river": "v0.47.0"},
		RolePolicy:        strings.Repeat("a", 64),
	}
	for n := 1; n <= schema; n++ {
		source.Migrations[fmt.Sprintf("%03d_migration.sql", n)] = strings.Repeat("b", 64)
	}
	return source
}

func TestClassifySourcesSharedPreflight(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*SourceCompatibility, *SourceCompatibility)
		mode      string
		pending   []string
		wantError bool
	}{
		{name: "unchanged", mode: "image-only"},
		{name: "forward migration", mutate: func(before, after *SourceCompatibility) { *after = compatibilityFixture(10) }, mode: "database-upgrade-required", pending: []string{"009_migration.sql", "010_migration.sql"}},
		{name: "role policy", mutate: func(before, after *SourceCompatibility) { after.RolePolicy = strings.Repeat("c", 64) }, mode: "database-upgrade-required"},
		{name: "legacy permission transition", mutate: func(before, after *SourceCompatibility) { before.PermissionProfile = LegacyPermissions }, mode: "database-upgrade-required"},
		{name: "engine version", mutate: func(before, after *SourceCompatibility) { after.Engines["github.com/riverqueue/river"] = "v0.48.0" }, mode: "review-required"},
		{name: "new engine module", mutate: func(before, after *SourceCompatibility) {
			after.Engines["github.com/duckdb/duckdb-go-bindings"] = "v0.1.0"
		}, mode: "review-required"},
		{name: "schema downgrade", mutate: func(before, after *SourceCompatibility) { *after = compatibilityFixture(7) }, wantError: true},
		{name: "permission downgrade", mutate: func(before, after *SourceCompatibility) { after.PermissionProfile = LegacyPermissions }, wantError: true},
		{name: "rewritten history", mutate: func(before, after *SourceCompatibility) {
			after.Migrations["001_migration.sql"] = strings.Repeat("c", 64)
		}, wantError: true},
		{name: "missing history", mutate: func(before, after *SourceCompatibility) { delete(after.Migrations, "001_migration.sql") }, wantError: true},
		{name: "unknown engine", mutate: func(before, after *SourceCompatibility) {
			before.Engines["unreviewed/engine"] = "v1.0.0"
			after.Engines = maps.Clone(before.Engines)
		}, wantError: true},
		{name: "empty module name", mutate: func(before, after *SourceCompatibility) { after.Engines["github.com/duckdb/"] = "v1.0.0" }, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, after := compatibilityFixture(8), compatibilityFixture(8)
			if tt.mutate != nil {
				tt.mutate(&before, &after)
			}
			mode, pending, err := ClassifySources(before, after)
			if (err != nil) != tt.wantError {
				t.Fatalf("mode=%q pending=%v error=%v", mode, pending, err)
			}
			if tt.wantError {
				if mode != "" || len(pending) != 0 {
					t.Fatal("invalid evidence produced a usable decision")
				}
				return
			}
			if mode != tt.mode || !slices.Equal(pending, tt.pending) {
				t.Fatalf("got %q %v, want %q %v", mode, pending, tt.mode, tt.pending)
			}
		})
	}
}
