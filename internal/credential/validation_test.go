package credential

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential/encryption"
	"github.com/google/uuid"
)

func TestValidationServiceProbesExactSavedVersionAndPersistsOnlyReceipt(t *testing.T) {
	fixture := newValidationFixture(t, map[string]string{"password": "sentinel-validation-secret"})
	var retainedFields map[string]string
	fixture.probe.probe = func(ctx context.Context, target ValidationTarget, versionID string, fields map[string]string) error {
		if target != fixture.target || versionID != fixture.version.Metadata.Binding.VersionID || fields["password"] != "sentinel-validation-secret" {
			t.Fatal("probe did not receive the saved version and server-owned target")
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > validationProbeTimeout {
			t.Fatal("probe context has no bounded deadline")
		}
		retainedFields = fields
		return nil
	}

	receipt, err := fixture.service.ValidateDraft(t.Context(), "actor", fixture.resource, fixture.version.Metadata.Binding.VersionID, fixture.target.BindingRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("receipt is invalid: %v", err)
	}
	if receipt.Binding != fixture.version.Metadata.Binding || receipt.ActorID != "actor" ||
		receipt.BindingID != fixture.target.BindingID || receipt.BindingRevision != fixture.target.BindingRevision ||
		receipt.ConfigurationDigest != fixture.target.ConfigurationDigest ||
		receipt.ExpiresAt.Sub(receipt.ValidatedAt) != validationReceiptTTL {
		t.Fatalf("receipt does not bind the exact draft and target: %#v", receipt)
	}
	if len(retainedFields) != 0 {
		t.Fatal("validation retained the decrypted field map after probing")
	}
	if len(fixture.keys.lastPlaintext) == 0 {
		t.Fatal("fake keyring did not record plaintext for clearing assertion")
	}
	for _, value := range fixture.keys.lastPlaintext {
		if value != 0 {
			t.Fatal("validation retained decrypted plaintext bytes after probing")
		}
	}
	if len(fixture.repository.receipts) != 1 || len(fixture.repository.audits) != 1 {
		t.Fatal("successful validation did not persist exactly one receipt and audit")
	}
	if _, err := ValidateValidationAuditIntent(receipt, fixture.repository.audits[0]); err != nil {
		t.Fatalf("receipt audit is invalid: %v", err)
	}
	if bytes.Contains([]byte(fixture.repository.audits[0].MetadataJSON), []byte("sentinel-validation-secret")) {
		t.Fatal("audit metadata contains secret input")
	}
	if len(fixture.authorizer.pairs) != 4 ||
		fixture.authorizer.pairs[0].Action != access.ActionConnectionManage ||
		fixture.authorizer.pairs[1].Action != access.ActionConnectionUse ||
		fixture.authorizer.pairs[2].Action != access.ActionConnectionManage ||
		fixture.authorizer.pairs[3].Action != access.ActionConnectionUse {
		t.Fatalf("validation did not require manage and use before and after probing: %#v", fixture.authorizer.pairs)
	}
	if fixture.repository.version.Metadata.Binding != receipt.Binding {
		t.Fatal("validation mutated saved draft metadata")
	}
}

func TestValidationServiceRejectsUnsupportedProviderAndCredentialShapesBeforeProbe(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		fields   map[string]string
	}{
		{name: "connection string", fields: map[string]string{"connection_string": "host=attacker password=secret"}},
		{name: "mixed fields", fields: map[string]string{"password": "secret", "connection_string": "host=attacker"}},
		{name: "unsupported provider", provider: "mysql", fields: map[string]string{"password": "secret"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newValidationFixture(t, test.fields)
			if test.provider != "" {
				fixture.scope.Provider = test.provider
				fixture.target.Scope = fixture.scope
				fixture.scopes.scope = fixture.scope
				fixture.probe.target = fixture.target
				fixture.version.Metadata.Binding.Provider = test.provider
			}
			fixture.probe.probe = func(context.Context, ValidationTarget, string, map[string]string) error {
				t.Fatal("unsupported credential reached provider probe")
				return nil
			}
			_, err := fixture.service.ValidateDraft(t.Context(), "actor", fixture.resource, fixture.version.Metadata.Binding.VersionID, fixture.target.BindingRevision)
			if !errors.Is(err, ErrValidationFailed) {
				t.Fatalf("unsupported credential error = %v, want generic validation failure", err)
			}
			if len(fixture.repository.receipts) != 0 {
				t.Fatal("unsupported credential produced a receipt")
			}
		})
	}
}

