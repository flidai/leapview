package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	accesscli "github.com/flidai/leapview/internal/access/cli"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/stretchr/testify/require"
)

func TestTargetEnvironmentDiscoversAndAssertsInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instance" || r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("request = %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"environment":"prod"}`))
	}))
	defer server.Close()
	if got, err := targetEnvironment(context.Background(), server.Client(), server.URL, "token", ""); err != nil || got != "prod" {
		t.Fatalf("environment = %q, %v", got, err)
	}
	if _, err := targetEnvironment(context.Background(), server.Client(), server.URL, "token", "staging"); err == nil {
		t.Fatal("mismatched assertion succeeded")
	}
}

func TestCapabilityAPIClientRejectsLegacyPlaintextTokenConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.json")
	t.Setenv("LEAPVIEW_CLI_CONFIG", path)
	if err := os.WriteFile(path, []byte(`{"version":1,"targets":{"local":{"origin":"http://localhost:8080","instanceId":"lvinst_local","projectId":"project","credentialAccount":"account","token":"secret"}}}`), 0o600); err != nil {
		t.Fatalf("write client config: %v", err)
	}
	_, err := (capabilityAPIClient{}).Resolve(context.Background(), cliapi.Credentials{Target: "local"})
	if err == nil || !strings.Contains(err.Error(), "secret-bearing") {
		t.Fatalf("Resolve error = %v, want plaintext credential rejection", err)
	}
}

type fakeAuthoringResolver struct {
	name    string
	profile cliapi.TargetProfile
}

func (resolver *fakeAuthoringResolver) Resolve(_ context.Context, name string) (accesscli.ResolvedCredential, error) {
	resolver.name = name
	return accesscli.ResolvedCredential{
		Profile:     resolver.profile,
		AccessToken: "short-lived",
	}, nil
}

func TestCapabilityAPIClientResolvesAuthoringProfile(t *testing.T) {
	server := authoringIdentityServer(t, "v1")
	defer server.Close()
	resolver := &fakeAuthoringResolver{profile: cliapi.TargetProfile{
		Origin:      server.URL,
		InstanceID:  "lvinst_prod",
		Environment: "production",
		ProjectID:   "analytics",
	}}
	credentials, err := (capabilityAPIClient{
		httpClient:        server.Client(),
		authoring:         resolver,
		validateAuthoring: true,
	}).Resolve(
		context.Background(),
		cliapi.Credentials{Target: "prod"},
	)
	require.NoError(t, err)
	if resolver.name != "prod" ||
		credentials.Target != server.URL ||
		credentials.Token != "short-lived" {
		t.Fatalf("resolver name=%q credentials=%+v", resolver.name, credentials)
	}
}

func TestCapabilityAPIClientRejectsReplacedAndIncompatibleTargets(t *testing.T) {
	for _, test := range []struct {
		name       string
		apiVersion string
		instanceID string
		want       string
	}{
		{
			name:       "instance changed",
			apiVersion: "v1",
			instanceID: "lvinst_other",
			want:       "target identity changed",
		},
		{
			name:       "api changed",
			apiVersion: "v2",
			instanceID: "lvinst_prod",
			want:       "incompatible client/server API",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := authoringIdentityServerFor(
				t,
				test.instanceID,
				test.apiVersion,
			)
			defer server.Close()
			resolver := &fakeAuthoringResolver{profile: cliapi.TargetProfile{
				Origin:      server.URL,
				InstanceID:  "lvinst_prod",
				Environment: "production",
				ProjectID:   "analytics",
			}}
			_, err := (capabilityAPIClient{
				httpClient:        server.Client(),
				authoring:         resolver,
				validateAuthoring: true,
			}).Resolve(
				t.Context(),
				cliapi.Credentials{Target: "prod"},
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCapabilityAPIClientReportsUnreachableTargetBeforeAuthoring(t *testing.T) {
	_, err := (capabilityAPIClient{validateAuthoring: true}).Resolve(
		t.Context(),
		cliapi.Credentials{
			Target: "http://127.0.0.1:1",
			Token:  "ephemeral",
		},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "could not reach authoring target") {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestCapabilityAPIClientExchangesEphemeralWorkloadIdentity(t *testing.T) {
	t.Setenv("LEAPVIEW_API_TOKEN", "")
	t.Setenv("LEAPVIEW_WORKLOAD_CLIENT_ID", "sp-ci")
	t.Setenv("LEAPVIEW_WORKLOAD_CLIENT_SECRET", "service-secret")
	t.Setenv("LEAPVIEW_WORKLOAD_PROJECT", "analytics")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/instance":
			_, _ = w.Write([]byte(`{"id":"lvinst_prod","canonicalOrigin":"` + "http://" + r.Host + `","environment":"production"}`))
		case "/api/v1/capabilities":
			if r.Header.Get("Authorization") != "Bearer ephemeral-access" {
				t.Fatalf(
					"capabilities authorization = %q",
					r.Header.Get("Authorization"),
				)
			}
			_, _ = w.Write([]byte(`{
				"apiVersion":"v1","buildVersion":"test","buildRevision":"test",
				"buildTime":"2026-07-29T12:00:00Z","buildDirty":false,
				"buildDevelopment":false,"environment":"production",
				"authentication":["bearer"],"queryFormats":["application/json"],
				"uploadProtocols":[],
				"visualization":{"schemaVersion":3,"renderers":[]}
			}`))
		case "/oauth/token":
			if r.Header.Get("Authorization") != "" {
				t.Fatalf("workload exchange used authorization header %q", r.Header.Get("Authorization"))
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("grant_type") != "client_credentials" ||
				r.Form.Get("client_id") != "sp-ci" ||
				r.Form.Get("client_secret") != "service-secret" ||
				r.Form.Get("project_id") != "analytics" ||
				r.Form.Get("scope") != "RESOURCE_USE RESOURCE_READ RESOURCE_EDIT RESOURCE_PUBLISH" ||
				r.Form.Get("lifetime_seconds") != "900" {
				t.Fatalf("workload form = %v", r.Form)
			}
			_, _ = w.Write([]byte(`{
				"access_token":"ephemeral-access","token_type":"Bearer","expires_in":900,
				"session_id":"session-1","session_kind":"workload","target_id":"lvinst_prod",
				"project_id":"analytics","scope":"RESOURCE_USE RESOURCE_READ RESOURCE_EDIT RESOURCE_PUBLISH"
			}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	credentials, err := (capabilityAPIClient{
		httpClient:        server.Client(),
		validateAuthoring: true,
	}).Resolve(
		context.Background(),
		cliapi.Credentials{Target: server.URL},
	)
	require.NoError(t, err)
	if credentials.Target != server.URL || credentials.Token != "ephemeral-access" {
		t.Fatalf("credentials = %+v", credentials)
	}
}

func authoringIdentityServer(
	t *testing.T,
	apiVersion string,
) *httptest.Server {
	t.Helper()
	return authoringIdentityServerFor(t, "lvinst_prod", apiVersion)
}

func authoringIdentityServerFor(
	t *testing.T,
	instanceID,
	apiVersion string,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/v1/instance":
				_, _ = w.Write([]byte(`{
					"id":"` + instanceID + `",
					"canonicalOrigin":"http://` + r.Host + `",
					"environment":"production"
				}`))
			case "/api/v1/capabilities":
				if r.Header.Get("Authorization") != "Bearer short-lived" {
					t.Fatalf(
						"capabilities authorization = %q",
						r.Header.Get("Authorization"),
					)
				}
				_, _ = w.Write([]byte(`{
					"apiVersion":"` + apiVersion + `",
					"buildVersion":"test","buildRevision":"test",
					"buildTime":"2026-07-29T12:00:00Z",
					"buildDirty":false,"buildDevelopment":false,
					"environment":"production",
					"authentication":["bearer"],
					"queryFormats":["application/json"],
					"uploadProtocols":[],
					"visualization":{"schemaVersion":3,"renderers":[]}
				}`))
			default:
				t.Fatalf("unexpected path %q", r.URL.Path)
			}
		},
	))
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %#o, want %#o", path, got, want)
	}
}
