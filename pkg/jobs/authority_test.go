package jobs

import (
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/pkg/permissions"
)

func TestAuthorityEnvelopeValidatesCallerEvidenceAndExactPermission(t *testing.T) {
	pair, err := permissions.NewExactPair("leapview.permissions/v1", permissions.Action("dashboard.read"), "project_a", permissions.Kind("dashboard"), "dashboard_a")
	if err != nil {
		t.Fatal(err)
	}
	authority := AuthorityEnvelope{
		Profile:              AuthorityEnvelopeProfile,
		Mode:                 CallerAuthorityMode,
		ActorPrincipalID:     "principal_a",
		ExecutionPrincipalID: "principal_a",
		Credential:           &CredentialEvidence{Class: CredentialClassAPIToken, ID: "token_a", Fingerprint: "fingerprint_a", ExpiresAt: time.Now().Add(time.Hour)},
		Target:               AuthorityTarget{InstanceID: "instance_a", ProjectID: "project_a", Environment: "production", ResourceKind: "dashboard", ResourceID: "dashboard_a"},
		Permissions:          []permissions.Pair{pair},
	}
	encoded, err := MarshalAuthority(authority)
	if err != nil {
		t.Fatalf("MarshalAuthority() error = %v", err)
	}
	decoded, err := UnmarshalAuthority(encoded)
	if err != nil {
		t.Fatalf("UnmarshalAuthority() error = %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded authority validation error = %v", err)
	}
	if decoded.Credential == nil || decoded.Credential.ID != "token_a" || len(decoded.Permissions) != 1 {
		t.Fatalf("decoded authority = %#v", decoded)
	}
	decoded.Credential.Class = "transport"
	if err := decoded.Validate(); !errors.Is(err, ErrAuthorityInvalid) {
		t.Fatalf("unsupported credential class error = %v", err)
	}
}

func TestAuthorityEnvelopeRejectsMissingCallerEvidenceAndAllowsDelegatedShape(t *testing.T) {
	pair, err := permissions.NewExactPair("leapview.permissions/v1", permissions.Action("pipeline.run"), "project_a", permissions.Kind("pipeline"), "pipeline_a")
	if err != nil {
		t.Fatal(err)
	}
	caller := AuthorityEnvelope{
		Profile: AuthorityEnvelopeProfile, Mode: CallerAuthorityMode,
		ActorPrincipalID: "principal_a", ExecutionPrincipalID: "principal_a",
		Target:      AuthorityTarget{ProjectID: "project_a", ResourceKind: "pipeline", ResourceID: "pipeline_a"},
		Permissions: []permissions.Pair{pair},
	}
	if err := caller.Validate(); !errors.Is(err, ErrAuthorityInvalid) {
		t.Fatalf("missing caller evidence error = %v", err)
	}
	delegated := caller
	delegated.Mode = DelegatedWorkloadMode
	delegated.ExecutionPrincipalID = "workload_a"
	delegated.ExecutionGrant = &ExecutionGrantEvidence{ID: "grant_a", Fingerprint: "grant_fp", ExpiresAt: time.Now().Add(time.Hour)}
	if err := delegated.Validate(); err != nil {
		t.Fatalf("delegated authority shape error = %v", err)
	}
}

func TestMarshalAuthorityLegacySentinelIsNotValidAuthority(t *testing.T) {
	encoded, err := MarshalAuthority(AuthorityEnvelope{})
	if err != nil || string(encoded) != "{}" {
		t.Fatalf("legacy authority encoding = %q, %v", encoded, err)
	}
	decoded, err := UnmarshalAuthority(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Profile != "" || decoded.Permissions != nil {
		t.Fatalf("legacy authority decoded as usable: %#v", decoded)
	}
	if err := decoded.Validate(); err == nil {
		t.Fatal("legacy authority sentinel unexpectedly validated")
	}
}

func TestUnmarshalAuthorityRejectsUnknownAndTrailingJSON(t *testing.T) {
	for _, encoded := range []string{
		`{"profile":"leapview.jobs/authority/v1","unknown":true}`,
		`{"profile":"leapview.jobs/authority/v1"}{}`,
	} {
		if _, err := UnmarshalAuthority([]byte(encoded)); err == nil {
			t.Fatalf("UnmarshalAuthority(%q) unexpectedly accepted", encoded)
		}
	}
}
