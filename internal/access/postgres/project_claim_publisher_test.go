package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

func TestProjectClaimPublisherExchangeRotatesUntilAcknowledged(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte(initializationFingerprintKey)})
	if err != nil {
		t.Fatal(err)
	}
	instanceID := "instance_claim_exchange"
	projectID := projectgraph.ResourceID("project:bootstrap")
	initialized, err := repo.InitializeInstance(t.Context(), access.InstanceInitializationInput{
		InstanceID: instanceID, Email: "claim-exchange@example.test", Environment: "production", Now: time.Now().UTC(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimCredential, err := repo.CredentialForAPIToken(t.Context(), initialized.ProjectClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	input := access.ProjectClaimPublisherExchangeInput{
		InstanceID: instanceID, ProjectID: projectID.String(), PrincipalID: claimCredential.Principal.ID,
		ClaimCredentialID: claimCredential.Token.ID, ClaimedProjectID: projectID.String(), ClaimedBy: claimCredential.Principal.ID,
	}
	exchange := func() access.ProjectClaimPublisherCredentials {
		t.Helper()
		var result access.ProjectClaimPublisherCredentials
		err := repo.RunAuditedMutationBatch(t.Context(), func(txRepo access.Repository) ([]access.AuditEventInput, error) {
			issuer, ok := txRepo.(access.ProjectClaimPublisherRepository)
			if !ok {
				return nil, errors.New("transaction lost project claim publisher capability")
			}
			var exchangeErr error
			result, exchangeErr = issuer.ExchangeProjectClaimPublisher(t.Context(), input)
			if exchangeErr != nil {
				return nil, exchangeErr
			}
			return []access.AuditEventInput{{
				PrincipalID: input.PrincipalID, Action: "project.claim.publisher.exchanged",
				ResourceKind: "api_token", ResourceID: result.PublisherCredentialID, Status: "success",
				MetadataJSON: `{"claimCredentialId":"` + input.ClaimCredentialID + `"}`,
			}}, nil
		})
		if err != nil {
			t.Fatalf("exchange project claim publisher: %v", err)
		}
		return result
	}
	first := exchange()
	if first.PublisherToken == "" || first.PublisherCredentialID == "" || !first.PublisherTokenExpiresAt.After(time.Now()) {
		t.Fatalf("first exchange omitted publisher evidence: %#v", first)
	}
	if len(first.RevokedPublisherCredentialIDs) != 0 {
		t.Fatalf("first exchange unexpectedly revoked credentials: %#v", first.RevokedPublisherCredentialIDs)
	}
	if _, err := repo.CredentialForAPIToken(t.Context(), initialized.ProjectClaimToken); err != nil {
		t.Fatalf("claim credential was revoked before ACK: %v", err)
	}

	second := exchange()
	if second.PublisherToken == first.PublisherToken || second.PublisherCredentialID == first.PublisherCredentialID {
		t.Fatal("claim retry did not rotate publisher secret and identity")
	}
	if len(second.RevokedPublisherCredentialIDs) != 1 || second.RevokedPublisherCredentialIDs[0] != first.PublisherCredentialID {
		t.Fatalf("retry did not report exactly the previous publisher: %#v", second.RevokedPublisherCredentialIDs)
	}
	if _, err := repo.CredentialForAPIToken(t.Context(), first.PublisherToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("previous publisher remains active: %v", err)
	}
	current, err := repo.CredentialForAPIToken(t.Context(), second.PublisherToken)
	if err != nil {
		t.Fatalf("resolve current publisher: %v", err)
	}
	want, err := access.InitialProjectPublisherPermissions(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Token.ID != second.PublisherCredentialID || current.Token.PermissionProfile != access.PermissionCatalogProfile || len(current.Token.Capabilities) != 0 || !exactPermissionSet(current.Token.Permissions, want) {
		t.Fatalf("publisher scope = %#v, want exact typed role expansion %#v", current.Token, want)
	}
	for _, pair := range current.Token.Permissions {
		if pair.Action == access.ActionConnectionManage {
			t.Fatal("initial publisher unexpectedly received connection.manage")
		}
	}

	ack := access.ProjectClaimPublisherAcknowledgeInput{
		InstanceID: instanceID, ProjectID: projectID.String(), PrincipalID: input.PrincipalID,
		ClaimCredentialID: input.ClaimCredentialID, PublisherCredentialID: second.PublisherCredentialID,
		ClaimedProjectID: projectID.String(), ClaimedBy: input.PrincipalID,
	}
	acknowledge := func(publisherID string) error {
		return repo.RunAuditedMutationBatch(t.Context(), func(txRepo access.Repository) ([]access.AuditEventInput, error) {
			issuer, ok := txRepo.(access.ProjectClaimPublisherRepository)
			if !ok {
				return nil, errors.New("transaction lost project claim publisher capability")
			}
			ack.PublisherCredentialID = publisherID
			if err := issuer.AcknowledgeProjectClaimPublisher(t.Context(), ack); err != nil {
				return nil, err
			}
			return []access.AuditEventInput{{
				PrincipalID: input.PrincipalID, Action: "project.claim.publisher.acknowledged",
				ResourceKind: "api_token", ResourceID: input.ClaimCredentialID, Status: "success",
				MetadataJSON: `{"publisherCredentialId":"` + publisherID + `"}`,
			}}, nil
		})
	}
	if err := acknowledge(first.PublisherCredentialID); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("ACK accepted stale publisher credential: %v", err)
	}
	if err := acknowledge(second.PublisherCredentialID); err != nil {
		t.Fatalf("ACK current publisher: %v", err)
	}
	if _, err := repo.CredentialForAPIToken(t.Context(), initialized.ProjectClaimToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ACK left claim credential active: %v", err)
	}
	if err := acknowledge(second.PublisherCredentialID); err != nil {
		t.Fatalf("ACK replay was not safe: %v", err)
	}
	if _, err := repo.CredentialForAPIToken(t.Context(), second.PublisherToken); err != nil {
		t.Fatalf("ACK revoked publisher credential: %v", err)
	}
}

func TestProjectClaimPublisherExchangeRejectsMismatchedClaimAndScope(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte(initializationFingerprintKey)})
	if err != nil {
		t.Fatal(err)
	}
	instanceID := "instance_claim_mismatch"
	initialized, err := repo.InitializeInstance(t.Context(), access.InstanceInitializationInput{
		InstanceID: instanceID, Email: "claim-mismatch@example.test", Environment: "production", Now: time.Now().UTC(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := repo.CredentialForAPIToken(t.Context(), initialized.ProjectClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	valid := access.ProjectClaimPublisherExchangeInput{
		InstanceID: instanceID, ProjectID: "project:expected", PrincipalID: claim.Principal.ID,
		ClaimCredentialID: claim.Token.ID, ClaimedProjectID: "project:expected", ClaimedBy: claim.Principal.ID,
	}
	for name, mutate := range map[string]func(*access.ProjectClaimPublisherExchangeInput){
		"different project":   func(input *access.ProjectClaimPublisherExchangeInput) { input.ClaimedProjectID = "project:other" },
		"different principal": func(input *access.ProjectClaimPublisherExchangeInput) { input.ClaimedBy = "other-principal" },
		"different instance":  func(input *access.ProjectClaimPublisherExchangeInput) { input.InstanceID = "instance:other" },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			if err := repo.RunAuditedMutationBatch(t.Context(), func(txRepo access.Repository) ([]access.AuditEventInput, error) {
				issuer := txRepo.(access.ProjectClaimPublisherRepository)
				_, err := issuer.ExchangeProjectClaimPublisher(t.Context(), input)
				return nil, err
			}); !errors.Is(err, access.ErrForbidden) {
				t.Fatalf("exchange error = %v, want forbidden", err)
			}
		})
	}
}

func TestProjectClaimPublisherExchangeRevokesMoreThanOneTokenPage(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte(initializationFingerprintKey)})
	if err != nil {
		t.Fatal(err)
	}
	instanceID := "instance_claim_rotation_many"
	initialized, err := repo.InitializeInstance(t.Context(), access.InstanceInitializationInput{
		InstanceID: instanceID, Email: "claim-rotation-many@example.test", Environment: "production", Now: time.Now().UTC(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := repo.CredentialForAPIToken(t.Context(), initialized.ProjectClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	projectID := projectgraph.ResourceID("project:rotation-many")
	tokenName := access.InitialProjectClaimPublisherTokenName(claim.Token.ID)
	const tokenCount = 1005
	if _, err := db.admin.Exec(t.Context(), `
		INSERT INTO access.api_token(id, principal_id, name, token_fingerprint, verifier, capabilities, permission_profile, permissions, expires_at)
		SELECT md5('claim-publisher-id-' || n::text)::uuid, $1::uuid, $2,
		       decode(md5('claim-publisher-fingerprint-a-' || n::text) || md5('claim-publisher-fingerprint-b-' || n::text), 'hex'),
		       decode(repeat('ab', 32), 'hex'), NULL, $3, '[]'::jsonb, clock_timestamp() + interval '90 days'
		FROM generate_series(1, $4::int) AS n`, claim.Principal.ID, tokenName, access.PermissionCatalogProfile, tokenCount); err != nil {
		t.Fatalf("seed more than one token-list page: %v", err)
	}
	input := access.ProjectClaimPublisherExchangeInput{
		InstanceID: instanceID, ProjectID: projectID.String(), PrincipalID: claim.Principal.ID,
		ClaimCredentialID: claim.Token.ID, ClaimedProjectID: projectID.String(), ClaimedBy: claim.Principal.ID,
	}
	var result access.ProjectClaimPublisherCredentials
	err = repo.RunAuditedMutationBatch(t.Context(), func(txRepo access.Repository) ([]access.AuditEventInput, error) {
		issuer := txRepo.(access.ProjectClaimPublisherRepository)
		var exchangeErr error
		result, exchangeErr = issuer.ExchangeProjectClaimPublisher(t.Context(), input)
		if exchangeErr != nil {
			return nil, exchangeErr
		}
		return []access.AuditEventInput{{PrincipalID: claim.Principal.ID, Action: "project.claim.publisher.exchanged", ResourceKind: "api_token", ResourceID: result.PublisherCredentialID, Status: "success"}}, nil
	})
	if err != nil {
		t.Fatalf("rotate publisher after many prior exchanges: %v", err)
	}
	if len(result.RevokedPublisherCredentialIDs) != tokenCount {
		t.Fatalf("revoked %d prior publisher tokens, want %d", len(result.RevokedPublisherCredentialIDs), tokenCount)
	}
	var active int
	if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM access.api_token WHERE principal_id=$1::uuid AND name=$2 AND revoked_at IS NULL`, claim.Principal.ID, tokenName).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active publisher tokens after rotation = %d, want only the current publisher", active)
	}
}