func TestValidationServiceRejectsStaleRevisionDriftAndPostProbeRevocation(t *testing.T) {
	t.Run("expected revision", func(t *testing.T) {
		fixture := newValidationFixture(t, map[string]string{"password": "secret"})
		_, err := fixture.service.ValidateDraft(t.Context(), "actor", fixture.resource, fixture.version.Metadata.Binding.VersionID, fixture.target.BindingRevision+1)
		if !errors.Is(err, ErrConflict) || fixture.keys.decryptCalls != 0 || fixture.probe.probeCalls != 0 {
			t.Fatalf("stale expected revision = %v; decrypts=%d probes=%d", err, fixture.keys.decryptCalls, fixture.probe.probeCalls)
		}
	})
	t.Run("target drift during probe", func(t *testing.T) {
		fixture := newValidationFixture(t, map[string]string{"password": "secret"})
		fixture.probe.probe = func(context.Context, ValidationTarget, string, map[string]string) error {
			fixture.probe.target.BindingRevision++
			return nil
		}
		_, err := fixture.service.ValidateDraft(t.Context(), "actor", fixture.resource, fixture.version.Metadata.Binding.VersionID, fixture.target.BindingRevision)
		if !errors.Is(err, ErrConflict) || len(fixture.repository.receipts) != 0 {
			t.Fatalf("target drift = %v, receipts=%d", err, len(fixture.repository.receipts))
		}
	})
	t.Run("authority revoked during probe", func(t *testing.T) {
		fixture := newValidationFixture(t, map[string]string{"password": "secret"})
		fixture.probe.probe = func(context.Context, ValidationTarget, string, map[string]string) error {
			fixture.authorizer.deny = true
			return nil
		}
		_, err := fixture.service.ValidateDraft(t.Context(), "actor", fixture.resource, fixture.version.Metadata.Binding.VersionID, fixture.target.BindingRevision)
		if !errors.Is(err, ErrForbidden) || len(fixture.repository.receipts) != 0 {
			t.Fatalf("post-probe revocation = %v, receipts=%d", err, len(fixture.repository.receipts))
		}
	})
}

func TestValidationServiceRejectsStoredProviderOrDestinationSubstitutionBeforeDecrypt(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*encryption.Binding)
	}{
		{name: "provider", mutate: func(binding *encryption.Binding) { binding.Provider = "mysql" }},
		{name: "destination", mutate: func(binding *encryption.Binding) { binding.Destination = "sha256:" + strings.Repeat("c", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newValidationFixture(t, map[string]string{"password": "secret"})
			test.mutate(&fixture.repository.version.Metadata.Binding)
			_, err := fixture.service.ValidateDraft(t.Context(), "actor", fixture.resource, fixture.version.Metadata.Binding.VersionID, fixture.target.BindingRevision)
			if !errors.Is(err, ErrNotFound) || fixture.keys.decryptCalls != 0 || fixture.probe.probeCalls != 0 {
				t.Fatalf("substituted saved scope = %v; decrypts=%d probes=%d", err, fixture.keys.decryptCalls, fixture.probe.probeCalls)
			}
		})
	}
}

