package app

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

const (
	runtimeReaderPinnedVersion  = "fb1a93e4-c9e4-4f50-90a4-8c2f7e2bfa7d"
	runtimeReaderNewerVersion   = "0070a4f4-5dcb-4d26-a69a-dac930858573"
	runtimeReaderChangedVersion = "7d238a62-7d4e-41a6-9a51-a9a5e7f7b285"
)

func TestLocalRuntimeCredentialCheckUsesRuntimeResolverExactAuthorityAndClearsValues(t *testing.T) {
	check, binding, identity, _, _ := runtimeCheckFixture(t)
	resource := credential.Resource{
		ScopeKind: "connection", TargetID: binding.TargetID.String(), ProjectID: identity.ProjectID.String(),
		Environment: identity.Environment, ResourceID: binding.ConnectionID.String(),
	}
	scope := credential.Scope{
		Resource: resource, OwnerID: "customer-one", Purpose: "connection-authentication",
		Provider: "postgres", Destination: binding.Evidence().EndpointConfigHash,
	}

	pinnedBinding := runtimeReaderEncryptionBinding(scope, runtimeReaderPinnedVersion)
	newerBinding := runtimeReaderEncryptionBinding(scope, runtimeReaderNewerVersion)
	repository := &runtimeReaderRepository{versions: map[string]credential.StoredVersion{
		runtimeReaderPinnedVersion: runtimeReaderStoredVersion(pinnedBinding, `{"password":"runtime-check-secret"}`),
		runtimeReaderNewerVersion:  runtimeReaderStoredVersion(newerBinding, `{"password":"newer-draft-secret"}`),
	}}
	keys := &runtimeReaderKeyring{deploymentID: "deployment-runtime-check"}
	authority := &runtimeReaderAuthority{references: []credential.RuntimeCredentialReference{
		{Scope: scope, VersionID: runtimeReaderPinnedVersion},
		{Scope: scope, VersionID: runtimeReaderPinnedVersion},
	}}
	resolver, err := credential.NewRuntimeResolver(repository, keys, authority)
	require.NoError(t, err)
	check.reader = resolver
	analytics := &runtimeReaderAnalytics{}
	check.analytics = analytics

	got, err := check.check(t.Context(), identity, binding, runtimeReaderPinnedVersion)
	require.NoError(t, err)
	require.Equal(t, connectionbinding.CredentialIdentity{CredentialVersionID: runtimeReaderPinnedVersion}, got)
	require.Equal(t, 2, authority.calls, "the real resolver must check authority before and after storage")
	require.Equal(t, []projectgraph.ServingIdentity{identity, identity}, authority.identities)
	require.Equal(t, []credential.Resource{resource, resource}, authority.resources)
	require.Equal(t, "deployment-runtime-check", repository.deploymentID)
	require.Equal(t, scope.OwnerID, repository.ownerID)
	require.Equal(t, resource, repository.resource)
	require.Equal(t, runtimeReaderPinnedVersion, repository.versionID,
		"the authorized committed version must be fetched exactly even when a newer draft exists")
	require.NotEqual(t, runtimeReaderNewerVersion, repository.versionID)
	require.Equal(t, 1, analytics.preparations)
	require.Equal(t, "runtime-check-secret", analytics.password)
	require.Empty(t, analytics.retainedFields, "RuntimeResolver must clear the callback field map")
	require.Equal(t, 1, keys.decryptCalls)
	require.Equal(t, pinnedBinding, keys.binding)
	require.Equal(t, make([]byte, len(keys.lastPlaintext)), keys.lastPlaintext,
		"RuntimeResolver must clear decrypted plaintext bytes after its callback")
}

func TestLocalRuntimeCredentialCheckStopsPreparationWhenAuthorityChanges(t *testing.T) {
	check, binding, identity, _, _ := runtimeCheckFixture(t)
	resource := credential.Resource{
		ScopeKind: "connection", TargetID: binding.TargetID.String(), ProjectID: identity.ProjectID.String(),
		Environment: identity.Environment, ResourceID: binding.ConnectionID.String(),
	}
	scope := credential.Scope{
		Resource: resource, OwnerID: "customer-one", Purpose: "connection-authentication",
		Provider: "postgres", Destination: binding.Evidence().EndpointConfigHash,
	}
	pinnedBinding := runtimeReaderEncryptionBinding(scope, runtimeReaderPinnedVersion)
	repository := &runtimeReaderRepository{versions: map[string]credential.StoredVersion{
		runtimeReaderPinnedVersion: runtimeReaderStoredVersion(pinnedBinding, `{"password":"runtime-check-secret"}`),
	}}
	keys := &runtimeReaderKeyring{deploymentID: "deployment-runtime-check"}
	authority := &runtimeReaderAuthority{references: []credential.RuntimeCredentialReference{
		{Scope: scope, VersionID: runtimeReaderPinnedVersion},
		{Scope: scope, VersionID: runtimeReaderChangedVersion},
	}}
	resolver, err := credential.NewRuntimeResolver(repository, keys, authority)
	require.NoError(t, err)
	check.reader = resolver
	analytics := &runtimeReaderAnalytics{}
	check.analytics = analytics

	got, err := check.check(t.Context(), identity, binding, runtimeReaderPinnedVersion)
	require.ErrorIs(t, err, credential.ErrConflict)
	require.Zero(t, got)
	require.Equal(t, 2, authority.calls)
	require.Equal(t, runtimeReaderPinnedVersion, repository.versionID)
	require.Zero(t, keys.decryptCalls, "changed authority must be detected before decryption")
	require.Zero(t, analytics.preparations, "changed authority must never reach pool preparation")
	require.Empty(t, analytics.retainedFields)
}

