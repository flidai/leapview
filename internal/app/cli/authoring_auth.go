package cli

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

	accesscli "github.com/flidai/leapview/internal/access/cli"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/platform/securestore"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
)

const authoringCredentialService = "com.leapview.cli.authoring.v1"

type applicationAuthoringAuthentication struct{}

func (applicationAuthoringAuthentication) Login(ctx context.Context, request accesscli.LoginRequest, notify func(accesscli.DeviceChallenge)) (accesscli.LoginResult, error) {
	authentication, err := defaultAuthoringAuthenticator(http.DefaultClient)
	if err != nil {
		return accesscli.LoginResult{}, err
	}
	return authentication.Login(ctx, request, notify)
}

func (applicationAuthoringAuthentication) Logout(ctx context.Context, name string) error {
	authentication, err := defaultAuthoringAuthenticator(http.DefaultClient)
	if err != nil {
		return err
	}
	return authentication.Logout(ctx, name)
}

func defaultAuthoringAuthenticator(client *http.Client) (*accesscli.Authenticator, error) {
	secrets, err := securestore.NewNative(authoringCredentialService)
	if err != nil {
		return nil, err
	}
	return &accesscli.Authenticator{
		OAuth:       accesscli.StandardOAuthClient{HTTPClient: client},
		Profiles:    cliapi.NewProfileStore(clientConfigPath()),
		Secrets:     secrets,
		OpenBrowser: openSystemBrowser,
	}, nil
}

type applicationTargetDiscovery struct{}

func (applicationTargetDiscovery) Discover(ctx context.Context, target string) (accesscli.TargetMetadata, error) {
	instance, err := newDeploymentCLIClient(http.DefaultClient, target, "").instance(ctx)
	if err != nil {
		return accesscli.TargetMetadata{}, err
	}
	if strings.TrimSpace(instance.Id) == "" || strings.TrimSpace(instance.CanonicalOrigin) == "" ||
		strings.TrimSpace(instance.Environment) == "" {
		return accesscli.TargetMetadata{}, fmt.Errorf("target returned incomplete instance identity")
	}
	return accesscli.TargetMetadata{
		Origin: strings.TrimRight(instance.CanonicalOrigin, "/"), InstanceID: instance.Id, Environment: instance.Environment,
	}, nil
}

type applicationProjectIdentity struct{}

func (applicationProjectIdentity) ProjectID(path string) (string, error) {
	project, err := projectcompiler.LoadProject(path)
	if err != nil {
		return "", err
	}
	// Authoring credentials are scoped to the immutable graph root. The
	// metadata name is only an executable-facing label and must not be used as
	// the server-bound project assertion.
	return project.ID.String(), nil
}

func openSystemBrowser(uri string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", uri)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", uri)
	default:
		command = exec.Command("xdg-open", uri)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
