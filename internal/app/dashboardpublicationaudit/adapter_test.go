package dashboardpublicationaudit

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestRecordAuditIntentFailsClosedWithoutTransaction(t *testing.T) {
	if err := New().RecordAuditIntent(context.Background(), nil, access.AuditIntent{}); err == nil {
		t.Fatal("audit adapter accepted nil transaction")
	}
	var adapter *Adapter
	if err := adapter.RecordAuditIntent(context.Background(), nil, access.AuditIntent{}); err == nil {
		t.Fatal("nil audit adapter accepted nil transaction")
	}
}
