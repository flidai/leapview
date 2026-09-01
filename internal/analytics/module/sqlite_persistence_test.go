package module

import (
	"database/sql"
	"strings"
	"testing"
)

func TestNewSQLiteConnectionBindingsRequiresDatabase(t *testing.T) {
	bindings, err := NewSQLiteConnectionBindings(nil, nil)
	if err == nil || bindings != nil {
		t.Fatalf("NewSQLiteConnectionBindings(nil) = (%T, %v), want nil authority and error", bindings, err)
	}
}

func TestNewSQLiteConnectionBindingsRequiresAuditIntentRecorder(t *testing.T) {
	bindings, err := NewSQLiteConnectionBindings(&sql.DB{}, nil)
	if err == nil || bindings != nil || !strings.Contains(err.Error(), "audit intent recorder") {
		t.Fatalf("missing audit recorder = (%T, %v)", bindings, err)
	}
}
