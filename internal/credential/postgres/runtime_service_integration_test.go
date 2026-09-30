package postgres

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type runtimeIntegrationAuthority struct {
	references []credential.RuntimeCredentialReference
	identities []projectgraph.ServingIdentity
	resources  []credential.Resource
	calls      int
}

func (a *runtimeIntegrationAuthority) ResolveRuntimeCredential(
	_ context.Context,
	identity projectgraph.ServingIdentity,
	resource credential.Resource,
) (credential.RuntimeCredentialReference, error) {
	index := a.calls
	a.calls++
	a.identities = append(a.identities, identity)
	a.resources = append(a.resources, resource)
	if index >= len(a.references) {
		index = len(a.references) - 1
	}
	return a.references[index], nil
}

type runtimeIntegrationFixture struct {
	db       *pgxpool.Pool
	repo     *Repository
	keys     *encryption.Keyring
	resource credential.Resource
	scope    credential.Scope
	identity projectgraph.ServingIdentity
	versionA credential.Metadata
	versionB credential.Metadata
}

func newRuntimeIntegrationFixture(t *testing.T) runtimeIntegrationFixture {
	t.Helper()
	db, _, repository := credentialDB(t)
	keys := loadTestCredentialKeyring(t, "runtime-deployment", "runtime-key", bytes.Repeat([]byte{0x68}, 32))
	resource := credential.Resource{ScopeKind: "connection", TargetID: "target", ProjectID: "project", Environment: "production", ResourceID: "warehouse"}
	scope := credential.Scope{
		Resource: resource, OwnerID: "customer", Purpose: "connection-authentication",
		Provider: "postgres", Destination: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	drafts, err := credential.NewService(repository, keys, testCredentialScopeResolver{scope}, allowCredentialAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	actor := uuid.NewString()
	versionA, err := drafts.SaveDraft(t.Context(), actor, resource, map[string]string{"password": "runtime-password-A"})
	if err != nil {
		t.Fatal(err)
	}
	versionB, err := drafts.SaveDraft(t.Context(), actor, resource, map[string]string{"password": "runtime-password-B"})
	if err != nil {
		t.Fatal(err)
	}
	if versionA.Binding.VersionID == versionB.Binding.VersionID {
		t.Fatal("saved draft versions unexpectedly share an identity")
	}
	if !versionB.CreatedAt.After(versionA.CreatedAt) {
		t.Fatal("second saved draft is not newer than the first")
	}
	identity := projectgraph.ServingIdentity{
		ProjectID: projectgraph.ResourceID(resource.ProjectID), Environment: resource.Environment, GenerationID: "generation-runtime-test",
	}
	return runtimeIntegrationFixture{
		db: db, repo: repository, keys: keys, resource: resource, scope: scope,
		identity: identity, versionA: versionA, versionB: versionB,
	}
}

type runtimeDurableState struct {
	drafts, envelopes, receipts, audits int64
}

func runtimeState(t *testing.T, db *pgxpool.Pool) runtimeDurableState {
	t.Helper()
	var state runtimeDurableState
	if err := db.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM credential.draft_version),
			(SELECT count(*) FROM credential.envelope),
			(SELECT count(*) FROM credential.validation_receipt),
			(SELECT count(*) FROM audit.audit_event)
	`).Scan(&state.drafts, &state.envelopes, &state.receipts, &state.audits); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRuntimeResolverUsesAuthorityPinnedVersionAndIsReadOnly(t *testing.T) {
	fixture := newRuntimeIntegrationFixture(t)
	probe := &validationIntegrationProbe{target: credential.ValidationTarget{
		Scope: fixture.scope, BindingID: "runtime-binding", BindingRevision: 1,
		ConfigurationDigest: "sha256:" + strings.Repeat("b", 64),
	}}
	probe.probe = func(_ context.Context, _ credential.ValidationTarget, version string, fields map[string]string) error {
		if version != fixture.versionB.Binding.VersionID || fields["password"] != "runtime-password-B" {
			t.Fatal("probe did not receive the exact second saved version")
		}
		return nil
	}
	validation, err := credential.NewValidationService(
		fixture.repo, fixture.keys, testCredentialScopeResolver{fixture.scope},
		&validationIntegrationAuthorizer{}, probe, time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validation.ValidateDraft(t.Context(), fixture.versionB.ActorID, fixture.resource, fixture.versionB.Binding.VersionID, 1); err != nil {
		t.Fatalf("ValidateDraft() for second saved version failed: %v", err)
	}
	stateBefore := runtimeState(t, fixture.db)
	if stateBefore.receipts != 1 {
		t.Fatalf("validation receipts before runtime read = %d, want one", stateBefore.receipts)
	}
	authority := &runtimeIntegrationAuthority{references: []credential.RuntimeCredentialReference{{
		Scope: fixture.scope, VersionID: fixture.versionA.Binding.VersionID,
	}}}
	resolver, err := credential.NewRuntimeResolver(fixture.repo, fixture.keys, authority)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	var retainedFields map[string]string
	err = resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(reference credential.RuntimeCredentialReference, fields map[string]string) error {
		called = true
		if reference.Scope != fixture.scope || reference.VersionID != fixture.versionA.Binding.VersionID {
			t.Fatalf("runtime received reference %+v; want authority-pinned scope/version", reference)
		}
		if fields["password"] != "runtime-password-A" {
			t.Fatal("runtime did not receive authority-pinned version A")
		}
		retainedFields = fields
		return nil
	})
	if err != nil {
		t.Fatalf("WithCredential() error = %v", err)
	}
	if !called {
		t.Fatal("WithCredential() did not invoke its callback")
	}
	if authority.calls != 2 {
		t.Fatalf("runtime authority calls = %d, want initial resolution and post-read recheck", authority.calls)
	}
	for index := range authority.identities {
		if authority.identities[index] != fixture.identity || authority.resources[index] != fixture.resource {
			t.Fatalf("authority call %d used identity/resource %+v/%+v", index+1, authority.identities[index], authority.resources[index])
		}
	}
	if len(retainedFields) != 0 {
		t.Fatal("runtime retained plaintext fields after callback")
	}
	if stateAfter := runtimeState(t, fixture.db); stateAfter != stateBefore {
		t.Fatalf("runtime read changed durable state: before=%+v after=%+v", stateBefore, stateAfter)
	}
}

func TestRuntimeResolverDoesNotFallBackWhenPinnedVersionIsMissing(t *testing.T) {
	fixture := newRuntimeIntegrationFixture(t)
	authority := &runtimeIntegrationAuthority{references: []credential.RuntimeCredentialReference{{
		Scope: fixture.scope, VersionID: uuid.NewString(),
	}}}
	resolver, err := credential.NewRuntimeResolver(fixture.repo, fixture.keys, authority)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(credential.RuntimeCredentialReference, map[string]string) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("WithCredential() succeeded for a nonexistent server-pinned version")
	}
	if called {
		t.Fatal("WithCredential() fell back to another saved draft version")
	}
}

func TestRuntimeResolverRejectsWrongDecryptionKey(t *testing.T) {
	fixture := newRuntimeIntegrationFixture(t)
	wrongKeys := loadTestCredentialKeyring(t, "runtime-deployment", "runtime-key", bytes.Repeat([]byte{0x69}, 32))
	authority := &runtimeIntegrationAuthority{references: []credential.RuntimeCredentialReference{{
		Scope: fixture.scope, VersionID: fixture.versionA.Binding.VersionID,
	}}}
	resolver, err := credential.NewRuntimeResolver(fixture.repo, wrongKeys, authority)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(credential.RuntimeCredentialReference, map[string]string) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("WithCredential() succeeded with a key that cannot authenticate the stored envelope")
	}
	if called {
		t.Fatal("WithCredential() invoked its callback after decryption failed")
	}
}

func TestRuntimeResolverRechecksAuthorityBeforeDecrypting(t *testing.T) {
	fixture := newRuntimeIntegrationFixture(t)
	authority := &runtimeIntegrationAuthority{references: []credential.RuntimeCredentialReference{
		{Scope: fixture.scope, VersionID: fixture.versionA.Binding.VersionID},
		{Scope: fixture.scope, VersionID: fixture.versionB.Binding.VersionID},
	}}
	resolver, err := credential.NewRuntimeResolver(fixture.repo, fixture.keys, authority)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(credential.RuntimeCredentialReference, map[string]string) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("WithCredential() succeeded after the authority changed the selected version")
	}
	if authority.calls != 2 {
		t.Fatalf("runtime authority calls = %d, want a post-read recheck", authority.calls)
	}
	if called {
		t.Fatal("WithCredential() invoked its callback after the authority recheck changed")
	}
}
