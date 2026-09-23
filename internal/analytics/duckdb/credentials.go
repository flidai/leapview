package duckdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsenvironment "github.com/flidai/leapview/internal/analytics/environment"
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
	selection        connectionbinding.ResolverSelection
	allowedVariables map[string]struct{}
}

func NewDevelopmentEnvironmentCredentialResolver(
	selection connectionbinding.ResolverSelection,
	allowedVariables []string,
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
	allowed, err := developmentCredentialAllowlist(allowedVariables)
	if err != nil {
		return DevelopmentEnvironmentCredentialResolver{}, err
	}
	return DevelopmentEnvironmentCredentialResolver{selection: validated, allowedVariables: allowed}, nil
}

// NewUnboundDevelopmentEnvironmentCredentialResolver builds the process
// environment resolver before the first serving generation has established a
// project. Environment credentials are resolved from the authored connection
// reference at query time; no project identity is captured in this resolver.
func NewUnboundDevelopmentEnvironmentCredentialResolver(targetID connectionbinding.TargetID, environment string, allowedVariables []string) (DevelopmentEnvironmentCredentialResolver, error) {
	if err := connectionbinding.ValidateResolverTarget(targetID, environment); err != nil {
		return DevelopmentEnvironmentCredentialResolver{}, err
	}
	allowed, err := developmentCredentialAllowlist(allowedVariables)
	if err != nil {
		return DevelopmentEnvironmentCredentialResolver{}, err
	}
	return DevelopmentEnvironmentCredentialResolver{selection: connectionbinding.ResolverSelection{
		TargetID: targetID, Environment: strings.TrimSpace(environment),
		TargetClass: connectionbinding.TargetDevelopment, Kind: connectionbinding.ResolverEnvironment,
	}, allowedVariables: allowed}, nil
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
		if _, allowed := resolver.allowedVariables[secretName]; !allowed {
			return nil, fmt.Errorf("connection %q credential reference is not selected", name)
		}
		value, ok := os.LookupEnv(secretName)
		if !ok {
			return nil, fmt.Errorf("connection %q credential reference is unavailable", name)
		}
		value, err := analyticsenvironment.DecodeCredentialBundleTransport(value)
		if err != nil {
			return nil, fmt.Errorf("connection %q credential bundle is invalid", name)
		}
		if err := analyticsenvironment.ValidateCredentialBundle(value); err != nil {
			return nil, fmt.Errorf("connection %q credential bundle is invalid", name)
		}
		var object map[string]string
		if err := json.Unmarshal([]byte(value), &object); err != nil {
			return nil, fmt.Errorf("connection %q credential bundle is invalid", name)
		}
		result := make(semanticmodel.ConnectionAuth, len(object))
		for key, item := range object {
			result[key] = item
		}
		return result, nil
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

func developmentCredentialAllowlist(values []string) (map[string]struct{}, error) {
	ordered := append([]string(nil), values...)
	sort.Strings(ordered)
	result := make(map[string]struct{}, len(ordered))
	for _, value := range ordered {
		if !strings.HasPrefix(value, "LEAPVIEW_DEV_CONNECTION_") || len(value) == len("LEAPVIEW_DEV_CONNECTION_") {
			return nil, connectionbinding.ErrInvalidBinding
		}
		for _, char := range strings.TrimPrefix(value, "LEAPVIEW_DEV_CONNECTION_") {
			if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
				return nil, connectionbinding.ErrInvalidBinding
			}
		}
		if _, duplicate := result[value]; duplicate {
			return nil, connectionbinding.ErrInvalidBinding
		}
		result[value] = struct{}{}
	}
	return result, nil
}
