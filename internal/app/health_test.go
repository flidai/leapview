package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestReadyzDoesNotExposeDependencyErrors(t *testing.T) {
	const secret = "postgres://operator:super-secret@db.internal/platform?token=private"
	projectID := projectgraph.ResourceID("project:customer-secret")
	tests := []struct {
		name      string
		config    healthConfig
		wantBody  string
		forbidden []string
	}{
		{
			name:     "platform",
			config:   healthConfig{Platform: func(context.Context) error { return errors.New(secret) }},
			wantBody: `{"checks":{"platformStore":"failed"},"status":"not_ready"}` + "\n",
		},
		{
			name: "analytics",
			config: healthConfig{
				Platform:  func(context.Context) error { return nil },
				Analytics: func() error { return errors.New("analytics catalog at /var/lib/private: " + secret) },
			},
			wantBody: `{"checks":{"analytics":"failed","platformStore":"ok"},"status":"not_ready"}` + "\n",
		},
		{
			name: "runtime lease",
			config: healthConfig{
				Platform:          func(context.Context) error { return nil },
				RuntimeLeaseReady: func(context.Context) error { return errors.New("lease backend: " + secret) },
			},
			wantBody: `{"checks":{"platformStore":"ok","runtimeLease":"failed"},"status":"not_ready"}` + "\n",
		},
		{
			name: "custom check",
			config: healthConfig{
				Platform: func(context.Context) error { return nil },
				Checks: map[string]func(context.Context) error{
					"mapAssets": func(context.Context) error { return errors.New("s3://private-bucket?" + secret) },
				},
			},
			wantBody: `{"checks":{"mapAssets":"failed","platformStore":"ok"},"status":"not_ready"}` + "\n",
		},
		{
			name: "active project lookup",
			config: healthConfig{
				Platform:        func(context.Context) error { return nil },
				ActiveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "", errors.New(secret) },
				RuntimeReady:    func(context.Context) error { return nil },
			},
			wantBody: `{"checks":{"platformStore":"ok","runtime":"failed"},"status":"not_ready"}` + "\n",
		},
		{
			name: "project runtime",
			config: healthConfig{
				Platform:        func(context.Context) error { return nil },
				ActiveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
				RuntimeReady: func(context.Context) error {
					return errors.New("runtime path /private/" + projectID.String() + ": " + secret)
				},
			},
			wantBody:  `{"checks":{"platformStore":"ok","runtime":"failed"},"status":"not_ready"}` + "\n",
			forbidden: []string{projectID.String()},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			newHealth(test.config).Readyz(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
			if got := response.Body.String(); got != test.wantBody {
				t.Fatalf("body = %q, want %q", got, test.wantBody)
			}
			for _, forbidden := range append([]string{secret, "super-secret", "db.internal", "/var/lib/private", "private-bucket"}, test.forbidden...) {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("response exposed %q: %s", forbidden, response.Body.String())
				}
			}
		})
	}
}

func TestReadyzExposesOnlyReviewedDeliveryStartupCodes(t *testing.T) {
	deliveryErr := deployment.ValidateDeliveryStartup(deployment.DeliveryStartupState{
		TargetID:   "customer-secret-target",
		Production: true,
	})
	response := httptest.NewRecorder()
	newHealth(healthConfig{
		Platform: func(context.Context) error { return nil },
		Checks: map[string]func(context.Context) error{
			"deliveryStartup": func(context.Context) error { return deliveryErr },
		},
	}).Readyz(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	want := `{"checks":{"deliveryStartup":"missing_physical_pool_admission,target_revision_missing","platformStore":"ok"},"status":"not_ready"}` + "\n"
	if got := response.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if strings.Contains(response.Body.String(), "customer-secret-target") || strings.Contains(response.Body.String(), "delivery startup is not ready") {
		t.Fatalf("response exposed delivery error context: %s", response.Body.String())
	}
}

func TestReadyzUsesStableRuntimeKeyWhenReady(t *testing.T) {
	response := httptest.NewRecorder()
	newHealth(healthConfig{
		Platform:        func(context.Context) error { return nil },
		ActiveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:internal-name", nil },
		RuntimeReady:    func(context.Context) error { return nil },
	}).Readyz(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	want := `{"checks":{"platformStore":"ok","runtime":"ok"},"status":"ready"}` + "\n"
	if got := response.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if strings.Contains(response.Body.String(), "internal-name") {
		t.Fatalf("response exposed project identity: %s", response.Body.String())
	}
}

func TestReadyzAllowsFreshTargetWithoutRequiredDeployment(t *testing.T) {
	response := httptest.NewRecorder()
	newHealth(healthConfig{
		Platform:                func(context.Context) error { return nil },
		ActiveProjectID:         func(context.Context) (projectgraph.ResourceID, error) { return "", nil },
		RuntimeReady:            func(context.Context) error { return errors.New("must not run without an active project") },
		RequireActiveDeployment: false,
	}).Readyz(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	want := `{"checks":{"platformStore":"ok","runtime":"no_active_deployments"},"status":"ready"}` + "\n"
	if got := response.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestReadyzRejectsMalformedNonemptyActiveProject(t *testing.T) {
	response := httptest.NewRecorder()
	newHealth(healthConfig{
		Platform:                func(context.Context) error { return nil },
		ActiveProjectID:         func(context.Context) (projectgraph.ResourceID, error) { return "invalid project", nil },
		RuntimeReady:            func(context.Context) error { return nil },
		RequireActiveDeployment: false,
	}).Readyz(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	want := `{"checks":{"platformStore":"ok","runtime":"failed"},"status":"not_ready"}` + "\n"
	if got := response.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}
