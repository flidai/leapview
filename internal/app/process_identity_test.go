package app

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewProcessNodeIDIsUniqueUUIDv7(t *testing.T) {
	first, err := newProcessNodeID()
	if err != nil {
		t.Fatalf("first process node ID: %v", err)
	}
	second, err := newProcessNodeID()
	if err != nil {
		t.Fatalf("second process node ID: %v", err)
	}
	if first == second {
		t.Fatalf("process node IDs collided: %q", first)
	}
	for _, value := range []string{first, second} {
		id, parseErr := uuid.Parse(value)
		if parseErr != nil || id.String() != value || id.Version() != 7 || id == uuid.Nil {
			t.Fatalf("process node ID %q is not canonical UUIDv7: %v", value, parseErr)
		}
	}
}
