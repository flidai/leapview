package settings

import (
	"context"
	"errors"
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
	case "create_secret":
		if command.AccountID == "" || command.SecretName == "" {
			return "", "", errors.New("account id and secret name are required")
		}
		var expiresAt time.Time
		if command.ExpiresAt != "" {
			parsed, err := time.Parse(time.RFC3339, command.ExpiresAt)
			if err != nil {
				return "", "", errors.New("expiresAt must be RFC3339")
			}
			expiresAt = parsed
		}
		raw, _, err := mutator.CreateServicePrincipalSecret(ctx, command.AccountID, access.ServicePrincipalSecretInput{Name: command.SecretName, ExpiresAt: expiresAt})
		return raw, command.AccountID, err
	case "revoke_secret":
		if command.AccountID == "" || command.SecretID == "" {
			return "", "", errors.New("account id and secret id are required")
		}
		return "", command.AccountID, mutator.RevokeServicePrincipalSecret(ctx, command.AccountID, command.SecretID)
	default:
		return "", "", errors.New("unknown service account action")
	}
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
		return access.AuditEventInput{
			PrincipalID: actorID, Action: auditAction,
			ResourceKind: "service_principal", ResourceID: targetID, Status: "success", MetadataJSON: `{}`,
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
	case "create_secret":
		return "service_principal_secret.created", true
	case "revoke_secret":
		return "service_principal_secret.revoked", true
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
	return command
}
