package duckdb

import (
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func ApplyTargetBinding(
	logical semanticmodel.Connection,
	binding connectionbinding.TargetBinding,
	snapshot connectionbinding.CredentialSnapshot,
) (semanticmodel.Connection, error) {
	if err := binding.Validate(); err != nil || !binding.Enabled ||
		strings.TrimSpace(logical.Kind) != binding.ConnectorKind ||
		binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle {
		return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
	}
	resolved := logical
	resolved.Host = binding.Endpoint.Host
	resolved.Port = binding.Endpoint.Port
	resolved.Database = binding.Endpoint.Database
	resolved.Username = binding.Endpoint.SourceIdentity
	resolved.SSLMode = binding.Endpoint.TLSMode
	if binding.Endpoint.ObjectScope != "" {
		resolved.Scope = binding.Endpoint.ObjectScope
	}
	if len(binding.Endpoint.Options) > 0 {
		resolved.Options = make(map[string]any, len(logical.Options)+len(binding.Endpoint.Options))
		for key, value := range logical.Options {
			resolved.Options[key] = value
		}
		for key, value := range binding.Endpoint.Options {
			resolved.Options[key] = value
		}
	}
	resolved.Credentials = semanticmodel.ConnectionCredentials{}
	if err := snapshot.Use(func(values map[string]string) error {
		resolved.Auth = make(semanticmodel.ConnectionAuth, len(values))
		for key, value := range values {
			resolved.Auth[key] = value
		}
		return nil
	}); err != nil {
		return semanticmodel.Connection{}, connectionbinding.ErrInvalidCredentialBundle
	}
	validated, err := resolved.Validate(binding.ConnectionID.String())
	if err != nil {
		clear(resolved.Auth)
		return semanticmodel.Connection{}, connectionbinding.ErrInvalidCredentialBundle
	}
	return validated, nil
}
