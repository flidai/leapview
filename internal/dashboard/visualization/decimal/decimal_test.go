package decimal

import "testing"

func TestParseCanonicalDecimal(t *testing.T) {
	for _, test := range []struct {
		value string
		scale int
	}{
		{"10", 0},
		{"10.0", 1},
		{"9007199254740993.125", 3},
	} {
		if _, scale, err := Parse(test.value); err != nil || scale != test.scale {
			t.Fatalf("Parse(%q) scale=%d err=%v, want scale=%d", test.value, scale, err, test.scale)
		}
	}
	for _, value := range []string{"", "+1", "01", "1.", ".5", "-0.0", "1e3"} {
		if _, _, err := Parse(value); err == nil {
			t.Errorf("Parse(%q) unexpectedly accepted non-canonical decimal", value)
		}
	}
}
