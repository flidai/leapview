package adminpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/flidai/leapview/internal/access"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	platformtypednil "github.com/flidai/leapview/internal/platform/typednil"
	"github.com/google/uuid"
)

type platformAdminRecoveryEvidence struct {
	Mode               string `json:"mode"`
	OperationID        string `json:"operationId"`
	PrincipalID        string `json:"principalId"`
	Email              string `json:"email"`
	AlreadyAdmin       bool   `json:"alreadyAdmin"`
	LocalPasswordReset bool   `json:"localPasswordReset"`
	PreviousRevision   string `json:"previousRevision"`
	ResultRevision     string `json:"resultRevision,omitempty"`
	BindingID          string `json:"bindingId,omitempty"`
}

const platformAdminRecoveryIdempotencyPrefix = "offline-platform-admin-recovery:"

// RecoverPlatformAdministrator restores platform authority to one existing,
// enabled principal through a production-only offline operator path. Preview
// is the default; apply uses the normal CAS/idempotency writer and commits the
// grant and immutable audit event in one PostgreSQL transaction.
func (o Operations) RecoverPlatformAdministrator(ctx context.Context, request admincli.PlatformAdminRecoveryRequest, out io.Writer) error {
	if out == nil {
		return errors.New("platform administrator recovery output is required")
	}
	principalID := strings.TrimSpace(request.PrincipalID)
	expectedEmail := access.NormalizeEmail(request.ExpectedEmail)
	operationID := strings.TrimSpace(request.OperationID)
	expectedRevision := strings.TrimSpace(request.ExpectedRevision)
	parsedPrincipalID, err := uuid.Parse(principalID)
	if err != nil {
		return fmt.Errorf("platform administrator recovery principal ID must be a UUID: %w", err)
	}
	principalID = parsedPrincipalID.String()
	if expectedEmail == "" {
		return errors.New("platform administrator recovery expected email is required")
	}
	parsedOperationID, err := uuid.Parse(operationID)
	if err != nil {
		return fmt.Errorf("platform administrator recovery operation ID must be a UUID: %w", err)
	}
	operationID = parsedOperationID.String()
	if !request.AcknowledgeOfflineRecovery {
		return errors.New("platform administrator recovery acknowledgement is required")
	}
	if request.Apply && expectedRevision == "" {
		return errors.New("platform administrator recovery expected revision is required with apply")
	}
	passwordFile := strings.TrimSpace(request.LocalPasswordFile)
	if passwordFile != "" && !request.Apply {
		return errors.New("platform administrator local password recovery requires apply")
	}
	if passwordFile != "" && !request.AcknowledgeCredentialReset {
		return errors.New("platform administrator local password recovery acknowledgement is required")
	}

	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Production {
		return ErrNativeAdminUnavailable
	}
	var recoveryPassword string
	if passwordFile != "" {
		if !cfg.LocalAuth {
			return errors.New("platform administrator local password recovery requires LEAPVIEW_LOCAL_AUTH=true")
		}
		recoveryPassword, err = readRecoveryPasswordFile(passwordFile)
		if err != nil {
			return err
		}
	}
	accessConfig, err := productionAccessConfig(cfg)
	if err != nil {
		return err
	}
	key, err := accessFingerprintKey(cfg)
	if err != nil {
		return err
	}
	pool, err := deps.OpenAccess(ctx, accessConfig)
	if err != nil {
		return fmt.Errorf("open PostgreSQL access pool: %w", err)
	}
	if nilAccessPool(pool) {
		return errors.New("open PostgreSQL access pool returned nil pool")
	}
	defer pool.Close()
	if err := deps.VerifyBaseline(ctx, pool); err != nil {
		return fmt.Errorf("verify PostgreSQL control baseline before platform administrator recovery: %w", err)
	}
	authority, err := deps.NewRecoveryAccess(pool, key)
	if err != nil {
		return fmt.Errorf("construct PostgreSQL platform administrator recovery authority: %w", err)
	}
	if platformtypednil.IsNil(authority) {
		return errors.New("construct PostgreSQL platform administrator recovery authority returned nil authority")
	}
	principal, err := authority.PrincipalByID(ctx, principalID)
	if err != nil {
		return fmt.Errorf("resolve platform administrator recovery principal: %w", err)
	}
	if access.NormalizeEmail(principal.Email) != expectedEmail {
		return fmt.Errorf("platform administrator recovery principal email does not match --expected-email")
	}
	resolvedPrincipalID, err := uuid.Parse(strings.TrimSpace(principal.ID))
	if err != nil || resolvedPrincipalID.String() != principalID {
		return errors.New("platform administrator recovery authority returned an unexpected principal")
	}
	if principal.Kind != access.PrincipalKindUser {
		return fmt.Errorf("platform administrator recovery principal %q is not a user", principalID)
	}
	if principal.AccessDisabled() {
		return fmt.Errorf("platform administrator recovery principal %q is not enabled", principalID)
	}
	state, err := authority.ListPlatformAdministrators(ctx)
	if err != nil {
		return fmt.Errorf("list platform administrators before recovery: %w", err)
	}
	alreadyAdmin := platformAdminStateContains(state, principalID)
	evidence := platformAdminRecoveryEvidence{
		Mode: "preview", OperationID: operationID, PrincipalID: principalID, Email: expectedEmail,
		AlreadyAdmin: alreadyAdmin, PreviousRevision: state.Revision,
	}
	// Preview may optionally verify a revision supplied by an earlier read. Do
	// not perform this check before an apply mutation: idempotent replays must be
	// handed to the writer even when unrelated delegation changes happened after
	// the original successful apply.
	if !request.Apply && expectedRevision != "" && expectedRevision != state.Revision {
		return fmt.Errorf("platform administrator recovery expected revision is stale")
	}
	if !request.Apply {
		return writePlatformAdminRecoveryEvidence(out, evidence)
	}

	var result access.PlatformAdminGrantResult
	localPasswordReset := false
	err = authority.RunAuditedMutation(ctx, func(transaction access.Repository) (access.AuditEventInput, error) {
		lister, ok := transaction.(access.PlatformAdminLister)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional platform administrator lister is unavailable")
		}
		writer, ok := transaction.(access.PlatformAdminWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional platform administrator writer is unavailable")
		}
		_, listErr := lister.ListPlatformAdministrators(ctx)
		if listErr != nil {
			return access.AuditEventInput{}, listErr
		}
		result, err = writer.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{
			PrincipalID: principalID, ExpectedRevision: expectedRevision,
			IdempotencyKey: platformAdminRecoveryIdempotencyPrefix + operationID,
		})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		if recoveryPassword != "" {
			writer, ok := transaction.(access.LocalPasswordRecoveryWriter)
			if !ok {
				return access.AuditEventInput{}, errors.New("transactional local password recovery is unavailable")
			}
			reset, resetErr := writer.RecoverLocalPassword(ctx, principalID, recoveryPassword)
			if resetErr != nil {
				return access.AuditEventInput{}, fmt.Errorf("recover local password: %w", resetErr)
			}
			if reset.Principal.ID != principalID {
				return access.AuditEventInput{}, errors.New("local password recovery returned an unexpected principal")
			}
			localPasswordReset = true
		}
		metadata, encodeErr := json.Marshal(map[string]any{
			"bindingId": result.Administrator.BindingID, "operationId": operationID,
			"principalId": principalID, "recoveryMode": "offline_operator",
			"revision": result.State.Revision, "localPasswordReset": localPasswordReset,
		})
		if encodeErr != nil {
			return access.AuditEventInput{}, encodeErr
		}
		return access.AuditEventInput{
			PrincipalID: principalID, Action: "platform_admin.recovered", ResourceKind: "platform_role_binding",
			ResourceID: result.Administrator.BindingID, Status: "success", MetadataJSON: string(metadata),
		}, nil
	})
	if err != nil {
		return fmt.Errorf("recover platform administrator: %w", err)
	}
	evidence.Mode = "apply"
	evidence.BindingID = result.Administrator.BindingID
	evidence.ResultRevision = result.State.Revision
	evidence.LocalPasswordReset = localPasswordReset
	return writePlatformAdminRecoveryEvidence(out, evidence)
}

func readRecoveryPasswordFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect platform administrator recovery password file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("platform administrator recovery password file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("platform administrator recovery password file must not be accessible by group or other users")
	}
	if info.Size() > 4096 {
		return "", errors.New("platform administrator recovery password file exceeds 4096 bytes")
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read platform administrator recovery password file: %w", err)
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(value), "\n"), "\r")
	if strings.ContainsAny(password, "\r\n") {
		return "", errors.New("platform administrator recovery password file must contain exactly one line")
	}
	if err := access.ValidateLocalPassword(password); err != nil {
		return "", fmt.Errorf("platform administrator recovery password: %w", err)
	}
	return password, nil
}

func platformAdminStateContains(state access.PlatformAdministratorState, principalID string) bool {
	for _, item := range state.Administrators {
		if item.Principal.ID == principalID {
			return true
		}
	}
	return false
}

func writePlatformAdminRecoveryEvidence(out io.Writer, evidence platformAdminRecoveryEvidence) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(evidence); err != nil {
		return fmt.Errorf("encode platform administrator recovery evidence: %w", err)
	}
	return nil
}
