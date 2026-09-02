package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	accesscli "github.com/flidai/leapview/internal/access/cli"
	"github.com/flidai/leapview/internal/app/api/clienttransport"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/platform/cliapi"
)

type authoringCredentialResolver interface {
	Resolve(context.Context, string) (accesscli.ResolvedCredential, error)
}

type capabilityAPIClient struct {
	httpClient        *http.Client
	authoring         authoringCredentialResolver
	validateAuthoring bool
}

func (client capabilityAPIClient) Resolve(ctx context.Context, credentials cliapi.Credentials) (cliapi.Credentials, error) {
	cfg := config.MustLoad()
	target := strings.TrimRight(strings.TrimSpace(credentials.Target), "/")
	if target == "" {
		target = strings.TrimRight(strings.TrimSpace(cfg.Target), "/")
	}
	token := strings.TrimSpace(credentials.Token)
	if token == "" {
		token = strings.TrimSpace(cfg.APIToken)
	}
	if target == "" {
		return cliapi.Credentials{}, fmt.Errorf("target is required")
	}
	// Explicit tokens support ephemeral CI and one-shot automation but are
	// never persisted by the CLI.
	if token != "" {
		return client.resolveResult(
			ctx,
			cliapi.Credentials{Target: target, Token: token},
			nil,
		)
	}
	workloadConfigured := strings.TrimSpace(cfg.WorkloadClientID) != "" ||
		strings.TrimSpace(cfg.WorkloadClientSecret) != "" ||
		strings.TrimSpace(cfg.WorkloadProject) != ""
	if workloadConfigured {
		if strings.TrimSpace(cfg.WorkloadClientID) == "" || strings.TrimSpace(cfg.WorkloadClientSecret) == "" ||
			strings.TrimSpace(cfg.WorkloadProject) == "" {
			return cliapi.Credentials{}, fmt.Errorf("workload identity requires LEAPVIEW_WORKLOAD_CLIENT_ID, LEAPVIEW_WORKLOAD_CLIENT_SECRET, and LEAPVIEW_WORKLOAD_PROJECT")
		}
		instance, err := newDeploymentCLIClient(client.http(), target, "").instance(ctx)
		if err != nil {
			return cliapi.Credentials{}, fmt.Errorf("discover workload target: %w", err)
		}
		workload, err := accesscli.ExchangeWorkloadIdentity(ctx, accesscli.StandardOAuthClient{HTTPClient: client.http()}, accesscli.WorkloadIdentityRequest{
			Origin: target, InstanceID: instance.Id, ProjectID: cfg.WorkloadProject,
			ClientID: cfg.WorkloadClientID, ClientSecret: cfg.WorkloadClientSecret,
			Capabilities: []string{
				"RESOURCE_USE",
				"RESOURCE_READ",
				"RESOURCE_EDIT",
				"RESOURCE_PUBLISH",
			},
			Lifetime: 15 * time.Minute,
		}, nil)
		if err != nil {
			return cliapi.Credentials{}, err
		}
		return client.resolveResult(
			ctx,
			cliapi.Credentials{
				Target: target,
				Token:  workload.AccessToken,
			},
			&cliapi.TargetProfile{
				Origin:      target,
				InstanceID:  instance.Id,
				Environment: instance.Environment,
				ProjectID:   cfg.WorkloadProject,
			},
		)
	}
	resolver := client.authoring
	if resolver == nil {
		var err error
		resolver, err = defaultAuthoringAuthenticator(client.http())
		if err != nil {
			return cliapi.Credentials{}, err
		}
	}
	resolved, err := resolver.Resolve(ctx, target)
	if err != nil {
		return cliapi.Credentials{}, fmt.Errorf("resolve authoring login for %q: %w; run leapview login %s", target, err, target)
	}
	return client.resolveResult(
		ctx,
		cliapi.Credentials{
			Target: resolved.Profile.Origin,
			Token:  resolved.AccessToken,
		},
		&resolved.Profile,
	)
}

func (client capabilityAPIClient) resolveResult(
	ctx context.Context,
	credentials cliapi.Credentials,
	expected *cliapi.TargetProfile,
) (cliapi.Credentials, error) {
	if !client.validateAuthoring {
		return credentials, nil
	}
	return client.validateAuthoringTarget(ctx, credentials, expected)
}

