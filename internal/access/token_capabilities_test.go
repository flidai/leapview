package access

import (
	"errors"
	"testing"
)

func TestValidateTokenCapabilitiesAllowsOmittedDynamicScope(t *testing.T) {
	effective := []Capability{CapabilityResourceRead, CapabilityResourceEdit}
	if err := ValidateTokenCapabilities(nil, effective); err != nil {
		t.Fatalf("nil token capability allowlist rejected: %v", err)
	}
	got := IntersectTokenCapabilities(nil, effective)
	if len(got) != len(effective) || got[0] != effective[0] || got[1] != effective[1] {
		t.Fatalf("dynamic capabilities = %#v, want %#v", got, effective)
	}
	got[0] = CapabilityResourceUse
	if effective[0] == got[0] {
		t.Fatal("dynamic intersection leaked effective-slice storage")
	}
}

func TestValidateTokenCapabilitiesAcceptsLeastPrivilegeSubset(t *testing.T) {
	effective := []Capability{CapabilityResourceRead, CapabilityResourceEdit, CapabilityResourcePublish}
	requested := []Capability{CapabilityResourceRead, CapabilityResourcePublish}
	if err := ValidateTokenCapabilities(requested, effective); err != nil {
		t.Fatalf("least-privilege subset rejected: %v", err)
	}
	got := IntersectTokenCapabilities(requested, []Capability{CapabilityResourcePublish, CapabilityResourceRead})
	want := []Capability{CapabilityResourcePublish, CapabilityResourceRead}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("intersection = %#v, want %#v", got, want)
	}
}

func TestValidateTokenCapabilitiesRejectsEscalationAndInvalidValues(t *testing.T) {
	effective := []Capability{CapabilityResourceRead}
	if err := ValidateTokenCapabilities([]Capability{CapabilityResourceEdit}, effective); !errors.Is(err, ErrCapabilityNotAllowed) {
		t.Fatalf("escalating capability error = %v, want ErrCapabilityNotAllowed", err)
	}
	if err := ValidateTokenCapabilities([]Capability{Capability("NOT_A_CAPABILITY")}, effective); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("invalid capability error = %v, want ErrInvalidCapability", err)
	}
	if err := ValidateTokenCapabilities([]Capability{}, effective); err != nil {
		t.Fatalf("explicit deny-all allowlist error = %v", err)
	}
	if got := IntersectTokenCapabilities([]Capability{}, effective); got == nil || len(got) != 0 {
		t.Fatalf("explicit deny-all intersection = %#v, want non-nil empty", got)
	}
}

func TestIntersectTokenCapabilitiesDeniesCapabilityNotInEffectiveSet(t *testing.T) {
	token := []Capability{CapabilityResourceRead, CapabilityResourceEdit}
	effective := []Capability{CapabilityResourceRead}
	got := IntersectTokenCapabilities(token, effective)
	if len(got) != 1 || got[0] != CapabilityResourceRead {
		t.Fatalf("intersection = %#v, want read only", got)
	}
}
