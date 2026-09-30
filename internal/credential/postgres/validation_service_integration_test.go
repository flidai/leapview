package postgres

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential"
	"github.com/google/uuid"
)

type validationIntegrationProbe struct {
	target credential.ValidationTarget
	probe  func(context.Context, credential.ValidationTarget, string, map[string]string) error
}

func (p *validationIntegrationProbe) ResolveValidationTarget(context.Context, credential.Resource, credential.Scope) (credential.ValidationTarget, error) {
	return p.target, nil
}

func (p *validationIntegrationProbe) ProbeCredential(ctx context.Context, target credential.ValidationTarget, version string, fields map[string]string) error {
	return p.probe(ctx, target, version, fields)
}

type validationIntegrationAuthorizer struct{ revoked bool }

func (a *validationIntegrationAuthorizer) RequirePermission(_ context.Context, _ string, pair access.PermissionPair) error {
	if a.revoked || (pair.Action != access.ActionConnectionManage && pair.Action != access.ActionConnectionUse) {
		return credential.ErrForbidden
	}
	return nil
}

func TestSavedDraftValidationUsesEncryptedVersionAndCommitsOnlyEvidence(t *testing.T) {
	db, _, repository := credentialDB(t)
	keys := loadTestCredentialKeyring(t, "validation-deployment", "validation-key", bytes.Repeat([]byte{0x58}, 32))
	resource := credential.Resource{ScopeKind: "connection", TargetID: "target", ProjectID: "project", Environment: "production", ResourceID: "warehouse"}
	scope := credential.Scope{Resource: resource, OwnerID: "customer", Purpose: "connection-authentication", Provider: "postgres", Destination: "sha256:" + strings.Repeat("a", 64)}
	drafts, err := credential.NewService(repository, keys, testCredentialScopeResolver{scope}, allowCredentialAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	actor := uuid.NewString()
	const password = "exact-saved-password-sentinel"
	draft, err := drafts.SaveDraft(t.Context(), actor, resource, map[string]string{"password": password})
	if err != nil {
		t.Fatal(err)
	}
	authority := &validationIntegrationAuthorizer{}
	probe := &validationIntegrationProbe{target: credential.ValidationTarget{
		Scope: scope, BindingID: "binding", BindingRevision: 7, ConfigurationDigest: "sha256:" + strings.Repeat("b", 64),
	}}
	var retainedFields map[string]string
	probe.probe = func(ctx context.Context, target credential.ValidationTarget, version string, fields map[string]string) error {
		if target != probe.target || version != draft.Binding.VersionID || fields["password"] != password {
			t.Fatal("probe did not receive the exact saved version and server-owned target")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second {
			t.Fatal("probe was not bounded by the validation deadline")
		}
		retainedFields = fields
		return nil
	}
	validation, err := credential.NewValidationService(repository, keys, testCredentialScopeResolver{scope}, authority, probe, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := validation.ValidateDraft(t.Context(), actor, resource, draft.Binding.VersionID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Binding != draft.Binding || receipt.ActorID != actor || receipt.BindingRevision != 7 || receipt.ExpiresAt.Sub(receipt.ValidatedAt) != 5*time.Minute {
		t.Fatal("receipt lost the exact version, actor, revision or bounded lifetime")
	}
	if len(retainedFields) != 0 {
		t.Fatal("validation retained the plaintext field map after probing")
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.validation_receipt WHERE receipt_id=$1 AND version_id=$2`, receipt.ReceiptID, draft.Binding.VersionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("receipt was not persisted once: count=%d err=%v", count, err)
	}
	var auditJSON string
	if err := db.QueryRow(t.Context(), `SELECT metadata::text FROM audit.audit_event WHERE aggregate_key=$1`, "credential-validation:"+receipt.ReceiptID).Scan(&auditJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditJSON, password) || !strings.Contains(auditJSON, receipt.ReceiptID) || !strings.Contains(auditJSON, draft.Binding.VersionID) {
		t.Fatal("validation audit omitted receipt/version evidence or exposed plaintext")
	}
	metadata, err := drafts.GetDraft(t.Context(), actor, resource, draft.Binding.VersionID)
	if err != nil || metadata.Binding != draft.Binding || metadata.ActorID != draft.ActorID {
		t.Fatalf("validation changed the stored draft: %v", err)
	}

	// A successful network result cannot survive authority revocation during it.
	probe.probe = func(context.Context, credential.ValidationTarget, string, map[string]string) error {
		authority.revoked = true
		return nil
	}
	if _, err := validation.ValidateDraft(t.Context(), actor, resource, draft.Binding.VersionID, 7); !errors.Is(err, credential.ErrForbidden) {
		t.Fatalf("post-probe revocation = %v, want forbidden", err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.validation_receipt WHERE version_id=$1`, draft.Binding.VersionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("revoked validation persisted evidence: count=%d err=%v", count, err)
	}
}
