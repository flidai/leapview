package postgres

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/jackc/pgx/v5"
)

type initialSetupFixture struct {
	repo    *Repository
	db      auditDatabase
	initial access.InitialInstanceCredentials
	claim   access.APICredential
	input   access.ProjectClaimPublisherExchangeInput
}

func newInitialSetupFixture(t *testing.T) initialSetupFixture {
	t.Helper()
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte(initializationFingerprintKey)})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := repo.InitializeInstance(t.Context(), access.InstanceInitializationInput{InstanceID: "instance_setup", Email: "setup@example.test", Environment: "prod"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := repo.CredentialForAPIToken(t.Context(), initial.ProjectClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	return initialSetupFixture{repo: repo, db: db, initial: initial, claim: claim, input: access.ProjectClaimPublisherExchangeInput{InstanceID: "instance_setup", ProjectID: "project:setup", PrincipalID: claim.Principal.ID, ClaimCredentialID: claim.Token.ID, ClaimedProjectID: "project:setup", ClaimedBy: claim.Principal.ID}}
}
func (f initialSetupFixture) exchange(t *testing.T) access.ProjectClaimPublisherCredentials {
	t.Helper()
	var result access.ProjectClaimPublisherCredentials
	err := f.repo.RunAuditedMutationBatch(t.Context(), func(tx access.Repository) ([]access.AuditEventInput, error) {
		var err error
		result, err = tx.(access.ProjectClaimPublisherRepository).ExchangeProjectClaimPublisher(t.Context(), f.input)
		return []access.AuditEventInput{{PrincipalID: f.claim.Principal.ID, Action: "project.claim.publisher.exchanged", ResourceKind: "api_token", ResourceID: result.PublisherCredentialID, Status: "success"}}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func (f initialSetupFixture) assertOpen(t *testing.T, tokenID string, want bool) {
	t.Helper()
	open, err := f.repo.InitialPublisherPasswordSetupOpen(t.Context(), f.claim.Principal.ID, tokenID)
	if err != nil || open != want {
		t.Fatalf("setup open=%v err=%v want=%v", open, err, want)
	}
}

func TestInitialPasswordSetupClosesPermanentlyAndPreservesOrigin(t *testing.T) {
	f := newInitialSetupFixture(t)
	p := f.exchange(t)
	f.assertOpen(t, p.PublisherCredentialID, true)
	// Invalid password changes cannot close setup.
	if _, err := f.repo.ChangeLocalPassword(t.Context(), f.claim.Principal.ID, "wrong", "replacement-password-123"); err == nil {
		t.Fatal("accepted wrong current password")
	}
	f.assertOpen(t, p.PublisherCredentialID, true)
	// Even a successful mutation is rolled back with its enclosing audit batch.
	abort := errors.New("rollback password reset")
	err := f.repo.RunAuditedMutationBatch(t.Context(), func(tx access.Repository) ([]access.AuditEventInput, error) {
		if _, err := tx.ResetLocalPassword(t.Context(), f.claim.Principal.ID); err != nil {
			return nil, err
		}
		return nil, abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	f.assertOpen(t, p.PublisherCredentialID, true)
	if _, err := f.repo.ChangeLocalPassword(t.Context(), f.claim.Principal.ID, f.initial.TemporaryPassword, "replacement-password-123"); err != nil {
		t.Fatal(err)
	}
	f.assertOpen(t, p.PublisherCredentialID, false)
	// Exchanging a still-live claim cannot reopen the setup window.
	p = f.exchange(t)
	f.assertOpen(t, p.PublisherCredentialID, false)
	if _, err := f.repo.ResetLocalPassword(t.Context(), f.claim.Principal.ID); err != nil {
		t.Fatal(err)
	}
	p = f.exchange(t)
	f.assertOpen(t, p.PublisherCredentialID, false)
	credential, err := f.repo.CredentialForAPIToken(t.Context(), p.PublisherToken)
	if err != nil || credential.InitialPublisher == nil {
		t.Fatalf("lost immutable origin: %v", err)
	}
	// Database state cannot be reopened, even by the owner.
	if _, err := f.db.admin.Exec(t.Context(), `UPDATE access.initial_password_setup SET closed_at=NULL`); err == nil {
		t.Fatal("reopened closed setup")
	}
	if _, err := f.db.runtime.Exec(t.Context(), `DELETE FROM access.initial_password_setup`); err == nil {
		t.Fatal("runtime deleted setup")
	}
}

func TestInitialPasswordResetBeforeLoginAndConcurrentExchange(t *testing.T) {
	f := newInitialSetupFixture(t)
	p := f.exchange(t)
	var wg sync.WaitGroup
	wg.Add(2)
	var resetErr, exchangeErr error
	var replacement access.ProjectClaimPublisherCredentials
	go func() { defer wg.Done(); _, resetErr = f.repo.ResetLocalPassword(t.Context(), f.claim.Principal.ID) }()
	go func() {
		defer wg.Done()
		exchangeErr = f.repo.RunAuditedMutationBatch(t.Context(), func(tx access.Repository) ([]access.AuditEventInput, error) {
			var err error
			replacement, err = tx.(access.ProjectClaimPublisherRepository).ExchangeProjectClaimPublisher(t.Context(), f.input)
			return []access.AuditEventInput{{PrincipalID: f.claim.Principal.ID, Action: "project.claim.publisher.exchanged", ResourceKind: "api_token", ResourceID: replacement.PublisherCredentialID, Status: "success"}}, err
		})
	}()
	wg.Wait()
	if resetErr != nil || exchangeErr != nil {
		t.Fatalf("reset=%v exchange=%v", resetErr, exchangeErr)
	}
	f.assertOpen(t, replacement.PublisherCredentialID, false)
	if _, err := f.repo.CredentialForAPIToken(t.Context(), p.PublisherToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("superseded publisher: %v", err)
	}
}

func TestInitialPublisherCannotBeForgedEditedOrRotatedIntoExemption(t *testing.T) {
	f := newInitialSetupFixture(t)
	p := f.exchange(t)
	credential, err := f.repo.CredentialForAPIToken(t.Context(), p.PublisherToken)
	if err != nil {
		t.Fatal(err)
	}
	modified, err := time.Parse(time.RFC3339Nano, credential.Token.ModifiedAt)
	if err != nil {
		t.Fatal(err)
	}
	expiry, err := time.Parse(time.RFC3339Nano, credential.Token.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	spoof, metadata, err := f.repo.CreateScopedAPITokenWithMetadata(t.Context(), access.ScopedAPITokenInput{PrincipalID: f.claim.Principal.ID, Name: credential.Token.Name, Permissions: credential.Token.Permissions, ExpiresAt: expiry})
	if err != nil {
		t.Fatal(err)
	}
	f.assertOpen(t, metadata.ID, false)
	c, err := f.repo.CredentialForAPIToken(t.Context(), spoof)
	if err != nil || c.InitialPublisher != nil {
		t.Fatalf("spoof acquired provenance: %v", err)
	}
	_, err = f.repo.UpdateScopedAPITokenForPrincipal(t.Context(), access.ScopedAPITokenUpdate{PrincipalID: f.claim.Principal.ID, TokenID: credential.Token.ID, Name: "edited", Permissions: credential.Token.Permissions, ExpiresAt: expiry, ExpectedModifiedAt: modified})
	if !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("publisher edit error=%v", err)
	}
	secret, rotated, err := f.repo.RotateScopedAPITokenForPrincipal(t.Context(), access.ScopedAPITokenRotation{PrincipalID: f.claim.Principal.ID, TokenID: credential.Token.ID, ExpectedModifiedAt: modified})
	if err != nil {
		t.Fatal(err)
	}
	f.assertOpen(t, rotated.ID, false)
	c, err = f.repo.CredentialForAPIToken(t.Context(), secret)
	if err != nil || c.InitialPublisher != nil {
		t.Fatalf("rotation copied provenance: %v", err)
	}
	if _, err := f.repo.InitialPublisherPasswordSetupOpen(t.Context(), f.claim.Principal.ID, p.PublisherCredentialID); err == nil {
		t.Fatal("revoked publisher retained exception")
	}
}

func TestInitialPublisherAckPreservesOpenSetup(t *testing.T) {
	f := newInitialSetupFixture(t)
	p := f.exchange(t)
	err := f.repo.RunAuditedMutationBatch(t.Context(), func(tx access.Repository) ([]access.AuditEventInput, error) {
		err := tx.(access.ProjectClaimPublisherRepository).AcknowledgeProjectClaimPublisher(t.Context(), access.ProjectClaimPublisherAcknowledgeInput{InstanceID: f.input.InstanceID, ProjectID: f.input.ProjectID, PrincipalID: f.input.PrincipalID, ClaimCredentialID: f.input.ClaimCredentialID, PublisherCredentialID: p.PublisherCredentialID, ClaimedProjectID: f.input.ProjectID, ClaimedBy: f.input.PrincipalID})
		return []access.AuditEventInput{{PrincipalID: f.claim.Principal.ID, Action: "project.claim.publisher.acknowledged", ResourceKind: "api_token", ResourceID: p.PublisherCredentialID, Status: "success"}}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.CredentialForAPIToken(t.Context(), f.initial.ProjectClaimToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("claim after ACK: %v", err)
	}
	f.assertOpen(t, p.PublisherCredentialID, true)
	c, err := f.repo.CredentialForAPIToken(t.Context(), p.PublisherToken)
	if err != nil || c.InitialPublisher == nil {
		t.Fatalf("publisher after ACK: %v", err)
	}
}

func TestInitialPublisherOriginRejectsMismatchedScopeAndPrincipal(t *testing.T) {
	f := newInitialSetupFixture(t)
	p := f.exchange(t)
	valid, err := f.repo.CredentialForAPIToken(t.Context(), p.PublisherToken)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "other@example.test", Password: "other-password-123", MustChange: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, principal, project string
		permissions              []access.PermissionPair
	}{
		{"wrong project", f.claim.Principal.ID, "project:other", valid.Token.Permissions},
		{"wrong principal", other.Principal.ID, f.input.ProjectID, valid.Token.Permissions},
		{"wrong permissions", f.claim.Principal.ID, f.input.ProjectID, []access.PermissionPair{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret, token, err := f.repo.CreateScopedAPITokenWithMetadata(t.Context(), access.ScopedAPITokenInput{PrincipalID: tc.principal, Name: valid.Token.Name, Permissions: tc.permissions, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			// Owner-level fixture corruption must still not manufacture an exemption.
			if _, err := f.db.admin.Exec(t.Context(), `INSERT INTO access.initial_publisher_origin(publisher_credential_id,claim_credential_id,project_id) VALUES($1::uuid,$2::uuid,$3)`, token.ID, f.claim.Token.ID, tc.project); err != nil {
				t.Fatal(err)
			}
			credential, err := f.repo.CredentialForAPIToken(t.Context(), secret)
			if err == nil && credential.InitialPublisher != nil {
				t.Fatal("inconsistent linkage produced verified provenance")
			}
			open, err := f.repo.InitialPublisherPasswordSetupOpen(t.Context(), tc.principal, token.ID)
			if err == nil && open {
				t.Fatal("inconsistent linkage acquired exception")
			}
		})
	}
	if _, err := f.db.admin.Exec(t.Context(), `UPDATE access.api_token SET expires_at=created_at+interval '1 microsecond' WHERE id=$1::uuid`, p.PublisherCredentialID); err != nil {
		t.Fatal(err)
	}
	if open, err := f.repo.InitialPublisherPasswordSetupOpen(t.Context(), f.claim.Principal.ID, p.PublisherCredentialID); err == nil || open {
		t.Fatal("expired publisher retained exception")
	}
}

func TestInitialPublisherExchangeRollbackPreservesPredecessor(t *testing.T) {
	f := newInitialSetupFixture(t)
	previous := f.exchange(t)
	var candidate access.ProjectClaimPublisherCredentials
	abort := errors.New("audit rollback")
	err := f.repo.RunAuditedMutationBatch(t.Context(), func(tx access.Repository) ([]access.AuditEventInput, error) {
		var err error
		candidate, err = tx.(access.ProjectClaimPublisherRepository).ExchangeProjectClaimPublisher(t.Context(), f.input)
		if err != nil {
			return nil, err
		}
		return nil, abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	f.assertOpen(t, previous.PublisherCredentialID, true)
	if _, err := f.repo.CredentialForAPIToken(t.Context(), candidate.PublisherToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("rolled-back publisher exists: %v", err)
	}
	var count int
	if err := f.db.admin.QueryRow(t.Context(), `SELECT count(*) FROM access.initial_publisher_origin`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("orphan provenance count=%d err=%v", count, err)
	}
}

func TestInitialPublisherSurvivesClaimRetentionWithoutBlockingTokenCleanup(t *testing.T) {
	db := newAuditRetentionDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte(initializationFingerprintKey)})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := repo.InitializeInstance(t.Context(), access.InstanceInitializationInput{InstanceID: "instance_setup", Email: "retention-setup@example.test", Environment: "prod"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := repo.CredentialForAPIToken(t.Context(), initial.ProjectClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	f := initialSetupFixture{repo: repo, initial: initial, claim: claim, input: access.ProjectClaimPublisherExchangeInput{InstanceID: "instance_setup", ProjectID: "project:setup", PrincipalID: claim.Principal.ID, ClaimCredentialID: claim.Token.ID, ClaimedProjectID: "project:setup", ClaimedBy: claim.Principal.ID}}
	publisher := f.exchange(t)
	if err := repo.RevokeAPIToken(t.Context(), claim.Token.ID); err != nil {
		t.Fatal(err)
	}
	maintenance := NewMaintenance(db.maintenance)
	if _, err := maintenance.PruneAuthState(t.Context(), time.Now(), 1000); err != nil {
		t.Fatal(err)
	}
	var claims int
	if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM access.api_token WHERE id=$1::uuid`, claim.Token.ID).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("spent claim not pruned: count=%d err=%v", claims, err)
	}
	f.assertOpen(t, publisher.PublisherCredentialID, true)
	// Subsequent password reset still permanently closes the retained setup.
	if _, err := repo.ResetLocalPassword(t.Context(), claim.Principal.ID); err != nil {
		t.Fatal(err)
	}
	f.assertOpen(t, publisher.PublisherCredentialID, false)
	if err := repo.RevokeAPIToken(t.Context(), publisher.PublisherCredentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.PruneAuthState(t.Context(), time.Now(), 1000); err != nil {
		t.Fatal(err)
	}
	var origins, setups int
	if err := db.admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM access.initial_publisher_origin),(SELECT count(*) FROM access.initial_password_setup WHERE closed_at IS NOT NULL)`).Scan(&origins, &setups); err != nil || origins != 0 || setups != 1 {
		t.Fatalf("retention origins=%d closed setups=%d err=%v", origins, setups, err)
	}
}
