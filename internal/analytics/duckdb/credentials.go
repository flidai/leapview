package duckdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/analytics/connectors"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// CredentialResolver is an infrastructure boundary. Authored and compiled
// model values contain only references; resolved secret values exist only for
// the lifetime of an admitted refresh preparation.
type CredentialResolver interface {
	Resolve(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error)
}

var ErrDevelopmentCredentialResolverRequired = errors.New("development credential resolver required")

type NonSecretCredentialResolver struct{}

func (NonSecretCredentialResolver) Resolve(_ context.Context, name string, connection semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
	provider := strings.TrimSpace(connection.Credentials.Provider)
	switch provider {
	case "", "none":
		return nil, nil
	case "ambient":
		return ambientAuth(connection), nil
	case "env":
		return nil, fmt.Errorf("connection %q: %w", name, ErrDevelopmentCredentialResolverRequired)
	default:
		return nil, fmt.Errorf("connection %q has unsupported credential provider %q", name, provider)
	}
}

type DevelopmentEnvironmentCredentialResolver struct {
	selection connectionbinding.ResolverSelection
}

func NewDevelopmentEnvironmentCredentialResolver(
	selection connectionbinding.ResolverSelection,
) (DevelopmentEnvironmentCredentialResolver, error) {
	validated, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput(selection))
	if err != nil {
		return DevelopmentEnvironmentCredentialResolver{}, err
	}
	if validated.TargetClass != connectionbinding.TargetDevelopment || validated.Kind != connectionbinding.ResolverEnvironment {
		return DevelopmentEnvironmentCredentialResolver{}, fmt.Errorf(
			"%w: environment credentials require an explicit development target selection",
			connectionbinding.ErrInvalidBinding,
		)
	}
	return DevelopmentEnvironmentCredentialResolver{selection: validated}, nil
}

// NewUnboundDevelopmentEnvironmentCredentialResolver builds the process
// environment resolver before the first serving generation has established a
// project. Environment credentials are resolved from the authored connection
// reference at query time; no project identity is captured in this resolver.
func NewUnboundDevelopmentEnvironmentCredentialResolver(targetID connectionbinding.TargetID, environment string) (DevelopmentEnvironmentCredentialResolver, error) {
	if err := connectionbinding.ValidateResolverTarget(targetID, environment); err != nil {
		return DevelopmentEnvironmentCredentialResolver{}, err
	}
	return DevelopmentEnvironmentCredentialResolver{selection: connectionbinding.ResolverSelection{
		TargetID: targetID, Environment: strings.TrimSpace(environment),
		TargetClass: connectionbinding.TargetDevelopment, Kind: connectionbinding.ResolverEnvironment,
	}}, nil
}

func (resolver DevelopmentEnvironmentCredentialResolver) Resolve(
	_ context.Context,
	name string,
	connection semanticmodel.Connection,
) (semanticmodel.ConnectionAuth, error) {
	if resolver.selection.Kind != connectionbinding.ResolverEnvironment ||
		resolver.selection.TargetClass != connectionbinding.TargetDevelopment {
		return nil, fmt.Errorf("%w: development credential resolver is not configured", connectionbinding.ErrInvalidBinding)
	}
	provider := strings.TrimSpace(connection.Credentials.Provider)
	switch provider {
	case "", "none":
		return nil, nil
	case "ambient":
		return ambientAuth(connection), nil
	case "env":
		secretName := strings.TrimSpace(connection.Credentials.Secret)
		value, ok := os.LookupEnv(secretName)
		if !ok {
			return nil, fmt.Errorf("connection %q credential reference is unavailable", name)
		}
		var object map[string]any
		if err := json.Unmarshal([]byte(value), &object); err == nil {
			return semanticmodel.ConnectionAuth(object), nil
		}
		spec, ok := connectors.LookupConnection(connection.Kind)
		if !ok {
			return nil, fmt.Errorf("connection %q has unsupported kind %q", name, connection.Kind)
		}
		for _, key := range []string{"connection_string", "token"} {
			if containsString(spec.AuthKeys, key) {
				return semanticmodel.ConnectionAuth{key: value}, nil
			}
		}
		return nil, fmt.Errorf("connection %q credential reference has an invalid shape", name)
	default:
		return nil, fmt.Errorf("connection %q has unsupported credential provider %q", name, provider)
	}
}

func ambientAuth(connection semanticmodel.Connection) semanticmodel.ConnectionAuth {
	auth := semanticmodel.ConnectionAuth{}
	if connection.Credentials.Region != "" {
		auth["region"] = connection.Credentials.Region
	}
	if connection.Credentials.Endpoint != "" {
		auth["endpoint"] = connection.Credentials.Endpoint
	}
	if connection.Credentials.AccountName != "" {
		auth["account_name"] = connection.Credentials.AccountName
	}
	return auth
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
