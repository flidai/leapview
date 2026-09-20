package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
)

type ServiceAccountMutator interface {
	CreateServicePrincipal(context.Context, access.ServicePrincipalInput) (access.Principal, error)
	UpdateServicePrincipal(context.Context, string, access.ServicePrincipalInput) (access.Principal, error)
	DeleteServicePrincipal(context.Context, string) error
	CreateServicePrincipalSecret(context.Context, string, access.ServicePrincipalSecretInput) (string, access.ServicePrincipalSecret, error)
	RevokeServicePrincipalSecret(context.Context, string, string) error
}

type serviceAccountLifecycle interface {
	DisableServicePrincipal(context.Context, string) (access.Principal, error)
	EnableServicePrincipal(context.Context, string) (access.Principal, error)
	RevokeAllServicePrincipalCredentials(context.Context, string) error
}

type serviceAccountRotator interface {
	RotateServicePrincipalSecret(context.Context, access.ServicePrincipalSecretRotationInput) (access.ServicePrincipalSecretRotation, error)
}

func applyServiceAccountCommand(ctx context.Context, mutator ServiceAccountMutator, command ServiceAccountCommand) (string, string, error) {
	if mutator == nil {
		return "", "", errors.New("service account mutator is nil")
	}
	command = NormalizeServiceAccountCommand(command)
	switch command.Action {
	case "create":
		if command.DisplayName == "" {
			return "", "", errors.New("display name is required")
		}
		principal, err := mutator.CreateServicePrincipal(ctx, access.ServicePrincipalInput{ID: command.AccountID, DisplayName: command.DisplayName})
		return "", principal.ID, err
	case "update":
		if command.AccountID == "" || command.DisplayName == "" {
			return "", "", errors.New("account id and display name are required")
		}
		_, err := mutator.UpdateServicePrincipal(ctx, command.AccountID, access.ServicePrincipalInput{ID: command.AccountID, DisplayName: command.DisplayName})
		return "", command.AccountID, err
	case "delete":
		if command.AccountID == "" {
			return "", "", errors.New("account id is required")
		}
		return "", command.AccountID, mutator.DeleteServicePrincipal(ctx, command.AccountID)
	case "disable":
		if command.AccountID == "" {
			return "", "", errors.New("account id is required")
		}
		lifecycle, ok := mutator.(serviceAccountLifecycle)
		if !ok {
			return "", "", errors.New("service principal lifecycle is unavailable")
		}
		_, err := lifecycle.DisableServicePrincipal(ctx, command.AccountID)
		return "", command.AccountID, err
	case "enable":
		if command.AccountID == "" {
			return "", "", errors.New("account id is required")
		}
		lifecycle, ok := mutator.(serviceAccountLifecycle)
		if !ok {
			return "", "", errors.New("service principal lifecycle is unavailable")
		}
		_, err := lifecycle.EnableServicePrincipal(ctx, command.AccountID)
		return "", command.AccountID, err
	case "create_secret":
		if command.AccountID == "" || command.SecretName == "" {
			return "", "", errors.New("account id and secret name are required")
		}
		expiresAt, err := resolveServiceAccountSecretExpiry(command, time.Now().UTC())
		if err != nil {
			return "", "", err
		}
		raw, _, err := mutator.CreateServicePrincipalSecret(ctx, command.AccountID, access.ServicePrincipalSecretInput{Name: command.SecretName, ExpiresAt: expiresAt})
		return raw, command.AccountID, err
	case "revoke_secret":
		if command.AccountID == "" || command.SecretID == "" {
			return "", "", errors.New("account id and secret id are required")
		}
		return "", command.AccountID, mutator.RevokeServicePrincipalSecret(ctx, command.AccountID, command.SecretID)
	case "rotate_secret":
		if command.AccountID == "" || command.SecretID == "" || command.SecretName == "" {
			return "", "", errors.New("account id, secret id, and secret name are required")
		}
		rotator, ok := mutator.(serviceAccountRotator)
		if !ok {
			return "", "", errors.New("service principal secret rotation is unavailable")
		}
		expiresAt, err := resolveServiceAccountSecretExpiry(command, time.Now().UTC())
		if err != nil {
			return "", "", err
		}
		rotation, err := rotator.RotateServicePrincipalSecret(ctx, access.ServicePrincipalSecretRotationInput{
			ServicePrincipalID: command.AccountID,
			PreviousSecretID:   command.SecretID,
			Secret:             access.ServicePrincipalSecretInput{Name: command.SecretName, ExpiresAt: expiresAt},
			RevokePrevious:     command.RevokePrevious,
		})
		return rotation.Secret, command.AccountID, err
	case "revoke_all":
		if command.AccountID == "" {
			return "", "", errors.New("account id is required")
		}
		lifecycle, ok := mutator.(serviceAccountLifecycle)
		if !ok {
			return "", "", errors.New("service principal credential revocation is unavailable")
		}
		return "", command.AccountID, lifecycle.RevokeAllServicePrincipalCredentials(ctx, command.AccountID)
	default:
		return "", "", errors.New("unknown service account action")
	}
}

