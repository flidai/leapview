package hostinstall

import (
	"testing"

	"github.com/flidai/leapview/internal/app/providerrestore"
)

func TestRecoveryFenceRefusesAuthorityOnOriginalWriter(t *testing.T) {
	config := providerrestore.PrimaryFenceSSHConfig{Primaries: []providerrestore.PrimaryEnrollment{
		{SystemIdentifier: "100"}, {SystemIdentifier: "200"},
	}}
	for _, identifier := range []string{"", "100", "200"} {
		if err := validateFenceAuthority(config, identifier); err == nil {
			t.Fatalf("recovery ledger on unavailable/original cluster %q accepted before physical fencing", identifier)
		}
	}
	if err := validateFenceAuthority(config, "300"); err != nil {
		t.Fatal(err)
	}
}

func TestHostRecoveryFenceIsSeparateFromAdmission(t *testing.T) {
	command, _, err := Command(t.Context(), CommandOptions{}).Find([]string{"fence-recovery"})
	if err != nil || command.Name() != "fence-recovery" {
		t.Fatal("operator cannot establish or inspect managed original-writer fence")
	}
	for _, flag := range []string{"enrollment-file", "control-url-file", "recovery-set-id", "check"} {
		if command.Flags().Lookup(flag) == nil {
			t.Fatalf("missing fence scope option %s", flag)
		}
	}
}
