package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type credentialConnectionBindingLookup interface {
	Binding(context.Context, connectionbinding.BindingScope, connectionbinding.TargetID, projectgraph.ResourceID) (connectionbinding.TargetBinding, error)
}

type credentialAnalyticsProbe interface {
	CredentialProbePolicyIdentity() string
	ProbeCredential(context.Context, connectionbinding.TargetBinding, string, map[string]string) error
}

type credentialValidationProbe struct {
	bindings  credentialConnectionBindingLookup
	analytics credentialAnalyticsProbe
}

func newCredentialValidationProbe(bindings credentialConnectionBindingLookup, analytics credentialAnalyticsProbe) credentialValidationProbe {
	return credentialValidationProbe{bindings: bindings, analytics: analytics}
}

func (probe credentialValidationProbe) ResolveValidationTarget(
	ctx context.Context,
	resource credentialmodule.ValidationResource,
	scope credentialmodule.ValidationScope,
) (credentialmodule.ValidationTarget, error) {
	if ctx == nil || resource.Validate() != nil || resource.ScopeKind != "connection" ||
		scope.Resource != resource || typednil.IsNil(probe.bindings) || typednil.IsNil(probe.analytics) {
		return credentialmodule.ValidationTarget{}, credentialmodule.ErrInvalidValidation
	}
	binding, err := probe.binding(ctx, resource)
	if err != nil {
		return credentialmodule.ValidationTarget{}, err
	}
	policy := probe.analytics.CredentialProbePolicyIdentity()
	if policy == "" {
		return credentialmodule.ValidationTarget{}, credentialmodule.ErrValidationUnavailable
	}
	return credentialValidationTargetForBinding(binding, resource, scope, policy)
}

func (probe credentialValidationProbe) ProbeCredential(
	ctx context.Context,
	target credentialmodule.ValidationTarget,
	versionID string,
	fields map[string]string,
) error {
	if ctx == nil || target.Validate(target.Scope.Resource) != nil || typednil.IsNil(probe.analytics) {
		return credentialmodule.ErrInvalidValidation
	}
	binding, err := probe.binding(ctx, target.Scope.Resource)
	if err != nil {
		return err
	}
	current, err := credentialValidationTargetForBinding(
		binding, target.Scope.Resource, target.Scope, probe.analytics.CredentialProbePolicyIdentity(),
	)
	if err != nil {
		return err
	}
	if current != target {
		return credentialmodule.ErrValidationConflict
	}
	return probe.analytics.ProbeCredential(ctx, binding, versionID, fields)
}

func credentialValidationTargetForBinding(
	binding connectionbinding.TargetBinding,
	resource credentialmodule.ValidationResource,
	scope credentialmodule.ValidationScope,
	policy string,
) (credentialmodule.ValidationTarget, error) {
	if binding.Validate() != nil || resource.Validate() != nil || scope.Resource != resource ||
		binding.TargetID.String() != resource.TargetID || binding.Scope.ProjectID.String() != resource.ProjectID ||
		binding.Scope.Environment != resource.Environment || binding.ConnectionID.String() != resource.ResourceID ||
		binding.Evidence().EndpointConfigHash != scope.Destination || binding.ConnectorKind != scope.Provider ||
		!binding.Enabled || binding.ConnectorKind != "postgres" ||
		binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle {
		return credentialmodule.ValidationTarget{}, credentialmodule.ErrValidationConflict
	}
	if policy == "" {
		return credentialmodule.ValidationTarget{}, credentialmodule.ErrValidationUnavailable
	}
	digest, err := credentialValidationConfigurationDigest(binding, policy)
	if err != nil {
		return credentialmodule.ValidationTarget{}, credentialmodule.ErrValidationUnavailable
	}
	return credentialmodule.ValidationTarget{
		Scope: scope, BindingID: binding.ID.String(), BindingRevision: binding.Revision,
		ConfigurationDigest: digest,
	}, nil
}

func (probe credentialValidationProbe) binding(ctx context.Context, resource credentialmodule.ValidationResource) (connectionbinding.TargetBinding, error) {
	target, err := connectionbinding.ParseTargetID(resource.TargetID)
	if err != nil {
		return connectionbinding.TargetBinding{}, credentialmodule.ErrInvalidValidation
	}
	projectID, err := projectgraph.NewResourceID(resource.ProjectID)
	if err != nil {
		return connectionbinding.TargetBinding{}, credentialmodule.ErrInvalidValidation
	}
	connectionID, err := projectgraph.NewResourceID(resource.ResourceID)
	if err != nil {
		return connectionbinding.TargetBinding{}, credentialmodule.ErrInvalidValidation
	}
	binding, err := probe.bindings.Binding(ctx,
		connectionbinding.BindingScope{ProjectID: projectID, Environment: resource.Environment}, target, connectionID)
	if err != nil {
		if errors.Is(err, connectionbinding.ErrBindingNotFound) {
			return connectionbinding.TargetBinding{}, credentialmodule.ErrValidationNotFound
		}
		return connectionbinding.TargetBinding{}, credentialmodule.ErrValidationUnavailable
	}
	if binding.Validate() != nil || binding.TargetID != target || binding.Scope.ProjectID != projectID ||
		binding.Scope.Environment != resource.Environment || binding.ConnectionID != connectionID {
		return connectionbinding.TargetBinding{}, credentialmodule.ErrValidationNotFound
	}
	return binding, nil
}

