package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/adminpostgres"
	"github.com/flidai/leapview/internal/app/config"
	postgresauthority "github.com/flidai/leapview/internal/app/postgresauthority"
	extensionfixture "github.com/flidai/leapview/internal/app/testing/extensionfixture"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
)

// This fixture starts the real authenticated development assembly for its
// sanctioned first-source profile bootstrap, then restarts production with
// no environment credential. Native compiler/build/publication and runtime
// components are never replaced by test implementations.
type sourceCredentialHTTPJourney struct {
	config         config.Config
	graph          *postgresauthority.PostgresAuthorityGraph
	target         *Application
	initial        adminoffline.InitialCredentials
	instance       string
	authoringToken string
	source         sourceCredentialUpstream
	snapshot       projectdevloop.Snapshot
	control        *postgrestest.Database
	production     bool
}

func newSourceCredentialHTTPJourney(t *testing.T) *sourceCredentialHTTPJourney {
	return newSourceCredentialHTTPJourneyProfile(t, false)
}

func newSourceCredentialHTTPJourneyProfile(t *testing.T, production bool) *sourceCredentialHTTPJourney {
	t.Helper()
	h := postgrestest.StartTLS(t)
	roles := provisionPostgresOnboardingRoles(t, h)
	source := newSourceCredentialUpstream(t, h)
	snapshot := sourceJourneySnapshot(t)
	control := h.NewDatabase(t, "leapview_control")
	catalog := h.NewDatabase(t, ducklakepostgres.DefaultDuckLakeDatabase)
	grantPostgresOnboardingDatabases(t, h, control, catalog, roles)
	extensions := extensionfixture.New(t, "ducklake", "postgres")
	cfg := postgresOnboardingConfig(t, h, control, catalog, roles, extensions)
	if production {
		cfg.LocalAuth = true
		cfg.APITokenOnlyAuth = false
	}
	if !production {
		cfg.DevelopmentCredentialVariables = sourceJourneyCredentialVariable
		cfg.RequireActiveDeployment = true
		cfg.LocalCheckoutID = "checkout:credential-journey"
		cfg.LocalOwnerID = "runtime:credential-journey"
		cfg.DevelopmentProfileName = "credential-journey"
		cfg.DevelopmentGraphDigest = snapshot.ConnectionCatalogDigest
		cfg.DevelopmentProfileDigest = source.profileDigest(t, cfg.DevelopmentProfileName)
		credentialJSON, err := json.Marshal(map[string]string{"password": source.password})
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(sourceJourneyCredentialVariable, string(credentialJSON))
	}
	cfg.RequireActiveDeployment = true
	operations := adminpostgres.New(adminpostgres.Dependencies{LoadConfig: func() (config.Config, error) { return cfg, nil }})
	var output bytes.Buffer
	if err := operations.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &output); err != nil {
		t.Fatal(err)
	}
	initial, err := adminoffline.DecodeInitialCredentials(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err = operations.AcknowledgeInitialCredentials(t.Context()); err != nil {
		t.Fatal(err)
	}

	tuple := sourceJourneyCompatibility(t, extensions)
	evidence, err := ducklake.RunLocalPoolConformance(t.Context(), filepath.Join(cfg.HomeDir, "conformance"), tuple, extensions.Admission)
	if err != nil {
		t.Fatal(err)
	}
	identity := physicalpool.PoolIdentity{
		StorageLocation: filepath.Join(cfg.HomeDir, "physical-pools"), StorageNamespace: "delivery", Region: "local", Tenant: "production",
		EncryptionDomain: "production", IsolationBoundary: "production", RetentionAuthority: "production",
		RetentionPolicy: physicalpool.RetentionPolicy{OrphanGracePeriodSeconds: 3600, ReaderGracePeriodSeconds: 300, BuildGracePeriodSeconds: 60}, Compatibility: tuple,
	}
	if err = operations.BootstrapPhysicalPool(t.Context(), adminoffline.PhysicalPoolBootstrapRequest{Pool: identity, Evidence: evidence, Apply: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	pool, err := physicalpool.NewPhysicalPool(identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DeliveryPhysicalPoolID = pool.ID.String()
	cfg.DeliveryPhysicalPoolCompatibilityDigest, err = tuple.Digest()
	if err != nil {
		t.Fatal(err)
	}

	runtimePool, err := platformpostgres.Open(t.Context(), cfg.PostgresControlPlaneConfig().Runtime)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimePool.Close)
	maintenancePool, err := platformpostgres.Open(t.Context(), cfg.PostgresControlMaintenanceConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenancePool.Close)
	instance, err := postgresauthority.ResolveInstanceIdentity(t.Context(), runtimePool, cfg.Environment)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := postgresauthority.NewPostgresAuthorityGraph(runtimePool, maintenancePool, postgresauthority.PostgresAuthorityGraphOptions{TargetID: instance, FingerprintKey: []byte(cfg.TokenHashKey)})
	if err != nil {
		t.Fatal(err)
	}
	setupPostgresOnboardingCustomerCredentials(t, &cfg)
	journey := &sourceCredentialHTTPJourney{config: cfg, graph: graph, initial: initial, instance: instance, source: source, snapshot: snapshot, control: control, production: production}
	journey.start(t)
	t.Cleanup(func() {
		if journey.target != nil {
			_ = journey.target.Shutdown(context.Background())
		}
	})
	return journey
}

func (f *sourceCredentialHTTPJourney) start(t *testing.T) {
	t.Helper()
	build := BuildDevelopment
	if f.production {
		build = BuildProduction
	}
	target, err := build(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err = target.Start(t.Context()); err != nil {
		_ = target.Shutdown(context.Background())
		t.Fatal(err)
	}
	f.target = target
}

func (f *sourceCredentialHTTPJourney) request(t *testing.T, method, path, token string, body any, expected int) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Host = "localhost"
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	f.target.Handler().ServeHTTP(response, request)
	if response.Code != expected {
		// Responses may contain one-time credentials; report only fixed status.
		t.Fatalf("%s %s: status=%d want=%d", method, path, response.Code, expected)
	}
	return response
}

func TestPostgresSourceCredentialHTTPJourney(t *testing.T) {
	f := newSourceCredentialHTTPJourney(t)
	response := f.request(t, http.MethodGet, "/readyz", "", nil, http.StatusServiceUnavailable)
	if !strings.Contains(response.Body.String(), `"runtime":"no_active_deployments"`) {
		t.Fatalf("fresh target readiness: %s", response.Body.String())
	}
	token := f.bootstrapProject(t)
	f.createSourceBinding(t, f.authoringToken)
	initial := f.publishSource(t, f.authoringToken)
	f.request(t, http.MethodPost, "/api/v1/projects/"+sourceJourneyProject+"/targets/"+f.instance+"/connection-bindings/connection:warehouse/credential-drafts", f.authoringToken, map[string]any{"fields": map[string]string{"password": "attenuated-request-must-not-be-saved"}}, http.StatusForbidden)
	beforeSnapshot := f.querySource(t, token, "30")
	f.source.rotatePasswordAndData(t, "source-journey-rotated-password")
	activated := f.activateSourceCredential(t, token)
	if *activated.GenerationId == initial.GenerationId {
		t.Fatal("credential activation did not publish its own native generation")
	}
	activatedSnapshot := f.querySource(t, token, "40")
	if activatedSnapshot == beforeSnapshot {
		t.Fatal("credential rebuild query retained the predecessor serving snapshot")
	}
	f.restartWithoutEnvironment(t, false)
	f.retryCompletedSource(t, token, activated)
	if f.querySource(t, token, "40") != activatedSnapshot {
		t.Fatal("restart query did not retain the committed serving snapshot")
	}
	// Production has a stricter probe policy. A stale development receipt must
	// not acknowledge that new policy, even while the committed runtime serves.
	f.restartWithoutEnvironment(t, true)
	f.request(t, http.MethodPost, "/api/v1/projects/"+sourceJourneyProject+"/targets/"+f.instance+"/connection-bindings/connection:warehouse/credential-activations/"+activated.OperationId+"/retry", token, map[string]any{}, http.StatusConflict)
	f.request(t, http.MethodGet, "/readyz", "", nil, http.StatusOK)
	if f.querySource(t, token, "40") != activatedSnapshot {
		t.Fatal("stale-policy retry disturbed the current production snapshot")
	}
	// Rotate again under actual production authority and outbound probe policy,
	// then prove exact completed-operation recovery across a production restart.
	f.source.rotatePasswordAndData(t, "source-journey-production-password")
	productionActivation := f.activateSourceCredential(t, token)
	productionSnapshot := f.querySource(t, token, "50")
	if productionSnapshot == activatedSnapshot {
		t.Fatal("production credential rebuild retained its predecessor snapshot")
	}
	f.restartWithoutEnvironment(t, true)
	f.retryCompletedSource(t, token, productionActivation)
	if f.querySource(t, token, "50") != productionSnapshot {
		t.Fatal("production restart query lost the exact committed snapshot")
	}
}