func TestValidationServiceRejectsConfigurationDigestOnlyDrift(t *testing.T) {
	fixture := newValidationFixture(t, map[string]string{"password": "secret"})
	fixture.probe.resolveTarget = func(calls int, target ValidationTarget) ValidationTarget {
		if calls == 2 {
			target.ConfigurationDigest = "sha256:" + strings.Repeat("d", 64)
		}
		return target
	}
	_, err := fixture.service.ValidateDraft(t.Context(), "actor", fixture.resource, fixture.version.Metadata.Binding.VersionID, fixture.target.BindingRevision)
	if !errors.Is(err, ErrConflict) || len(fixture.repository.receipts) != 0 {
		t.Fatalf("configuration-only drift = %v; receipts=%d", err, len(fixture.repository.receipts))
	}
}

func TestValidationServiceRequiresUsePermissionBeforeDecryption(t *testing.T) {
	fixture := newValidationFixture(t, map[string]string{"password": "secret"})
	fixture.authorizer.denyAction = access.ActionConnectionUse
	_, err := fixture.service.ValidateDraft(t.Context(), "actor", fixture.resource, fixture.version.Metadata.Binding.VersionID, fixture.target.BindingRevision)
	if !errors.Is(err, ErrForbidden) || fixture.keys.decryptCalls != 0 || fixture.probe.probeCalls != 0 {
		t.Fatalf("missing use permission = %v; decrypts=%d probes=%d", err, fixture.keys.decryptCalls, fixture.probe.probeCalls)
	}
	if len(fixture.authorizer.pairs) != 2 || fixture.authorizer.pairs[0].Action != access.ActionConnectionManage || fixture.authorizer.pairs[1].Action != access.ActionConnectionUse {
		t.Fatalf("pre-decrypt authority checks = %#v", fixture.authorizer.pairs)
	}
}

func TestValidationServiceDoesNotReturnFreshReceiptAfterClockPassesExpiry(t *testing.T) {
	t.Run("during post-probe target checks", func(t *testing.T) {
		fixture := newValidationFixture(t, map[string]string{"password": "secret"})
		fixture.probe.afterResolve = func(calls int) {
			if calls == 2 {
				fixture.clock.current = fixture.clock.current.Add(validationReceiptTTL + time.Second)
			}
		}
		_, err := fixture.service.ValidateDraft(t.Context(), "actor", fixture.resource, fixture.version.Metadata.Binding.VersionID, fixture.target.BindingRevision)
		if !errors.Is(err, ErrConflict) || len(fixture.repository.receipts) != 0 {
			t.Fatalf("post-probe expiry = %v; receipts=%d", err, len(fixture.repository.receipts))
		}
	})
	t.Run("during receipt persistence", func(t *testing.T) {
		fixture := newValidationFixture(t, map[string]string{"password": "secret"})
		fixture.repository.afterSave = func() {
			fixture.clock.current = fixture.clock.current.Add(validationReceiptTTL + time.Second)
		}
		receipt, err := fixture.service.ValidateDraft(t.Context(), "actor", fixture.resource, fixture.version.Metadata.Binding.VersionID, fixture.target.BindingRevision)
		if !errors.Is(err, ErrConflict) || receipt != (ValidationReceipt{}) {
			t.Fatalf("expired persisted receipt returned as success: receipt=%#v err=%v", receipt, err)
		}
	})
}

