package settings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
)

type serviceAccountSecretExpiryMutator struct {
	input        access.ServicePrincipalSecretInput
	updatedID    string
	updatedInput access.ServicePrincipalInput
}

type serviceAccountCreationRepository struct {
	access.Repository
	audit access.AuditEventInput
}

func (r *serviceAccountCreationRepository) CreateServicePrincipal(context.Context, access.ServicePrincipalInput) (access.Principal, error) {
	return access.Principal{ID: "created-service-account", Kind: access.PrincipalKindServicePrincipal}, nil
}

func (r *serviceAccountCreationRepository) RecordAuditEvent(_ context.Context, input access.AuditEventInput) error {
	r.audit = input
	return nil
}

func (m *serviceAccountSecretExpiryMutator) CreateServicePrincipal(context.Context, access.ServicePrincipalInput) (access.Principal, error) {
	return access.Principal{ID: "svc-1", Kind: access.PrincipalKindServicePrincipal}, nil
}

func (m *serviceAccountSecretExpiryMutator) UpdateServicePrincipal(_ context.Context, id string, input access.ServicePrincipalInput) (access.Principal, error) {
	m.updatedID = id
	m.updatedInput = input
	return access.Principal{ID: id, Kind: access.PrincipalKindServicePrincipal, DisplayName: input.DisplayName}, nil
}

func (m *serviceAccountSecretExpiryMutator) DeleteServicePrincipal(context.Context, string) error {
	return nil
}

func (m *serviceAccountSecretExpiryMutator) CreateServicePrincipalSecret(_ context.Context, _ string, input access.ServicePrincipalSecretInput) (string, access.ServicePrincipalSecret, error) {
	m.input = input
	return "secret", access.ServicePrincipalSecret{ID: "secret-1"}, nil
}

func (m *serviceAccountSecretExpiryMutator) RevokeServicePrincipalSecret(context.Context, string, string) error {
	return nil
}

func TestResolveServiceAccountSecretExpiryUsesSharedDefaultAndMaximum(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		command ServiceAccountCommand
		want    time.Time
		wantErr error
	}{
		{name: "omitted", command: ServiceAccountCommand{}, want: now.Add(access.ServicePrincipalSecretDefaultLifetime)},
		{name: "preset", command: ServiceAccountCommand{SecretLifetimeDays: 365}, want: now.Add(access.ServicePrincipalSecretMaxLifetime)},
		{name: "over maximum", command: ServiceAccountCommand{SecretLifetimeDays: 366}, wantErr: access.ErrCredentialExpiryTooFar},
		{name: "past date", command: ServiceAccountCommand{ExpiresAt: now.Add(-time.Minute).Format(time.RFC3339)}, wantErr: access.ErrCredentialExpiryInPast},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveServiceAccountSecretExpiry(test.command, now)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want errors.Is(..., %v)", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve expiry: %v", err)
			}
			if !got.Equal(test.want) {
				t.Fatalf("expiry = %s, want %s", got, test.want)
			}
		})
	}
}

func TestApplyServiceAccountCommandPassesResolvedSecretExpiry(t *testing.T) {
	mutator := &serviceAccountSecretExpiryMutator{}
	_, _, err := applyServiceAccountCommand(context.Background(), mutator, ServiceAccountCommand{
		Action: "create_secret", AccountID: "svc-1", SecretName: "CI", SecretLifetimeDays: 90,
	})
	if err != nil {
		t.Fatalf("apply command: %v", err)
	}
	if !mutator.input.ExpiresAt.After(time.Now().UTC().Add(89 * 24 * time.Hour)) {
		t.Fatalf("resolved expiry = %s, want roughly 90 days from now", mutator.input.ExpiresAt)
	}
}

func TestApplyServiceAccountCommandUpdatesServiceAccountDisplayName(t *testing.T) {
	mutator := &serviceAccountSecretExpiryMutator{}
	secret, targetID, err := applyServiceAccountCommand(context.Background(), mutator, ServiceAccountCommand{
		Action: "update", AccountID: "svc-1", DisplayName: "  Production deploy  ",
	})
	if err != nil {
		t.Fatalf("apply command: %v", err)
	}
	if secret != "" || targetID != "svc-1" {
		t.Fatalf("update result = secret %q target %q, want empty secret and svc-1", secret, targetID)
	}
	if mutator.updatedID != "svc-1" || mutator.updatedInput.ID != "svc-1" || mutator.updatedInput.DisplayName != "Production deploy" {
		t.Fatalf("updated service account = id %q input %#v, want trimmed display name", mutator.updatedID, mutator.updatedInput)
	}
}

func TestAuditedServiceAccountCreationReturnsNewSelection(t *testing.T) {
	repository := &serviceAccountCreationRepository{}
	secret, targetID, err := ApplyServiceAccountCommandAudited(t.Context(), repository, "administrator", ServiceAccountCommand{
		Action: "create", DisplayName: "Deployment bot",
	})
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" || targetID != "created-service-account" {
		t.Fatalf("creation result = secret %q target %q, want selected new account", secret, targetID)
	}
	if repository.audit.ResourceID != targetID || repository.audit.Action != "service_principal.created" {
		t.Fatalf("creation audit = %#v, want created service account", repository.audit)
	}
}
