package releaseaudit

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestRecordAuditEventFailsClosedWithoutTransaction(t *testing.T) {
	if _, err := New().RecordAuditEvent(context.Background(), nil, access.AuditIntent{}); err == nil {
		t.Fatal("audit adapter accepted nil transaction")
	}
}
