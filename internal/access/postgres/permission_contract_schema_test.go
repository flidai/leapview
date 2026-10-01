package postgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type freshSchemaPermissionFixture struct {
	Name    string          `json:"name"`
	Profile *string         `json:"profile"`
	Pairs   json.RawMessage `json:"pairs"`
	Valid   bool            `json:"valid"`
}

func TestFreshAccessSchemaUsesPermissionContractFixture(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate permission contract fixture")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../testdata/permission_pair_contract.json"))
	if err != nil {
		t.Fatalf("read permission contract fixture: %v", err)
	}
	var fixtures []freshSchemaPermissionFixture
	if err := json.Unmarshal(encoded, &fixtures); err != nil {
		t.Fatalf("decode permission contract fixture: %v", err)
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			var profile any
			if fixture.Profile != nil {
				profile = *fixture.Profile
			}
			var got bool
			if err := db.admin.QueryRow(t.Context(), `
				SELECT access.valid_permission_pairs($1::text, $2::jsonb)`, profile, fixture.Pairs).Scan(&got); err != nil {
				t.Fatalf("validate permission pairs: %v", err)
			}
			if got != fixture.Valid {
				t.Fatalf("fresh schema permission contract validity = %t, want %t", got, fixture.Valid)
			}
		})
	}
}
