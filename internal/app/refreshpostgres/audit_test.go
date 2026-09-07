package refreshpostgres

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNativeAuditEventIDProjectsDigestToCanonicalUUID(t *testing.T) {
	value := "sha256:" + strings.Repeat("ab", 32)
	got := nativeAuditEventID(value)
	if got != "abababab-abab-abab-abab-abababababab" {
		t.Fatalf("projected event id=%q", got)
	}
	parsed, err := uuid.Parse(got)
	if err != nil || parsed.String() != got {
		t.Fatalf("projected event id is not canonical UUID: %q (%v)", got, err)
	}
	if nativeAuditEventID("01900000-0000-7000-8000-000000000001") != "01900000-0000-7000-8000-000000000001" {
		t.Fatal("canonical UUID event id was unexpectedly changed")
	}
}
