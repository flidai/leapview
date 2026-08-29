package app

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	"github.com/flidai/leapview/internal/platform/http/idempotency"
)

func TestAPIProtocolPersistenceRequiresCompleteExplicitAuthorities(t *testing.T) {
	_, _, err := (apiProtocolPersistence{Idempotency: idempotency.NewMemoryStore()}).authorities()
	if err == nil || !strings.Contains(err.Error(), "both idempotency and cursor-signing") {
		t.Fatalf("partial explicit protocol authorities error = %v", err)
	}
	_, _, err = (apiProtocolPersistence{RequireExplicit: true}).authorities()
	if err == nil || !strings.Contains(err.Error(), "requires explicit durable authorities") {
		t.Fatalf("missing production protocol authorities error = %v", err)
	}
	store, cursor, err := (apiProtocolPersistence{
		Idempotency: idempotency.NewMemoryStore(), CursorSigning: cursorsigning.NewEphemeralInitializer(), RequireExplicit: true,
	}).authorities()
	if err != nil || store == nil || cursor == nil {
		t.Fatalf("complete explicit protocol authorities = (%T, %T, %v)", store, cursor, err)
	}
}
