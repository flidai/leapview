package model

import "testing"

func TestISO4217SnapshotMetadata(t *testing.T) {
	if iso4217SnapshotVersion != "ISO 4217:2015 List One (2026-01-01 publication)" {
		t.Fatalf("ISO 4217 snapshot version = %q", iso4217SnapshotVersion)
	}
	if iso4217SnapshotEffective != "2026-01-01" {
		t.Fatalf("ISO 4217 snapshot effective date = %q", iso4217SnapshotEffective)
	}
	if iso4217SnapshotSource != "https://www.six-group.com/dam/download/financial-information/data-center/iso-currrency/lists/list-one.xml" {
		t.Fatalf("ISO 4217 snapshot source = %q", iso4217SnapshotSource)
	}
	if got, want := len(iso4217Units), 178; got != want {
		t.Fatalf("ISO 4217 snapshot code count = %d, want %d", got, want)
	}
}

func TestNormalizeMetricUnitISO4217Snapshot(t *testing.T) {
	tests := []struct {
		name          string
		authored      string
		wantName      string
		wantKnown     bool
		wantDimension bool
	}{
		{name: "active currency", authored: " USD ", wantName: "USD", wantKnown: true},
		{name: "new active currency", authored: "zwg", wantName: "ZWG", wantKnown: true},
		{name: "fund", authored: "xdr", wantName: "XDR", wantKnown: true},
		{name: "metal", authored: "xau", wantName: "XAU", wantKnown: true},
		{name: "test", authored: "xts", wantName: "XTS", wantKnown: true},
		{name: "withdrawn currency", authored: "ANG", wantName: "ANG", wantKnown: false},
		{name: "invalid code", authored: "US$", wantName: "US$", wantKnown: false},
		{name: "unknown metadata", authored: "widgets", wantName: "widgets", wantKnown: false},
		{name: "dimensionless", authored: " Dimensionless ", wantName: "dimensionless", wantKnown: true, wantDimension: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeMetricUnit(test.authored)
			if got.name != test.wantName || got.known != test.wantKnown || got.dimensionless != test.wantDimension {
				t.Fatalf("normalizeMetricUnit(%q) = %#v, want name=%q known=%t dimensionless=%t", test.authored, got, test.wantName, test.wantKnown, test.wantDimension)
			}
		})
	}
}

func TestISO4217SnapshotExcludesWithdrawnCodes(t *testing.T) {
	for _, withdrawn := range []string{"ANG", "BGN", "CUC", "SLL", "ZWL"} {
		if _, ok := iso4217Units[withdrawn]; ok {
			t.Errorf("withdrawn ISO 4217 code %q is present in current snapshot", withdrawn)
		}
	}
}