func TestValidationServiceRejectsNilSuccessAfterParentDeadline(t *testing.T) {
	fixture := newValidationFixture(t, map[string]string{"password": "secret"})
	fixture.probe.probe = func(ctx context.Context, _ ValidationTarget, _ string, _ map[string]string) error {
		<-ctx.Done()
		return nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err := fixture.service.ValidateDraft(ctx, "actor", fixture.resource, fixture.version.Metadata.Binding.VersionID, fixture.target.BindingRevision)
	if !errors.Is(err, context.DeadlineExceeded) || len(fixture.repository.receipts) != 0 {
		t.Fatalf("nil result after deadline = %v; receipts=%d", err, len(fixture.repository.receipts))
	}
}

func TestValidationServiceChecksCancellationBeforeReceiptPersistence(t *testing.T) {
	for _, test := range []struct {
		name     string
		cancelAt func(*validationFixture, context.CancelFunc)
	}{
		{
			name: "final target read",
			cancelAt: func(fixture *validationFixture, cancel context.CancelFunc) {
				fixture.probe.afterResolve = func(calls int) {
					if calls == 2 {
						cancel()
					}
				}
			},
		},
		{
			name: "final authorization",
			cancelAt: func(fixture *validationFixture, cancel context.CancelFunc) {
				fixture.authorizer.afterCheck = func(calls int, pair access.PermissionPair) {
					if calls == 4 && pair.Action == access.ActionConnectionUse {
						cancel()
					}
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newValidationFixture(t, map[string]string{"password": "secret"})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			test.cancelAt(&fixture, cancel)
			_, err := fixture.service.ValidateDraft(ctx, "actor", fixture.resource, fixture.version.Metadata.Binding.VersionID, fixture.target.BindingRevision)
			if !errors.Is(err, context.Canceled) || fixture.repository.saveCalls != 0 || len(fixture.repository.receipts) != 0 {
				t.Fatalf("cancellation before receipt save = %v; saves=%d receipts=%d", err, fixture.repository.saveCalls, len(fixture.repository.receipts))
			}
		})
	}
}

func TestValidationServiceSuppressesProbeErrorsAndDoesNotPersistFailure(t *testing.T) {
	fixture := newValidationFixture(t, map[string]string{"password": "secret"})
	fixture.probe.probe = func(context.Context, ValidationTarget, string, map[string]string) error {
		return errors.New("provider error contains sentinel-secret")
	}
	_, err := fixture.service.ValidateDraft(t.Context(), "actor", fixture.resource, fixture.version.Metadata.Binding.VersionID, fixture.target.BindingRevision)
	if err != ErrValidationFailed || strings.Contains(err.Error(), "sentinel-secret") {
		t.Fatalf("probe error escaped safe boundary: %v", err)
	}
	if len(fixture.repository.receipts) != 0 {
		t.Fatal("failed probe persisted a receipt")
	}
}

func TestValidationReceiptAuditIntentRejectsMismatchedEvidence(t *testing.T) {
	fixture := newValidationFixture(t, map[string]string{"password": "secret"})
	receipt := ValidationReceipt{
		ReceiptID: uuid.NewString(), Binding: fixture.version.Metadata.Binding, ActorID: "actor",
		BindingID: fixture.target.BindingID, BindingRevision: fixture.target.BindingRevision,
		ConfigurationDigest: fixture.target.ConfigurationDigest,
		ValidatedAt:         time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC),
		ExpiresAt:           time.Date(2026, 9, 28, 10, 5, 0, 0, time.UTC),
	}
	intent, err := receipt.AuditIntent()
	if err != nil {
		t.Fatal(err)
	}
	intent.Action = "credential.draft.saved"
	if _, err := ValidateValidationAuditIntent(receipt, intent); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched audit action accepted: %v", err)
	}
}

type validationFixture struct {
	service    *ValidationService
	resource   Resource
	scope      Scope
	target     ValidationTarget
	version    StoredVersion
	repository *validationTestRepository
	keys       *validationTestKeyring
	scopes     *testScopeResolver
	authorizer *validationTestAuthorizer
	probe      *validationTestProbe
	clock      *validationTestClock
}

