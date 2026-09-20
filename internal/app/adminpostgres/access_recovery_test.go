package adminpostgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

type testPlatformAdminRecoveryAuthority struct {
	principal access.Principal
	state     access.PlatformAdministratorState
	tx        *testPlatformAdminRecoveryTransaction
	audit     access.AuditEventInput
	auditErr  error
}

func (a *testPlatformAdminRecoveryAuthority) PrincipalByID(context.Context, string) (access.Principal, error) {
	return a.principal, nil
}

func (a *testPlatformAdminRecoveryAuthority) ListPlatformAdministrators(context.Context) (access.PlatformAdministratorState, error) {
	return a.state, nil
}

func (a *testPlatformAdminRecoveryAuthority) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	if a.tx == nil {
		return a.auditErr
	}
	audit, err := mutation(a.tx)
	a.audit = audit
	if err != nil {
		return err
	}
	return a.auditErr
}

type testPlatformAdminRecoveryTransaction struct {
	access.Repository
	state            access.PlatformAdministratorState
	input            access.PlatformAdminGrantInput
	grant            access.PlatformAdminGrantResult
	recoveryPassword string
	resetCalls       int
	reset            access.LocalPasswordReset
	resetErr         error
}

func (tx *testPlatformAdminRecoveryTransaction) ListPlatformAdministrators(context.Context) (access.PlatformAdministratorState, error) {
	return tx.state, nil
}

func (tx *testPlatformAdminRecoveryTransaction) GrantPlatformAdmin(_ context.Context, input access.PlatformAdminGrantInput) (access.PlatformAdminGrantResult, error) {
	tx.input = input
	return tx.grant, nil
}

func (*testPlatformAdminRecoveryTransaction) RevokePlatformAdmin(context.Context, access.PlatformAdminRevokeInput) (access.PlatformAdministratorState, error) {
	panic("unexpected platform administrator revocation")
}

func (tx *testPlatformAdminRecoveryTransaction) RecoverLocalPassword(_ context.Context, _ string, password string) (access.LocalPasswordReset, error) {
	tx.resetCalls++
	tx.recoveryPassword = password
	return tx.reset, tx.resetErr
}

