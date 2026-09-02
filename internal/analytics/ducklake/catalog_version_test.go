package ducklake

import "testing"

func TestCanonicalCatalogVersionNormalizesZeroMinorPresentation(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "1", want: "1"},
		{value: "v1", want: "1"},
		{value: "1.0", want: "1"},
		{value: "ducklake:v1.0", want: "1"},
		{value: "ducklake-catalog:v1", want: "1"},
		{value: "1.1", want: "1.1"},
		{value: "v1.1-dev1", want: "1.1-dev1"},
	} {
		got, err := CanonicalCatalogVersion(test.value)
		if err != nil || got != test.want {
			t.Fatalf("CanonicalCatalogVersion(%q) = %q, %v; want %q", test.value, got, err, test.want)
		}
	}
	if _, err := CanonicalCatalogVersion("format:v1"); err == nil {
		t.Fatal("foreign catalog version prefix accepted")
	}
}

func TestCatalogVersionNumberAcceptsZeroMinorAndRejectsDistinctMinor(t *testing.T) {
	for _, value := range []string{"1", "v1", "1.0", "ducklake-catalog:v1.0"} {
		got, err := CatalogVersionNumber(value)
		if err != nil || got != 1 {
			t.Fatalf("CatalogVersionNumber(%q) = %d, %v; want 1", value, got, err)
		}
	}
	for _, value := range []string{"0", "1.1", "1.1-dev1", "01.0"} {
		if _, err := CatalogVersionNumber(value); err == nil {
			t.Fatalf("CatalogVersionNumber(%q) accepted a non-major format", value)
		}
	}
}