func newValidationFixture(t *testing.T, fields map[string]string) validationFixture {
	t.Helper()
	resource := connectionResource()
	scope := Scope{
		Resource: resource, OwnerID: "customer-a", Purpose: "connection-authentication",
		Provider: "postgres", Destination: "sha256:" + strings.Repeat("a", 64),
	}
	versionID := uuid.NewString()
	binding := expectedEncryptionBinding("deployment-a", scope, versionID)
	plaintext, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	version := StoredVersion{
		Metadata: Metadata{Binding: binding, ActorID: "draft-actor", CreatedAt: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)},
		Envelope: encryption.Envelope{Format: "test-only", KeyID: "key-a", Ciphertext: []byte("opaque")},
	}
	repository := &validationTestRepository{version: version}
	keys := &validationTestKeyring{plaintext: plaintext, binding: binding}
	target := ValidationTarget{
		Scope: scope, BindingID: "binding:a", BindingRevision: 7,
		ConfigurationDigest: "sha256:" + strings.Repeat("b", 64),
	}
	probe := &validationTestProbe{target: target}
	authorizer := &validationTestAuthorizer{}
	scopes := &testScopeResolver{scope: scope}
	clock := &validationTestClock{current: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)}
	service, err := NewValidationService(repository, keys, scopes, authorizer, probe, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	return validationFixture{
		service: service, resource: resource, scope: scope, target: target,
		version: version, repository: repository, keys: keys,
		scopes: scopes, authorizer: authorizer, probe: probe, clock: clock,
	}
}

type validationTestClock struct{ current time.Time }

func (clock *validationTestClock) Now() time.Time { return clock.current }

type validationTestRepository struct {
	version   StoredVersion
	receipts  []ValidationReceipt
	audits    []access.AuditIntent
	saveCalls int
	afterSave func()
}

func (r *validationTestRepository) GetStoredDraft(_ context.Context, deploymentID, ownerID string, resource Resource, versionID string) (StoredVersion, error) {
	if metadataMatchesScope(r.version.Metadata, deploymentID, ownerID, resource) && r.version.Metadata.Binding.VersionID == versionID {
		return r.version, nil
	}
	return StoredVersion{}, ErrNotFound
}

func (r *validationTestRepository) SaveValidation(_ context.Context, receipt ValidationReceipt, intent access.AuditIntent) error {
	r.saveCalls++
	if _, err := ValidateValidationAuditIntent(receipt, intent); err != nil {
		return err
	}
	r.receipts = append(r.receipts, receipt)
	r.audits = append(r.audits, intent)
	if r.afterSave != nil {
		r.afterSave()
	}
	return nil
}

type validationTestKeyring struct {
	plaintext     []byte
	lastPlaintext []byte
	binding       encryption.Binding
	decryptCalls  int
}

func (*validationTestKeyring) DeploymentID() string { return "deployment-a" }

func (k *validationTestKeyring) Decrypt(binding encryption.Binding, _ encryption.Envelope) ([]byte, error) {
	k.decryptCalls++
	if binding != k.binding {
		return nil, ErrValidationFailed
	}
	k.lastPlaintext = bytes.Clone(k.plaintext)
	return k.lastPlaintext, nil
}

type validationTestAuthorizer struct {
	pairs      []access.PermissionPair
	deny       bool
	denyAction access.Action
	afterCheck func(int, access.PermissionPair)
}

func (a *validationTestAuthorizer) RequirePermission(_ context.Context, _ string, pair access.PermissionPair) error {
	a.pairs = append(a.pairs, pair)
	if a.afterCheck != nil {
		a.afterCheck(len(a.pairs), pair)
	}
	if a.deny || pair.Action == a.denyAction {
		return ErrForbidden
	}
	return nil
}

type validationTestProbe struct {
	target        ValidationTarget
	probe         func(context.Context, ValidationTarget, string, map[string]string) error
	probeCalls    int
	resolveCalls  int
	resolveTarget func(int, ValidationTarget) ValidationTarget
	afterResolve  func(int)
}

func (p *validationTestProbe) ResolveValidationTarget(context.Context, Resource, Scope) (ValidationTarget, error) {
	p.resolveCalls++
	if p.afterResolve != nil {
		p.afterResolve(p.resolveCalls)
	}
	if p.resolveTarget != nil {
		return p.resolveTarget(p.resolveCalls, p.target), nil
	}
	return p.target, nil
}

func (p *validationTestProbe) ProbeCredential(ctx context.Context, target ValidationTarget, versionID string, fields map[string]string) error {
	p.probeCalls++
	if p.probe != nil {
		return p.probe(ctx, target, versionID, fields)
	}
	return nil
}
