package composectl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/cli/installationstate"
)

const firstInstallAppURL = "http://127.0.0.1:8080"

var privateBootstrapEnvironment = map[string]string{
	"COMPOSE_APP_BIND":     "127.0.0.1:8080",
	"CADDY_HTTP_BIND":      "127.0.0.1:80",
	"CADDY_HTTPS_BIND":     "127.0.0.1:443",
	"CADDY_HTTPS_UDP_BIND": "127.0.0.1:443",
}

// StartFirstInstallBootstrap starts the first installed image with only its
// loopback application port and, when enabled, a loopback Caddy route using
// Caddy's internal CA. The durable marker is written by hostinstall first.
func (c *Controller) StartFirstInstallBootstrap(ctx context.Context) error {
	return c.withLock(func() error {
		return c.startFirstInstallBootstrapAt(ctx, firstInstallAppURL)
	})
}

// ActivateFirstInstall is the explicit boundary between the private first
// install and the ordinary public proxy configuration. It requires actual
// application readiness before Compose can load the public Caddyfile.
func (c *Controller) ActivateFirstInstall(ctx context.Context) error {
	return c.withLock(func() error {
		return c.activateFirstInstallAt(ctx, firstInstallAppURL)
	})
}

func (c *Controller) startFirstInstallBootstrapAt(ctx context.Context, appURL string) error {
	marker, err := c.hostInstallationMarker()
	if err != nil {
		return err
	}
	return c.applyPrivateFirstInstallAt(ctx, marker, appURL)
}

func (c *Controller) applyPrivateFirstInstallAt(ctx context.Context, marker installationstate.Marker, appURL string) error {
	if marker.BootstrapPhase != installationstate.PhasePrivate {
		return errors.New("private first-install startup requires the private-bootstrap installation phase")
	}
	if err := validateMarkerHTTPS(c.root, marker); err != nil {
		return err
	}
	if marker.HTTPS != nil && *marker.HTTPS {
		// A pending marker always removes any Caddy container left by an
		// interrupted activation before it restarts the application.
		var caddy bytes.Buffer
		if err := c.composeForPhase(ctx, installationstate.PhasePrivate, nil, nil, &caddy, c.stderr,
			"ps", "--quiet", "caddy"); err != nil {
			return fmt.Errorf("inspect private first-install proxy: %w", err)
		}
		if strings.TrimSpace(caddy.String()) != "" {
			if err := c.composeForPhase(ctx, installationstate.PhasePrivate, nil, nil, c.stdout, c.stderr,
				"rm", "--stop", "--force", "caddy"); err != nil {
				return fmt.Errorf("remove prior first-install proxy configuration: %w", err)
			}
		}
	}
	if err := c.composeForPhase(ctx, installationstate.PhasePrivate, nil, nil, c.stdout, c.stderr,
		"up", "-d", "leapview"); err != nil {
		return fmt.Errorf("start first-install application privately: %w", err)
	}
	if err := waitForHostHTTPStatus(ctx, c.sleep, appURL+"/healthz", http.StatusOK); err != nil {
		return fmt.Errorf("first-install application did not become live: %w", err)
	}
	if marker.HTTPS != nil && *marker.HTTPS {
		if err := c.composeForPhase(ctx, installationstate.PhasePrivate, nil, nil, c.stdout, c.stderr,
			"up", "-d", "caddy"); err != nil {
			return fmt.Errorf("start first-install proxy privately: %w", err)
		}
	}
	return nil
}