func resolveServiceAccountSecretExpiry(command ServiceAccountCommand, now time.Time) (time.Time, error) {
	if command.SecretLifetimeDays < 0 {
		return time.Time{}, fmt.Errorf("%w: secretLifetimeDays cannot be negative", access.ErrCredentialExpiryInPast)
	}
	if command.SecretLifetimeDays > 0 && command.ExpiresAt != "" {
		return time.Time{}, errors.New("expiresAt and secretLifetimeDays cannot both be set")
	}
	if command.SecretLifetimeDays > 0 {
		maxDays := int(access.ServicePrincipalSecretMaxLifetime / (24 * time.Hour))
		if command.SecretLifetimeDays > maxDays {
			return time.Time{}, fmt.Errorf("%w: secretLifetimeDays must be at most %d", access.ErrCredentialExpiryTooFar, maxDays)
		}
		return access.ResolveServicePrincipalSecretExpiry(
			now.Add(time.Duration(command.SecretLifetimeDays)*24*time.Hour), now,
		)
	}
	if command.ExpiresAt == "" {
		return access.ResolveServicePrincipalSecretExpiry(time.Time{}, now)
	}
	expiresAt, err := time.Parse(time.RFC3339, command.ExpiresAt)
	if err != nil {
		return time.Time{}, errors.New("expiresAt must be RFC3339")
	}
	return access.ResolveServicePrincipalSecretExpiry(expiresAt, now)
}

func ApplyServiceAccountCommandAudited(ctx context.Context, repository access.Repository, actorID string, command ServiceAccountCommand) (string, error) {
	command = NormalizeServiceAccountCommand(command)
	auditAction, ok := serviceAccountAuditAction(command.Action)
	if !ok {
		return "", errors.New("unknown service account action")
	}
	var secret string
	mutation := func(tx access.Repository) (access.AuditEventInput, error) {
		createdSecret, targetID, err := applyServiceAccountCommand(ctx, tx, command)
		secret = createdSecret
		metadata := map[string]any{}
		if command.Action == "rotate_secret" {
			metadata["secretId"] = command.SecretID
			metadata["revokePrevious"] = command.RevokePrevious
		}
		if command.Action == "revoke_all" && command.Reason != "" {
			metadata["reason"] = command.Reason
		}
		return access.AuditEventInput{
			PrincipalID: actorID, Action: auditAction,
			ResourceKind: "service_principal", ResourceID: targetID, Status: "success", MetadataJSON: encodeServiceAccountMetadata(metadata),
		}, err
	}
	if transactional, ok := repository.(access.AuditedMutationRepository); ok {
		if err := transactional.RunAuditedMutation(ctx, mutation); err != nil {
			return "", err
		}
		return secret, nil
	}
	event, err := mutation(repository)
	if err != nil {
		return "", err
	}
	if err := repository.RecordAuditEvent(ctx, event); err != nil {
		return "", err
	}
	return secret, nil
}

// serviceAccountAuditAction is the compatibility boundary for the legacy
// settings command names. Durable audit actions are TypeSpec vocabulary and
// must not be synthesized from UI command strings.
func serviceAccountAuditAction(commandAction string) (string, bool) {
	switch commandAction {
	case "create":
		return "service_principal.created", true
	case "update":
		return "service_principal.updated", true
	case "delete":
		return "service_principal.deleted", true
	case "disable":
		return "service_principal.disabled", true
	case "enable":
		return "service_principal.enabled", true
	case "create_secret":
		return "service_principal_secret.created", true
	case "revoke_secret":
		return "service_principal_secret.revoked", true
	case "rotate_secret":
		return "service_principal_secret.rotated", true
	case "revoke_all":
		return "service_principal_credentials.revoked_all", true
	default:
		return "", false
	}
}

func NormalizeServiceAccountCommand(command ServiceAccountCommand) ServiceAccountCommand {
	command.Action = strings.TrimSpace(command.Action)
	command.AccountID = strings.TrimSpace(command.AccountID)
	command.SecretID = strings.TrimSpace(command.SecretID)
	command.DisplayName = strings.TrimSpace(command.DisplayName)
	command.SecretName = strings.TrimSpace(command.SecretName)
	command.ExpiresAt = strings.TrimSpace(command.ExpiresAt)
	command.Reason = strings.TrimSpace(command.Reason)
	if len(command.Reason) > 512 {
		command.Reason = command.Reason[:512]
	}
	return command
}

func encodeServiceAccountMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return `{}`
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return `{}`
	}
	return string(encoded)
}
