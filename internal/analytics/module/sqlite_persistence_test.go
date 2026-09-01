package module

import "testing"

func TestNewSQLiteConnectionBindingsRequiresDatabase(t *testing.T) {
	bindings, err := NewSQLiteConnectionBindings(nil, nil)
	if err == nil || bindings != nil {
		t.Fatalf("NewSQLiteConnectionBindings(nil) = (%T, %v), want nil authority and error", bindings, err)
	}
}