func (client capabilityAPIClient) validateAuthoringTarget(
	ctx context.Context,
	credentials cliapi.Credentials,
	expected *cliapi.TargetProfile,
) (cliapi.Credentials, error) {
	target := strings.TrimRight(
		strings.TrimSpace(credentials.Target),
		"/",
	)
	token := strings.TrimSpace(credentials.Token)
	remote := newDeploymentCLIClient(client.http(), target, token)
	instance, err := remote.instance(ctx)
	if err != nil {
		return cliapi.Credentials{}, fmt.Errorf(
			"could not reach authoring target %q: %w",
			target,
			err,
		)
	}
	instance.Id = strings.TrimSpace(instance.Id)
	instance.CanonicalOrigin = strings.TrimRight(
		strings.TrimSpace(instance.CanonicalOrigin),
		"/",
	)
	instance.Environment = strings.TrimSpace(instance.Environment)
	if instance.Id == "" ||
		instance.CanonicalOrigin == "" ||
		instance.Environment == "" {
		return cliapi.Credentials{}, fmt.Errorf(
			"incompatible client/server identity response from %q",
			target,
		)
	}
	if expected != nil {
		expectedOrigin := strings.TrimRight(
			strings.TrimSpace(expected.Origin),
			"/",
		)
		expectedEnvironment := strings.TrimSpace(
			expected.Environment,
		)
		if instance.Id != strings.TrimSpace(expected.InstanceID) ||
			instance.CanonicalOrigin != expectedOrigin ||
			(expectedEnvironment != "" &&
				instance.Environment != expectedEnvironment) {
			return cliapi.Credentials{}, fmt.Errorf(
				"target identity changed for %q; expected instance %q at %q in %q, got %q at %q in %q; run leapview logout and login again only after verifying the target",
				target,
				expected.InstanceID,
				expectedOrigin,
				expectedEnvironment,
				instance.Id,
				instance.CanonicalOrigin,
				instance.Environment,
			)
		}
	}
	capabilities, err := remote.capabilities(ctx)
	if err != nil {
		return cliapi.Credentials{}, fmt.Errorf(
			"authenticate authoring target %q and verify authoring permission: %w",
			target,
			err,
		)
	}
	apiVersion := strings.TrimSpace(capabilities.ApiVersion)
	if apiVersion != "v1" {
		return cliapi.Credentials{}, fmt.Errorf(
			"incompatible client/server API at %q: LeapView CLI requires v1, target reports %q",
			target,
			apiVersion,
		)
	}
	capabilitiesEnvironment := strings.TrimSpace(
		capabilities.Environment,
	)
	if capabilitiesEnvironment != instance.Environment {
		return cliapi.Credentials{}, fmt.Errorf(
			"incompatible client/server environment identity at %q: instance reports %q, capabilities report %q",
			target,
			instance.Environment,
			capabilitiesEnvironment,
		)
	}
	deliveryMode := cliapi.DeliveryMode(strings.TrimSpace(string(capabilities.DeliveryMode)))
	if deliveryMode != cliapi.DeliveryModeNativePostgres && deliveryMode != cliapi.DeliveryModeLegacySQLite {
		return cliapi.Credentials{}, fmt.Errorf(
			"incompatible client/server delivery mode at %q: target reports %q",
			target,
			deliveryMode,
		)
	}
	return cliapi.Credentials{
		Target: target, Token: token,
		CanonicalOrigin: instance.CanonicalOrigin,
		DeliveryMode:    deliveryMode,
	}, nil
}

func (client capabilityAPIClient) Environment(ctx context.Context, credentials cliapi.Credentials, asserted string) (string, error) {
	resolved, err := client.Resolve(ctx, credentials)
	if err != nil {
		return "", err
	}
	return targetEnvironment(ctx, http.DefaultClient, resolved.Target, resolved.Token, asserted)
}

func (client capabilityAPIClient) Transport(ctx context.Context, credentials cliapi.Credentials) (apigenclient.Transport, error) {
	resolved, err := client.Resolve(ctx, credentials)
	if err != nil {
		return nil, err
	}
	return capabilityAPITransport{
		target: resolved.Target,
		token:  resolved.Token,
		client: client.http(),
	}, nil
}

func (client capabilityAPIClient) PublicTransport(_ context.Context, target string) (apigenclient.Transport, error) {
	target = strings.TrimRight(strings.TrimSpace(target), "/")
	if target == "" {
		return nil, fmt.Errorf("target is required")
	}
	return capabilityAPITransport{target: target, client: client.http()}, nil
}

func (client capabilityAPIClient) http() *http.Client {
	if client.httpClient != nil {
		return client.httpClient
	}
	return http.DefaultClient
}

type capabilityAPITransport struct {
	target string
	token  string
	client *http.Client
}

func (transport capabilityAPITransport) DoAPIGen(ctx context.Context, request apigenclient.Request, out any) (apigenclient.Response, error) {
	return (clienttransport.Transport{
		Target: transport.target,
		Token:  transport.token,
		Client: transport.client,
		PrepareRequest: func(request *http.Request) {
			request.Header.Set("X-LeapView-Invocation-Surface", "cli")
		},
	}).DoAPIGen(ctx, request, out)
}

func doJSON(ctx context.Context, method, endpoint, token string, body io.Reader, out any) error {
	return doJSONWithHeaders(ctx, method, endpoint, token, nil, body, out)
}

func doJSONWithHeaders(ctx context.Context, method, endpoint, token string, headers map[string]string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", method, endpoint, strings.TrimSpace(string(bytes)))
	}
	if out == nil || len(bytes) == 0 {
		return nil
	}
	return json.Unmarshal(bytes, out)
}

func targetEnvironment(ctx context.Context, client *http.Client, target, token, asserted string) (string, error) {
	instance, err := newDeploymentCLIClient(client, target, token).instance(ctx)
	if err != nil {
		return "", fmt.Errorf("read target instance: %w", err)
	}
	environment := strings.TrimSpace(instance.Environment)
	if environment == "" {
		return "", fmt.Errorf("target instance returned an empty environment")
	}
	if asserted = strings.TrimSpace(asserted); asserted != "" && asserted != environment {
		return "", fmt.Errorf("requested environment %q does not match target instance environment %q", asserted, environment)
	}
	return environment, nil
}

func clientConfigPath() string {
	return config.MustLoad().ClientConfigPath()
}

func shortDigest(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func init() {
	http.DefaultClient.Timeout = 5 * time.Minute
}
