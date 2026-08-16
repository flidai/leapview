package sqlite

import (
	"database/sql"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestDecodeTokenCapabilitiesPreservesDynamicAndDenyAllForms(t *testing.T) {
	if got := decodeTokenCapabilities(sql.NullString{}); got != nil {
		t.Fatalf("NULL capabilities = %#v, want nil dynamic scope", got)
	}
	for _, value := range []string{"", "null", "{\"capability\":\"RESOURCE_READ\"}", "[\"NOT_A_CAPABILITY\"]"} {
		got := decodeTokenCapabilities(sql.NullString{String: value, Valid: true})
		if got == nil || len(got) != 0 {
			t.Fatalf("malformed capabilities %q = %#v, want non-nil deny-all", value, got)
		}
	}
	denyAll := decodeTokenCapabilities(sql.NullString{String: "[]", Valid: true})
	if denyAll == nil || len(denyAll) != 0 {
		t.Fatalf("JSON [] capabilities = %#v, want explicit non-nil deny-all", denyAll)
	}
	readOnly := decodeTokenCapabilities(sql.NullString{String: `["RESOURCE_READ"]`, Valid: true})
	if len(readOnly) != 1 || readOnly[0] != access.CapabilityResourceRead {
		t.Fatalf("JSON capabilities = %#v, want RESOURCE_READ", readOnly)
	}
}

func TestMarshalTokenCapabilitiesPreservesDynamicAndDenyAllForms(t *testing.T) {
	dynamic, err := marshalTokenCapabilities(nil)
	if err != nil || dynamic.Valid {
		t.Fatalf("nil capabilities = %#v, %v; want SQL NULL", dynamic, err)
	}
	denyAll, err := marshalTokenCapabilities([]access.Capability{})
	if err != nil || !denyAll.Valid || denyAll.String != "[]" {
		t.Fatalf("empty capabilities = %#v, %v; want SQL []", denyAll, err)
	}
	if _, err := marshalTokenCapabilities([]access.Capability{access.Capability("NOT_A_CAPABILITY")}); err == nil {
		t.Fatal("invalid capability accepted by persistence encoder")
	}
	if _, err := marshalTokenCapabilities([]access.Capability{access.CapabilityResourceRead, access.CapabilityResourceRead}); err == nil {
		t.Fatal("duplicate capability accepted by persistence encoder")
	}
}

func TestCloneTokenCapabilitiesPreservesNilAndExplicitDenyAll(t *testing.T) {
	if got := cloneTokenCapabilities(nil); got != nil {
		t.Fatalf("nil clone = %#v, want nil", got)
	}
	got := cloneTokenCapabilities([]access.Capability{})
	if got == nil || len(got) != 0 {
		t.Fatalf("empty clone = %#v, want explicit non-nil empty", got)
	}
}
