package access

import (
	"encoding/json"
	"os"
	"testing"
)

type permissionPairContractFixture struct {
	Name    string          `json:"name"`
	Profile *string         `json:"profile"`
	Pairs   json.RawMessage `json:"pairs"`
	Valid   bool            `json:"valid"`
}

func TestPermissionPairContractFixture(t *testing.T) {
	encoded, err := os.ReadFile("testdata/permission_pair_contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []permissionPairContractFixture
	if err := json.Unmarshal(encoded, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			valid := fixture.Profile != nil && *fixture.Profile == PermissionCatalogProfile
			if valid {
				_, err = DecodePermissionPairs(fixture.Pairs)
				valid = err == nil
			}
			if valid != fixture.Valid {
				t.Fatalf("Go permission contract validity = %t, want %t", valid, fixture.Valid)
			}
		})
	}
}
