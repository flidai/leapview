package module

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectors"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

type scopeBindingReader struct{ binding TargetConnectionBinding }

func (reader scopeBindingReader) ReadTargetConnectionBinding(_ context.Context, target, project, environment, connection string) (TargetConnectionBinding, error) {
	if reader.binding.TargetID != target || reader.binding.ProjectID != project || reader.binding.Environment != environment || reader.binding.ConnectionID != connection {
		return TargetConnectionBinding{}, errors.New("binding scope mismatch")
	}
	return reader.binding, nil
}

type scopeOwnerReader struct{ owner string }

func (reader scopeOwnerReader) CustomerOwner(context.Context) (string, error) {
	return reader.owner, nil
}

func TestCredentialMetadataScopeSurvivesAuthenticationConfigurationChanges(t *testing.T) {
	resource := credential.Resource{ScopeKind: "connection", TargetID: "target_1", ProjectID: "project_one", Environment: "production", ResourceID: "connection_one"}
	projectID := projectgraph.ResourceID(resource.ProjectID)
	const deploymentID = "lvinst_0123456789abcdefghijklmnopqrstuv"
	versionID := uuid.NewString()
	metadata := credential.Metadata{
		Binding: encryption.Binding{
			DeploymentID: deploymentID, OwnerID: "customer:one", ScopeKind: resource.ScopeKind,
			TargetID: resource.TargetID, ProjectID: resource.ProjectID, Environment: resource.Environment,
			ResourceID: resource.ResourceID, Purpose: connectionCredentialPurpose, Provider: "postgres",
			Destination: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", VersionID: versionID,
		},
		ActorID: "actor", CreatedAt: time.Now().UTC(),
	}
	store := &metadataReadRepository{metadata: metadata}
	authorizer := connectionCredentialAuthorizer{authorize: func(context.Context, string, string, string, access.Action) (bool, error) { return true, nil }}
	keys := metadataReadEncryptor{deploymentID: deploymentID}

	for _, tc := range []struct {
		name, provider, mode string
	}{
		{name: "none", provider: "postgres", mode: "none"},
		{name: "workload identity", provider: "postgres", mode: "workload_identity"},
		{name: "known no-auth connector", provider: "managed", mode: "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := TargetConnectionBinding{
				TargetID: resource.TargetID, ProjectID: resource.ProjectID, Environment: resource.Environment,
				ConnectionID: resource.ResourceID, ConnectorKind: tc.provider,
				AuthenticationMode: tc.mode, EndpointConfigHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			}
			resolver := connectionCredentialScopeResolver{
				instanceID: resource.TargetID, environment: resource.Environment, ownerReader: scopeOwnerReader{owner: "customer:one"},
				currentProject: func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
				bindings:       scopeBindingReader{binding: binding},
			}
			service, err := credential.NewService(store, keys, resolver, authorizer)
			if err != nil {
				t.Fatal(err)
			}
			got, err := service.GetDraft(t.Context(), "actor", resource, versionID)
			if err != nil || got.Binding.VersionID != versionID {
				t.Fatalf("GetDraft() = %#v, %v", got, err)
			}
			page, err := service.ListDrafts(t.Context(), "actor", resource, 10, "")
			if err != nil || len(page.Items) != 1 || page.Items[0].Binding.VersionID != versionID {
				t.Fatalf("ListDrafts() = %#v, %v", page, err)
			}
		})
	}
	if spec, ok := connectors.LookupConnection("managed"); !ok || len(spec.AuthKeys) != 0 {
		t.Fatal("test connector no longer exercises a supported no-auth provider")
	}
}

func TestCredentialScopeRejectsWrongTargetEnvironmentAndActiveProject(t *testing.T) {
	resource := credential.Resource{ScopeKind: "connection", TargetID: "target_1", ProjectID: "project_one", Environment: "production", ResourceID: "connection_one"}
	validBinding := TargetConnectionBinding{
		TargetID: resource.TargetID, ProjectID: resource.ProjectID, Environment: resource.Environment,
		ConnectionID: resource.ResourceID, ConnectorKind: "postgres", AuthenticationMode: "external_bundle",
		EndpointConfigHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for _, tc := range []struct {
		name          string
		resource      credential.Resource
		activeProject projectgraph.ResourceID
	}{
		{name: "wrong target", resource: func() credential.Resource { value := resource; value.TargetID = "target_2"; return value }(), activeProject: projectgraph.ResourceID(resource.ProjectID)},
		{name: "wrong environment", resource: func() credential.Resource { value := resource; value.Environment = "staging"; return value }(), activeProject: projectgraph.ResourceID(resource.ProjectID)},
		{name: "wrong active project", resource: resource, activeProject: "project_two"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := connectionCredentialScopeResolver{
				instanceID: resource.TargetID, environment: resource.Environment, ownerReader: scopeOwnerReader{owner: "customer:one"},
				currentProject: func(context.Context) (projectgraph.ResourceID, error) { return tc.activeProject, nil },
				bindings:       scopeBindingReader{binding: validBinding},
			}
			if _, err := resolver.ResolveCredentialScope(t.Context(), tc.resource); !errors.Is(err, credential.ErrNotFound) {
				t.Fatalf("ResolveCredentialScope() error = %v, want not found", err)
			}
		})
	}
}

type metadataReadEncryptor struct{ deploymentID string }

func (keys metadataReadEncryptor) DeploymentID() string { return keys.deploymentID }
func (keys metadataReadEncryptor) Encrypt(context.Context, encryption.Budget, encryption.Binding, []byte) (encryption.Envelope, error) {
	return encryption.Envelope{}, errors.New("metadata reads must not encrypt")
}

type metadataReadRepository struct{ metadata credential.Metadata }

func (*metadataReadRepository) ReserveEncryption(context.Context, string, string, encryption.KeyCommitment) error {
	return nil
}
func (*metadataReadRepository) SaveDraft(context.Context, credential.StoredVersion, access.AuditIntent) error {
	return errors.New("metadata reads must not save")
}
func (repository *metadataReadRepository) GetDraftMetadata(_ context.Context, deploymentID, ownerID string, resource credential.Resource, versionID string) (credential.Metadata, error) {
	if repository.metadata.Binding.DeploymentID != deploymentID || repository.metadata.Binding.OwnerID != ownerID || repository.metadata.Binding.VersionID != versionID ||
		(credential.Resource{ScopeKind: repository.metadata.Binding.ScopeKind, TargetID: repository.metadata.Binding.TargetID, ProjectID: repository.metadata.Binding.ProjectID, Environment: repository.metadata.Binding.Environment, ResourceID: repository.metadata.Binding.ResourceID}) != resource {
		return credential.Metadata{}, credential.ErrNotFound
	}
	return repository.metadata, nil
}
func (repository *metadataReadRepository) ListDrafts(_ context.Context, deploymentID, ownerID string, resource credential.Resource, _ int, _ string) (credential.DraftPage, error) {
	if repository.metadata.Binding.DeploymentID != deploymentID || repository.metadata.Binding.OwnerID != ownerID ||
		(credential.Resource{ScopeKind: repository.metadata.Binding.ScopeKind, TargetID: repository.metadata.Binding.TargetID, ProjectID: repository.metadata.Binding.ProjectID, Environment: repository.metadata.Binding.Environment, ResourceID: repository.metadata.Binding.ResourceID}) != resource {
		return credential.DraftPage{}, nil
	}
	return credential.DraftPage{Items: []credential.Metadata{repository.metadata}}, nil
}
