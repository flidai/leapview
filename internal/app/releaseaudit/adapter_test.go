package releaseaudit

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
)

func TestNewWithRepositoryPreservesAccessAuditIdentity(t *testing.T) {
	audit := accesspostgres.New()
	adapter := NewWithRepository(audit)
	if !adapter.Matches(audit) {
		t.Fatal("adapter did not retain the supplied Access audit repository")
	}
	if adapter.Matches(accesspostgres.New()) {
		t.Fatal("adapter accepted a distinct Access audit repository")
	}
}

func TestRecordAuditEventFailsClosedWithoutTransaction(t *testing.T) {
	if _, err := NewWithRepository(accesspostgres.New()).RecordAuditEvent(context.Background(), nil, access.AuditIntent{}); err == nil {
		t.Fatal("audit adapter accepted nil transaction")
	}
}
