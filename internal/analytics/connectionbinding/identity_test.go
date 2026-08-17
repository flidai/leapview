package connectionbinding

import "testing"

func TestOperationalIDsRejectWhitespace(t *testing.T) {
	if _, err := ParseBindingID(" binding_1"); err == nil {
		t.Fatal("ParseBindingID accepted leading whitespace")
	}
	if _, err := ParseTargetID("target_1 "); err == nil {
		t.Fatal("ParseTargetID accepted trailing whitespace")
	}
}