func credentialValidationConfigurationDigest(binding connectionbinding.TargetBinding, policy string) (string, error) {
	if binding.Validate() != nil || policy == "" {
		return "", credentialmodule.ErrInvalidValidation
	}
	configuration := struct {
		Version             string                                `json:"version"`
		TargetID            string                                `json:"target_id"`
		ProjectID           string                                `json:"project_id"`
		Environment         string                                `json:"environment"`
		ConnectionID        string                                `json:"connection_id"`
		BindingID           string                                `json:"binding_id"`
		BindingRevision     int64                                 `json:"binding_revision"`
		ConnectorKind       string                                `json:"connector_kind"`
		AuthenticationMode  connectionbinding.AuthenticationMode  `json:"authentication_mode"`
		Enabled             bool                                  `json:"enabled"`
		Endpoint            connectionbinding.EndpointConfig      `json:"endpoint"`
		CredentialReference connectionbinding.CredentialReference `json:"credential_reference"`
		EndpointConfigHash  string                                `json:"endpoint_config_hash"`
		ProbePolicy         string                                `json:"probe_policy"`
	}{
		Version: "credential-validation-target-v1", TargetID: binding.TargetID.String(),
		ProjectID: binding.Scope.ProjectID.String(), Environment: binding.Scope.Environment,
		ConnectionID: binding.ConnectionID.String(), BindingID: binding.ID.String(),
		BindingRevision: binding.Revision, ConnectorKind: binding.ConnectorKind,
		AuthenticationMode: binding.AuthenticationMode, Enabled: binding.Enabled,
		Endpoint: binding.Endpoint, CredentialReference: binding.CredentialReference,
		EndpointConfigHash: binding.Evidence().EndpointConfigHash, ProbePolicy: policy,
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		return "", err
	}
	material := append([]byte("leapview:credential-validation-target:v1\x00"), encoded...)
	digest := sha256.Sum256(material)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

type credentialTargetBindingReader struct {
	bindings credentialConnectionBindingLookup
}

func newCredentialTargetBindingReader(bindings credentialConnectionBindingLookup) credentialmodule.TargetConnectionBindingReader {
	if typednil.IsNil(bindings) {
		return nil
	}
	return credentialTargetBindingReader{bindings: bindings}
}

func (reader credentialTargetBindingReader) ReadTargetConnectionBinding(
	ctx context.Context, targetID, projectID, environment, connectionID string,
) (credentialmodule.TargetConnectionBinding, error) {
	if ctx == nil || typednil.IsNil(reader.bindings) {
		return credentialmodule.TargetConnectionBinding{}, errors.New("credential connection binding authority is unavailable")
	}
	target, err := connectionbinding.ParseTargetID(targetID)
	if err != nil {
		return credentialmodule.TargetConnectionBinding{}, connectionbinding.ErrInvalidBinding
	}
	project, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		return credentialmodule.TargetConnectionBinding{}, connectionbinding.ErrInvalidBinding
	}
	connection, err := connectionbinding.ParseConnectionID(connectionID)
	if err != nil {
		return credentialmodule.TargetConnectionBinding{}, connectionbinding.ErrInvalidBinding
	}
	binding, err := reader.bindings.Binding(ctx, connectionbinding.BindingScope{ProjectID: project, Environment: environment}, target, connection)
	if err != nil {
		return credentialmodule.TargetConnectionBinding{}, err
	}
	if binding.Validate() != nil || binding.TargetID != target || binding.Scope.ProjectID != project ||
		binding.Scope.Environment != environment || binding.ConnectionID != connection {
		return credentialmodule.TargetConnectionBinding{}, connectionbinding.ErrBindingNotFound
	}
	evidence := binding.Evidence()
	return credentialmodule.TargetConnectionBinding{
		TargetID: binding.TargetID.String(), ProjectID: binding.Scope.ProjectID.String(),
		Environment: binding.Scope.Environment, ConnectionID: binding.ConnectionID.String(),
		ConnectorKind: binding.ConnectorKind, AuthenticationMode: string(binding.AuthenticationMode),
		EndpointConfigHash: evidence.EndpointConfigHash,
	}, nil
}