type runtimeReaderRepository struct {
	versions     map[string]credential.StoredVersion
	deploymentID string
	ownerID      string
	resource     credential.Resource
	versionID    string
}

func (repository *runtimeReaderRepository) GetStoredDraft(_ context.Context, deploymentID, ownerID string, resource credential.Resource, versionID string) (credential.StoredVersion, error) {
	repository.deploymentID = deploymentID
	repository.ownerID = ownerID
	repository.resource = resource
	repository.versionID = versionID
	stored, ok := repository.versions[versionID]
	if !ok {
		return credential.StoredVersion{}, credential.ErrNotFound
	}
	return stored, nil
}

type runtimeReaderKeyring struct {
	deploymentID  string
	decryptCalls  int
	binding       encryption.Binding
	lastPlaintext []byte
}

func (keyring *runtimeReaderKeyring) DeploymentID() string { return keyring.deploymentID }

func (keyring *runtimeReaderKeyring) Decrypt(binding encryption.Binding, envelope encryption.Envelope) ([]byte, error) {
	keyring.decryptCalls++
	keyring.binding = binding
	if binding.Validate() != nil || len(envelope.Ciphertext) == 0 {
		return nil, errors.New("invalid test envelope")
	}
	keyring.lastPlaintext = bytes.Clone(envelope.Ciphertext)
	return keyring.lastPlaintext, nil
}

type runtimeReaderAuthority struct {
	references []credential.RuntimeCredentialReference
	identities []projectgraph.ServingIdentity
	resources  []credential.Resource
	calls      int
}

func (authority *runtimeReaderAuthority) ResolveRuntimeCredential(_ context.Context, identity projectgraph.ServingIdentity, resource credential.Resource) (credential.RuntimeCredentialReference, error) {
	authority.calls++
	authority.identities = append(authority.identities, identity)
	authority.resources = append(authority.resources, resource)
	index := authority.calls - 1
	if index >= len(authority.references) {
		index = len(authority.references) - 1
	}
	if index < 0 {
		return credential.RuntimeCredentialReference{}, credential.ErrNotFound
	}
	return authority.references[index], nil
}

type runtimeReaderAnalytics struct {
	preparations   int
	password       string
	retainedFields map[string]string
}

func (analytics *runtimeReaderAnalytics) CheckLocalRuntimeCredential(ctx context.Context, _ connectionbinding.TargetBinding, versionID string, read func(context.Context, func(map[string]string) error) error) (connectionbinding.CredentialIdentity, error) {
	err := read(ctx, func(fields map[string]string) error {
		analytics.preparations++
		analytics.retainedFields = fields
		analytics.password = fields["password"]
		return nil
	})
	if err != nil {
		return connectionbinding.CredentialIdentity{}, err
	}
	return connectionbinding.CredentialIdentity{CredentialVersionID: versionID}, nil
}

func runtimeReaderEncryptionBinding(scope credential.Scope, versionID string) encryption.Binding {
	return encryption.Binding{
		DeploymentID: "deployment-runtime-check", OwnerID: scope.OwnerID,
		ScopeKind: scope.Resource.ScopeKind, TargetID: scope.Resource.TargetID,
		ProjectID: scope.Resource.ProjectID, Environment: scope.Resource.Environment,
		ResourceID: scope.Resource.ResourceID, Purpose: scope.Purpose, Provider: scope.Provider,
		Destination: scope.Destination, VersionID: versionID,
	}
}

func runtimeReaderStoredVersion(binding encryption.Binding, plaintext string) credential.StoredVersion {
	return credential.StoredVersion{
		Metadata: credential.Metadata{Binding: binding, ActorID: "draft-actor", CreatedAt: time.Now().UTC()},
		Envelope: encryption.Envelope{Format: "test-only", KeyID: "test-key", Ciphertext: []byte(plaintext)},
	}
}
