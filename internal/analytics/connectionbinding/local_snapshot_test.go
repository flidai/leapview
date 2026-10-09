package connectionbinding

import (
	"errors"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestCredentialIdentityValidatesExclusiveCanonicalOrigins(t *testing.T) {
	for name, identity := range map[string]CredentialIdentity{
		"provider":        {ProviderVersion: "version-7"},
		"public sentinel": {ProviderVersion: NoAuthProviderVersion},
		"local UUID":      {CredentialVersionID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	} {
		t.Run(name, func(t *testing.T) { require.NoError(t, identity.Validate()) })
	}
	for name, identity := range map[string]CredentialIdentity{
		"empty":                 {},
		"mixed":                 {ProviderVersion: "version-7", CredentialVersionID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		"blank provider":        {ProviderVersion: "  "},
		"noncanonical provider": {ProviderVersion: " version-7 "},
		"malformed local UUID":  {CredentialVersionID: "aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"},
		"uppercase local UUID":  {CredentialVersionID: "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"},
		"zero local UUID":       {CredentialVersionID: "00000000-0000-0000-0000-000000000000"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := identity.Validate(); !errors.Is(err, ErrInvalidBinding) {
				t.Fatalf("Validate() error = %v, want ErrInvalidBinding", err)
			}
		})
	}
}

func TestNewLocalCredentialSnapshotCarriesAndDestroysIdentity(t *testing.T) {
	now := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	versionID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	snapshot, err := NewLocalCredentialSnapshot(
		map[string]string{"password": "source-secret"}, versionID, now, now.Add(time.Hour),
	)
	require.NoError(t, err)
	if got := snapshot.Identity(); got != (CredentialIdentity{CredentialVersionID: versionID}) {
		t.Fatalf("snapshot identity = %#v", got)
	}
	if snapshot.ProviderVersion() != "" {
		t.Fatalf("local snapshot provider version = %q", snapshot.ProviderVersion())
	}
	if _, err := NewLocalCredentialSnapshot(map[string]string{"password": "secret"}, strings.ToUpper(versionID), now, now.Add(time.Hour)); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("uppercase local credential version error = %v", err)
	}
	snapshot.Destroy()
	if got := snapshot.Identity(); got != (CredentialIdentity{}) {
		t.Fatalf("destroyed snapshot retained identity: %#v", got)
	}
}
