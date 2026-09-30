package module

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestServicesRuntimeReaderUsesConfiguredDependenciesAndRequestAuthority(t *testing.T) {
	resource := RuntimeResource{ScopeKind: "connection", TargetID: "instance-one", ProjectID: "project_one", Environment: "production", ResourceID: "connection_one"}
	reference := RuntimeCredentialReference{
		Scope:     RuntimeScope{Resource: resource, OwnerID: "customer-one", Purpose: "connection-authentication", Provider: "postgres", Destination: "sha256:" + strings.Repeat("a", 64)},
		VersionID: "fb1a93e4-c9e4-4f50-90a4-8c2f7e2bfa7d",
	}
	identity := projectgraph.ServingIdentity{ProjectID: "project_one", Environment: "production", GenerationID: "generation_one"}
	repository := &moduleRuntimeRepository{reference: reference}
	keys := &moduleRuntimeKeyring{}
	services := &Services{runtimeRepository: repository, runtimeKeys: keys}
	allowed := &moduleRuntimeAuthority{reference: reference}
	denied := &moduleRuntimeAuthority{err: credential.ErrForbidden}
	allowedReader, err := services.RuntimeReader(allowed)
	require.NoError(t, err)
	deniedReader, err := services.RuntimeReader(denied)
	require.NoError(t, err)

	var retained map[string]string
	err = allowedReader.WithCredential(t.Context(), identity, resource, func(got RuntimeCredentialReference, fields map[string]string) error {
		require.Equal(t, reference, got)
		require.Equal(t, "configured-secret", fields["password"])
		retained = fields
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, allowed.calls)
	require.Equal(t, 1, repository.calls)
	require.Equal(t, "configured-deployment", repository.deploymentID)
	require.Equal(t, reference.Scope.OwnerID, repository.ownerID)
	require.Equal(t, resource, repository.resource)
	require.Equal(t, reference.VersionID, repository.versionID)
	require.Equal(t, 1, keys.calls)
	require.Empty(t, retained)
	require.Equal(t, make([]byte, len(keys.plaintext)), keys.plaintext)

	err = deniedReader.WithCredential(t.Context(), identity, resource, func(RuntimeCredentialReference, map[string]string) error {
		t.Fatal("denied request reached credential consumer")
		return nil
	})
	require.ErrorIs(t, err, credential.ErrForbidden)
	require.Equal(t, 1, denied.calls)
	require.Equal(t, 2, allowed.calls, "each reader must retain its own request authority")
	require.Equal(t, 1, repository.calls, "denied request must not reach the shared repository")
	require.Equal(t, 1, keys.calls)
}

func TestServicesRuntimeReaderRejectsMissingConfiguration(t *testing.T) {
	var absentServices *Services
	var absentRepository *moduleRuntimeRepository
	var absentKeys *moduleRuntimeKeyring
	var absentAuthority *moduleRuntimeAuthority
	configured := &Services{runtimeRepository: &moduleRuntimeRepository{}, runtimeKeys: &moduleRuntimeKeyring{}}
	for _, test := range []struct {
		name      string
		services  *Services
		authority RuntimeUseAuthority
	}{
		{"nil services", absentServices, &moduleRuntimeAuthority{}},
		{"unconfigured", &Services{}, &moduleRuntimeAuthority{}},
		{"typed nil repository", &Services{runtimeRepository: absentRepository, runtimeKeys: &moduleRuntimeKeyring{}}, &moduleRuntimeAuthority{}},
		{"typed nil keys", &Services{runtimeRepository: &moduleRuntimeRepository{}, runtimeKeys: absentKeys}, &moduleRuntimeAuthority{}},
		{"nil authority", configured, nil},
		{"typed nil authority", configured, absentAuthority},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, err := test.services.RuntimeReader(test.authority)
			require.Nil(t, reader)
			require.ErrorIs(t, err, ErrRuntimeUnavailable)
		})
	}
}

type moduleRuntimeAuthority struct {
	reference RuntimeCredentialReference
	err       error
	calls     int
}

func (a *moduleRuntimeAuthority) ResolveRuntimeCredential(context.Context, projectgraph.ServingIdentity, RuntimeResource) (RuntimeCredentialReference, error) {
	a.calls++
	return a.reference, a.err
}

type moduleRuntimeRepository struct {
	reference                        RuntimeCredentialReference
	calls                            int
	deploymentID, ownerID, versionID string
	resource                         RuntimeResource
}

func (r *moduleRuntimeRepository) GetStoredDraft(_ context.Context, deploymentID, ownerID string, resource RuntimeResource, versionID string) (credential.StoredVersion, error) {
	r.calls++
	r.deploymentID, r.ownerID, r.resource, r.versionID = deploymentID, ownerID, resource, versionID
	scope := r.reference.Scope
	return credential.StoredVersion{Metadata: credential.Metadata{ActorID: "author", CreatedAt: time.Now().UTC(), Binding: encryption.Binding{
		DeploymentID: deploymentID, OwnerID: scope.OwnerID, ScopeKind: scope.Resource.ScopeKind,
		TargetID: scope.Resource.TargetID, ProjectID: scope.Resource.ProjectID, Environment: scope.Resource.Environment,
		ResourceID: scope.Resource.ResourceID, Purpose: scope.Purpose, Provider: scope.Provider,
		Destination: scope.Destination, VersionID: r.reference.VersionID,
	}}}, nil
}

type moduleRuntimeKeyring struct {
	calls     int
	plaintext []byte
}

func (*moduleRuntimeKeyring) DeploymentID() string { return "configured-deployment" }
func (k *moduleRuntimeKeyring) Decrypt(encryption.Binding, encryption.Envelope) ([]byte, error) {
	k.calls++
	k.plaintext = []byte(`{"password":"configured-secret"}`)
	return k.plaintext, nil
}
