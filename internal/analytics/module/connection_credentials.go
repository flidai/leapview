package module

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsenvironment "github.com/flidai/leapview/internal/analytics/environment"
	"github.com/flidai/leapview/internal/analytics/infisical"
)

const developmentConnectionVariablePrefix = "LEAPVIEW_DEV_CONNECTION_"

type TargetCredentialConfig struct {
	InfisicalBaseURL               string
	InfisicalUniversalClientID     string
	InfisicalUniversalClientSecret string `json:"-" yaml:"-"`
	InfisicalAllowedScopes         string
}

func (TargetCredentialConfig) String() string   { return "<target-credential-config:redacted>" }
func (TargetCredentialConfig) GoString() string { return "module.TargetCredentialConfig{<redacted>}" }

func (config TargetCredentialConfig) configured() bool {
	return strings.TrimSpace(config.InfisicalBaseURL) != "" ||
		strings.TrimSpace(config.InfisicalUniversalClientID) != "" ||
		config.InfisicalUniversalClientSecret != "" ||
		strings.TrimSpace(config.InfisicalAllowedScopes) != ""
}

func buildTargetResolvers(config TargetCredentialConfig) (connectionbinding.ResolverSet, error) {
	if !config.configured() {
		return connectionbinding.ResolverSet{}, nil
	}
	client := &http.Client{Timeout: 15 * time.Second}
	var scopes []struct {
		ProjectID        string `json:"projectId"`
		Environment      string `json:"environment"`
		SecretPathPrefix string `json:"secretPathPrefix"`
	}
	if err := json.Unmarshal([]byte(config.InfisicalAllowedScopes), &scopes); err != nil || len(scopes) == 0 {
		return connectionbinding.ResolverSet{}, fmt.Errorf("%w: Infisical allowed scopes must be a non-empty JSON array", connectionbinding.ErrInvalidBinding)
	}
	allowed := make([]infisical.AllowedScope, len(scopes))
	for index, scope := range scopes {
		allowed[index] = infisical.AllowedScope{
			ProjectID: scope.ProjectID, Environment: scope.Environment, SecretPathPrefix: scope.SecretPathPrefix,
		}
	}
	authenticator, err := infisical.NewUniversalAuthenticator(infisical.UniversalAuthConfig{
		BaseURL: config.InfisicalBaseURL, ClientID: config.InfisicalUniversalClientID,
		ClientSecret: config.InfisicalUniversalClientSecret, HTTPClient: client, Now: time.Now,
	})
	if err != nil {
		return connectionbinding.ResolverSet{}, err
	}
	resolver, err := infisical.NewResolver(infisical.Config{
		BaseURL: config.InfisicalBaseURL, HTTPClient: client,
		Authenticator: authenticator, Now: time.Now, AllowedScopes: allowed,
	})
	if err != nil {
		return connectionbinding.ResolverSet{}, err
	}
	return connectionbinding.ResolverSet{Infisical: resolver}, nil
}

func buildProcessDevelopmentTargetResolver(
	targetID string,
	environment string,
) (connectionbinding.CredentialResolver, error) {
	return buildDevelopmentTargetResolver(
		targetID, environment, os.Environ(), os.LookupEnv, time.Now,
	)
}

func buildDevelopmentTargetResolver(
	targetID string,
	environment string,
	environ []string,
	lookup func(string) (string, bool),
	now func() time.Time,
) (connectionbinding.CredentialResolver, error) {
	allowedSet := map[string]struct{}{}
	for _, item := range environ {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(name, developmentConnectionVariablePrefix) &&
			len(name) > len(developmentConnectionVariablePrefix) {
			allowedSet[name] = struct{}{}
		}
	}
	if len(allowedSet) == 0 {
		return nil, nil
	}
	allowed := make([]string, 0, len(allowedSet))
	for name := range allowedSet {
		allowed = append(allowed, name)
	}
	sort.Strings(allowed)
	selection, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: connectionbinding.TargetID(targetID), Environment: environment,
		TargetClass: connectionbinding.TargetDevelopment, Kind: connectionbinding.ResolverEnvironment,
	})
	if err != nil {
		return nil, err
	}
	return analyticsenvironment.NewResolver(analyticsenvironment.Config{
		Selection: selection, AllowedVariables: allowed, LookupEnv: lookup,
		Now: now, TTL: 15 * time.Minute,
	})
}

func (m *Module) TargetCredentialResolver(
	selection connectionbinding.ResolverSelection,
	development connectionbinding.CredentialResolver,
) (connectionbinding.CredentialResolver, error) {
	if m == nil {
		return nil, connectionbinding.ErrProviderUnavailable
	}
	resolvers := m.targetResolvers
	resolvers.Environment = development
	return connectionbinding.SelectResolver(selection, resolvers)
}