func TestPlatformAdministratorRecoveryPreviewIsReadOnlyAndRedacted(t *testing.T) {
	const principalID = "0198f2c0-7c7a-7f00-8a11-000000000001"
	authority := &testPlatformAdminRecoveryAuthority{
		principal: access.Principal{ID: principalID, Kind: access.PrincipalKindUser, Email: "operator@example.com"},
		state:     access.PlatformAdministratorState{Revision: "sha256:current"},
	}
	opened := &testMaintenancePool{}
	ops := New(Dependencies{
		LoadConfig:        func() (config.Config, error) { return productionAdminConfig(t.TempDir()), nil },
		OpenAccess:        func(context.Context, platformpostgres.Config) (AccessPool, error) { return opened, nil },
		VerifyBaseline:    func(context.Context, postgresbaseline.SQLDBProvider) error { return nil },
		NewRecoveryAccess: func(AccessPool, []byte) (PlatformAdminRecoveryAuthority, error) { return authority, nil },
	})
	var out bytes.Buffer
	err := ops.RecoverPlatformAdministrator(t.Context(), admincli.PlatformAdminRecoveryRequest{
		PrincipalID: principalID, ExpectedEmail: "OPERATOR@example.com",
		OperationID: "0198f2c0-7c7a-7f00-8a11-000000000002", AcknowledgeOfflineRecovery: true,
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	var evidence platformAdminRecoveryEvidence
	if err := json.Unmarshal(out.Bytes(), &evidence); err != nil {
		t.Fatalf("decode evidence %q: %v", out.String(), err)
	}
	if evidence.Mode != "preview" || evidence.PrincipalID != principalID || evidence.Email != "operator@example.com" || evidence.PreviousRevision != "sha256:current" || evidence.BindingID != "" {
		t.Fatalf("evidence = %#v", evidence)
	}
	if !opened.closed {
		t.Fatal("access pool was not closed")
	}
}

func TestPlatformAdministratorRecoveryApplyRequiresPreviewRevisionBeforeOpeningPool(t *testing.T) {
	loaded := false
	ops := New(Dependencies{LoadConfig: func() (config.Config, error) {
		loaded = true
		return productionAdminConfig(t.TempDir()), nil
	}})
	err := ops.RecoverPlatformAdministrator(t.Context(), admincli.PlatformAdminRecoveryRequest{
		PrincipalID: principalRecoveryTestID(1), ExpectedEmail: "operator@example.com",
		OperationID: principalRecoveryTestID(2), AcknowledgeOfflineRecovery: true, Apply: true,
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "expected revision") {
		t.Fatalf("missing expected revision error = %v", err)
	}
	if loaded {
		t.Fatal("missing expected revision loaded production configuration")
	}
}

func TestPlatformAdministratorRecoveryPreviewRejectsStaleRevision(t *testing.T) {
	const principalID = "0198f2c0-7c7a-7f00-8a11-000000000001"
	authority := &testPlatformAdminRecoveryAuthority{
		principal: access.Principal{ID: principalID, Kind: access.PrincipalKindUser, Email: "operator@example.com"},
		state:     access.PlatformAdministratorState{Revision: "sha256:current"},
	}
	ops := New(Dependencies{
		LoadConfig:        func() (config.Config, error) { return productionAdminConfig(t.TempDir()), nil },
		OpenAccess:        func(context.Context, platformpostgres.Config) (AccessPool, error) { return &testMaintenancePool{}, nil },
		VerifyBaseline:    func(context.Context, postgresbaseline.SQLDBProvider) error { return nil },
		NewRecoveryAccess: func(AccessPool, []byte) (PlatformAdminRecoveryAuthority, error) { return authority, nil },
	})
	err := ops.RecoverPlatformAdministrator(t.Context(), admincli.PlatformAdminRecoveryRequest{
		PrincipalID: principalID, ExpectedEmail: "operator@example.com",
		OperationID: principalRecoveryTestID(2), ExpectedRevision: "sha256:stale", AcknowledgeOfflineRecovery: true,
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "expected revision is stale") {
		t.Fatalf("stale preview error = %v", err)
	}
}

func TestPlatformAdministratorRecoveryRejectsIdentityMismatchBeforeMutation(t *testing.T) {
	const principalID = "0198f2c0-7c7a-7f00-8a11-000000000001"
	authority := &testPlatformAdminRecoveryAuthority{principal: access.Principal{
		ID: principalID, Kind: access.PrincipalKindUser, Email: "actual@example.com",
	}}
	ops := New(Dependencies{
		LoadConfig:        func() (config.Config, error) { return productionAdminConfig(t.TempDir()), nil },
		OpenAccess:        func(context.Context, platformpostgres.Config) (AccessPool, error) { return &testMaintenancePool{}, nil },
		VerifyBaseline:    func(context.Context, postgresbaseline.SQLDBProvider) error { return nil },
		NewRecoveryAccess: func(AccessPool, []byte) (PlatformAdminRecoveryAuthority, error) { return authority, nil },
	})
	err := ops.RecoverPlatformAdministrator(t.Context(), admincli.PlatformAdminRecoveryRequest{
		PrincipalID: principalID, ExpectedEmail: "wrong@example.com",
		OperationID: "0198f2c0-7c7a-7f00-8a11-000000000002", AcknowledgeOfflineRecovery: true, Apply: true,
		ExpectedRevision: "sha256:before",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlatformAdministratorRecoveryApplyUsesCASIdempotencyAndAtomicAudit(t *testing.T) {
	const principalID = "0198f2c0-7c7a-7f00-8a11-000000000001"
	const operationID = "0198f2c0-7c7a-7f00-8a11-000000000002"
	principal := access.Principal{ID: principalID, Kind: access.PrincipalKindUser, Email: "operator@example.com"}
	before := access.PlatformAdministratorState{Revision: "sha256:before"}
	after := access.PlatformAdministratorState{Revision: "sha256:after", Administrators: []access.PlatformAdministrator{{
		BindingID: "0198f2c0-7c7a-7f00-8a11-000000000003", Principal: principal, Role: access.PlatformRoleAdmin,
	}}}
	tx := &testPlatformAdminRecoveryTransaction{state: before, grant: access.PlatformAdminGrantResult{Administrator: after.Administrators[0], State: after}}
	authority := &testPlatformAdminRecoveryAuthority{principal: principal, state: before, tx: tx}
	ops := New(Dependencies{
		LoadConfig:        func() (config.Config, error) { return productionAdminConfig(t.TempDir()), nil },
		OpenAccess:        func(context.Context, platformpostgres.Config) (AccessPool, error) { return &testMaintenancePool{}, nil },
		VerifyBaseline:    func(context.Context, postgresbaseline.SQLDBProvider) error { return nil },
		NewRecoveryAccess: func(AccessPool, []byte) (PlatformAdminRecoveryAuthority, error) { return authority, nil },
	})
	var out bytes.Buffer
	err := ops.RecoverPlatformAdministrator(t.Context(), admincli.PlatformAdminRecoveryRequest{
		PrincipalID: strings.ToUpper(principalID), ExpectedEmail: strings.ToUpper(principal.Email), OperationID: strings.ToUpper(operationID),
		ExpectedRevision:           before.Revision,
		AcknowledgeOfflineRecovery: true, Apply: true,
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if tx.input.PrincipalID != principalID || tx.input.ExpectedRevision != before.Revision || tx.input.IdempotencyKey != "offline-platform-admin-recovery:"+operationID {
		t.Fatalf("grant input = %#v", tx.input)
	}
	if authority.audit.PrincipalID != principalID || authority.audit.Action != "platform_admin.recovered" || authority.audit.ResourceID != after.Administrators[0].BindingID || !strings.Contains(authority.audit.MetadataJSON, operationID) {
		t.Fatalf("audit = %#v", authority.audit)
	}
	var evidence platformAdminRecoveryEvidence
	if err := json.Unmarshal(out.Bytes(), &evidence); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if evidence.Mode != "apply" || evidence.ResultRevision != after.Revision || evidence.BindingID != after.Administrators[0].BindingID {
		t.Fatalf("evidence = %#v", evidence)
	}
}

func TestPlatformAdministratorRecoveryCanResetLocalCredentialWithoutLeakingIt(t *testing.T) {
	const principalID = "0198f2c0-7c7a-7f00-8a11-000000000001"
	const replacement = "correct horse battery staple for recovery"
	passwordFile := t.TempDir() + "/recovery-password"
	if err := os.WriteFile(passwordFile, []byte(replacement+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	principal := access.Principal{ID: principalID, Kind: access.PrincipalKindUser, Email: "operator@example.com"}
	before := access.PlatformAdministratorState{Revision: "sha256:before"}
	after := access.PlatformAdministratorState{Revision: "sha256:after", Administrators: []access.PlatformAdministrator{{
		BindingID: principalRecoveryTestID(3), Principal: principal, Role: access.PlatformRoleAdmin,
	}}}
	tx := &testPlatformAdminRecoveryTransaction{
		state: before,
		grant: access.PlatformAdminGrantResult{Administrator: after.Administrators[0], State: after},
		reset: access.LocalPasswordReset{Principal: principal},
	}
	authority := &testPlatformAdminRecoveryAuthority{principal: principal, state: before, tx: tx}
	cfg := productionAdminConfig(t.TempDir())
	cfg.LocalAuth = true
	ops := New(Dependencies{
		LoadConfig:        func() (config.Config, error) { return cfg, nil },
		OpenAccess:        func(context.Context, platformpostgres.Config) (AccessPool, error) { return &testMaintenancePool{}, nil },
		VerifyBaseline:    func(context.Context, postgresbaseline.SQLDBProvider) error { return nil },
		NewRecoveryAccess: func(AccessPool, []byte) (PlatformAdminRecoveryAuthority, error) { return authority, nil },
	})
	var out bytes.Buffer
	err := ops.RecoverPlatformAdministrator(t.Context(), admincli.PlatformAdminRecoveryRequest{
		PrincipalID: principalID, ExpectedEmail: principal.Email, OperationID: principalRecoveryTestID(2),
		ExpectedRevision: before.Revision, LocalPasswordFile: passwordFile,
		AcknowledgeOfflineRecovery: true, AcknowledgeCredentialReset: true, Apply: true,
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if tx.recoveryPassword != replacement {
		t.Fatal("replacement password was not applied")
	}
	if strings.Contains(out.String(), replacement) || strings.Contains(authority.audit.MetadataJSON, replacement) {
		t.Fatal("recovery output or audit leaked the replacement password")
	}
	var evidence platformAdminRecoveryEvidence
	if err := json.Unmarshal(out.Bytes(), &evidence); err != nil {
		t.Fatal(err)
	}
	if !evidence.LocalPasswordReset {
		t.Fatalf("evidence = %#v", evidence)
	}
	// A transport retry must replay the role result without replacing a
	// password changed after the first recovery or revoking its new session.
	tx.grant.Replayed = true
	tx.recoveryPassword = ""
	out.Reset()
	if err := ops.RecoverPlatformAdministrator(t.Context(), admincli.PlatformAdminRecoveryRequest{
		PrincipalID: principalID, ExpectedEmail: principal.Email, OperationID: principalRecoveryTestID(2),
		ExpectedRevision: before.Revision, LocalPasswordFile: passwordFile,
		AcknowledgeOfflineRecovery: true, AcknowledgeCredentialReset: true, Apply: true,
	}, &out); err != nil {
		t.Fatal(err)
	}
	if tx.resetCalls != 1 || tx.recoveryPassword != "" {
		t.Fatalf("replay replaced password: calls=%d password=%q", tx.resetCalls, tx.recoveryPassword)
	}
	if err := json.Unmarshal(out.Bytes(), &evidence); err != nil {
		t.Fatal(err)
	}
	if !evidence.Replayed || evidence.LocalPasswordReset || authority.audit.Action != "platform_admin.recovered" {
		t.Fatalf("replay evidence = %#v, audit = %#v", evidence, authority.audit)
	}
}

func TestPlatformAdminRecoveryIntentBinding(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	original, err := platformAdminRecoveryRequestBinding(key, "principal", "operator@example.test", "first password")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := platformAdminRecoveryRequestBinding(key, "principal", "operator@example.test", "first password")
	if err != nil || retry != original {
		t.Fatalf("stable retry binding = %q, error = %v", retry, err)
	}
	changed, err := platformAdminRecoveryRequestBinding(key, "principal", "operator@example.test", "second password")
	if err != nil || changed == original || strings.Contains(changed, "password") {
		t.Fatalf("changed password did not change opaque binding: %q, error = %v", changed, err)
	}
}

func TestPlatformAdministratorRecoveryRejectsInsecurePasswordFileBeforeOpeningPool(t *testing.T) {
	passwordFile := t.TempDir() + "/recovery-password"
	if err := os.WriteFile(passwordFile, []byte("correct horse battery staple for recovery\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opened := false
	cfg := productionAdminConfig(t.TempDir())
	cfg.LocalAuth = true
	ops := New(Dependencies{
		LoadConfig: func() (config.Config, error) { return cfg, nil },
		OpenAccess: func(context.Context, platformpostgres.Config) (AccessPool, error) {
			opened = true
			return &testMaintenancePool{}, nil
		},
	})
	err := ops.RecoverPlatformAdministrator(t.Context(), admincli.PlatformAdminRecoveryRequest{
		PrincipalID: principalRecoveryTestID(1), ExpectedEmail: "operator@example.com", OperationID: principalRecoveryTestID(2),
		ExpectedRevision: "sha256:before", LocalPasswordFile: passwordFile,
		AcknowledgeOfflineRecovery: true, AcknowledgeCredentialReset: true, Apply: true,
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "group or other") {
		t.Fatalf("error = %v", err)
	}
	if opened {
		t.Fatal("insecure password file opened PostgreSQL")
	}
}

func TestPlatformAdministratorRecoveryRejectsUnexpectedPrincipalIdentity(t *testing.T) {
	authority := &testPlatformAdminRecoveryAuthority{principal: access.Principal{
		ID: principalRecoveryTestID(9), Kind: access.PrincipalKindUser, Email: "operator@example.com",
	}, state: access.PlatformAdministratorState{Revision: "sha256:current"}}
	ops := New(Dependencies{
		LoadConfig:        func() (config.Config, error) { return productionAdminConfig(t.TempDir()), nil },
		OpenAccess:        func(context.Context, platformpostgres.Config) (AccessPool, error) { return &testMaintenancePool{}, nil },
		VerifyBaseline:    func(context.Context, postgresbaseline.SQLDBProvider) error { return nil },
		NewRecoveryAccess: func(AccessPool, []byte) (PlatformAdminRecoveryAuthority, error) { return authority, nil },
	})
	err := ops.RecoverPlatformAdministrator(t.Context(), admincli.PlatformAdminRecoveryRequest{
		PrincipalID: principalRecoveryTestID(1), ExpectedEmail: "operator@example.com",
		OperationID: principalRecoveryTestID(2), AcknowledgeOfflineRecovery: true,
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "unexpected principal") {
		t.Fatalf("unexpected principal error = %v", err)
	}
}

func TestPlatformAdministratorRecoveryIsProductionOnly(t *testing.T) {
	opened := false
	ops := New(Dependencies{
		LoadConfig: func() (config.Config, error) { return config.Config{}, nil },
		OpenAccess: func(context.Context, platformpostgres.Config) (AccessPool, error) {
			opened = true
			return &testMaintenancePool{}, nil
		},
	})
	err := ops.RecoverPlatformAdministrator(t.Context(), admincli.PlatformAdminRecoveryRequest{
		PrincipalID: "0198f2c0-7c7a-7f00-8a11-000000000001", ExpectedEmail: "operator@example.com",
		OperationID: "0198f2c0-7c7a-7f00-8a11-000000000002", AcknowledgeOfflineRecovery: true,
	}, &bytes.Buffer{})
	if !strings.Contains(err.Error(), ErrNativeAdminUnavailable.Error()) {
		t.Fatalf("error = %v", err)
	}
	if opened {
		t.Fatal("non-production recovery opened a PostgreSQL access pool")
	}
}

func principalRecoveryTestID(value byte) string {
	return fmt.Sprintf("0198f2c0-7c7a-7f00-8a11-%012d", value)
}