func (c *Controller) activateFirstInstallAt(ctx context.Context, appURL string) error {
	marker, err := c.hostInstallationMarker()
	if err != nil {
		return err
	}
	if marker.BootstrapPhase != installationstate.PhasePrivate {
		return errors.New("first-install activation requires the private-bootstrap installation phase")
	}
	if err := validateMarkerHTTPS(c.root, marker); err != nil {
		return err
	}
	if err := requireLoopbackApplicationBind(c.root); err != nil {
		return err
	}
	status, err := hostHTTPStatus(ctx, appURL+"/readyz")
	if err != nil {
		return fmt.Errorf("probe first-install application readiness before activation: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("first-install readiness is HTTP %d; public activation requires HTTP 200", status)
	}

	activationErr := c.composeForPhase(ctx, installationstate.PhasePublic, nil, nil, c.stdout, c.stderr, "up", "-d")
	if activationErr == nil {
		activationErr = c.waitHealthy(ctx)
	}
	if activationErr == nil {
		status, err = hostHTTPStatus(ctx, appURL+"/readyz")
		if err != nil {
			activationErr = fmt.Errorf("verify readiness after public activation: %w", err)
		} else if status != http.StatusOK {
			activationErr = fmt.Errorf("readiness after public activation is HTTP %d, want 200", status)
		}
	}
	if activationErr != nil {
		rollbackErr := c.restorePrivateFirstInstall(marker, appURL)
		return errors.Join(fmt.Errorf("activate public first-install configuration: %w", activationErr), rollbackErr)
	}

	privateMarker := marker
	marker.BootstrapPhase = installationstate.PhasePublic
	if err := installationstate.WriteMarker(c.root, marker); err != nil {
		rollbackErr := c.restorePrivateFirstInstall(privateMarker, appURL)
		return errors.Join(fmt.Errorf("write public first-install phase: %w", err), rollbackErr)
	}
	_, err = fmt.Fprintln(c.stdout, "first-install public activation completed")
	return err
}

func (c *Controller) restorePrivateFirstInstall(marker installationstate.Marker, appURL string) error {
	marker.BootstrapPhase = installationstate.PhasePrivate
	markerWriteErr := installationstate.WriteMarker(c.root, marker)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.applyPrivateFirstInstallAt(ctx, marker, appURL); err != nil {
		return errors.Join(markerWriteErr, fmt.Errorf("restore private first-install configuration: %w", err))
	}
	return markerWriteErr
}

func (c *Controller) hostInstallationMarker() (installationstate.Marker, error) {
	image, err := c.ConfiguredImage()
	if err != nil {
		return installationstate.Marker{}, err
	}
	marker, err := installationstate.RequireForActiveHost(c.root, image)
	if err != nil {
		return installationstate.Marker{}, err
	}
	if marker.BootstrapPhase == "" {
		return installationstate.Marker{}, errors.New("first-install lifecycle requires an explicit host installation marker")
	}
	return marker, nil
}

func validateMarkerHTTPS(root string, marker installationstate.Marker) error {
	if marker.HTTPS == nil {
		return errors.New("host installation marker has no HTTPS selection")
	}
	value, err := envFileValue(root+"/"+deploymentEnvName, "COMPOSE_HTTPS")
	if err != nil {
		return err
	}
	want := "0"
	if *marker.HTTPS {
		want = "1"
	}
	if value != want {
		return fmt.Errorf("host installation HTTPS selection differs from Compose configuration")
	}
	return nil
}

func requireLoopbackApplicationBind(root string) error {
	value, err := envFileValue(root+"/"+deploymentEnvName, "COMPOSE_APP_BIND")
	if err != nil {
		return err
	}
	if value != privateBootstrapEnvironment["COMPOSE_APP_BIND"] {
		return errors.New("host application port must remain bound to 127.0.0.1:8080")
	}
	return nil
}

func waitForHostHTTPStatus(ctx context.Context, sleep func(context.Context, time.Duration) error, endpoint string, want int) error {
	var lastStatus int
	var lastErr error
	for attempt := 0; attempt < defaultHealthChecks; attempt++ {
		lastStatus, lastErr = hostHTTPStatus(ctx, endpoint)
		if lastErr == nil && lastStatus == want {
			return nil
		}
		if attempt+1 < defaultHealthChecks {
			if err := sleep(ctx, 2*time.Second); err != nil {
				return err
			}
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("endpoint returned HTTP %d, want %d", lastStatus, want)
}

func hostHTTPStatus(ctx context.Context, endpoint string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}
