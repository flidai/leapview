package deploymentpostgres

import (
	"context"
	"testing"
)

func TestNativeReaderFailsClosedWithoutAuthority(t *testing.T) {
	_, err := NewNativeReader(nil).Plan(context.Background(), "plan")
	if err == nil || err.Error() != "deployment PostgreSQL native reader is not configured" {
		t.Fatalf("error = %v", err)
	}
}
