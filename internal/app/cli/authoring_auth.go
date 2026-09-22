package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

	accesscli "github.com/flidai/leapview/internal/access/cli"
	"github.com/flidai/leapview/internal/app/cli/localruntime"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/platform/securestore"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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

func resolveLocalProjectAuthority() (localruntime.ProjectAuthority, error) {
	authority, err := cliapi.NewProfileStore(clientConfigPath()).ResolveProjectAuthority("", validateProjectAuthorityResourceID)
	if err != nil {
		return localruntime.ProjectAuthority{}, err
	}
	return localruntime.ProjectAuthority{IssuerID: authority.IssuerID, ProjectUID: authority.ProjectUID}, nil
}

func establishLocalAuthoringSessions(ctx context.Context, request localruntime.SessionRequest, out io.Writer) (localruntime.SessionResult, error) {
	authenticator, err := defaultAuthoringAuthenticator(http.DefaultClient)
	if err != nil {
		return localruntime.SessionResult{}, err
	}
	return establishLocalAuthoringSessionsWith(ctx, localSessionAuthority{Authenticator: authenticator}, request, out)
}

func resetLocalAuthoringSessions(ctx context.Context, request localruntime.SessionRequest) error {
	authenticator, err := defaultAuthoringAuthenticator(http.DefaultClient)
	if err != nil {
		return err
	}
	return resetLocalAuthoringSessionsWith(ctx, localSessionAuthority{Authenticator: authenticator}, request)
}

type localSessionResetAuthority interface {
	Profile(string) (cliapi.TargetProfile, error)
	DeleteProfile(string, cliapi.TargetProfile) error
	DeleteCredential(context.Context, string) error
}

func (authority localSessionAuthority) DeleteProfile(name string, expected cliapi.TargetProfile) error {
	return authority.Profiles.DeleteIfMatch(name, expected)
}

func (authority localSessionAuthority) DeleteCredential(ctx context.Context, account string) error {
	return authority.Secrets.Delete(ctx, account)
}

func resetLocalAuthoringSessionsWith(ctx context.Context, authority localSessionResetAuthority, request localruntime.SessionRequest) error {
	if authority == nil {
		return errors.New("local authoring session reset authority is required")
	}
	profile, err := authority.Profile(request.TargetName)
	if errors.Is(err, cliapi.ErrProfileNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if profile.Origin != request.Origin || profile.InstanceID != request.InstanceID ||
		profile.Environment != request.Environment || profile.ProjectID != request.ProjectID {
		return fmt.Errorf("local authoring target %q changed before reset; refusing credential removal", request.TargetName)
	}
	if err := authority.DeleteCredential(ctx, profile.CredentialAccount); err != nil && !errors.Is(err, securestore.ErrNotFound) {
		return fmt.Errorf("remove local authoring credential: %w", err)
	}
	// Remove the credential first so a transient keychain failure leaves the
	// exact profile available for an idempotent retry. DeleteIfMatch then keeps
	// a concurrent profile replacement from being removed.
	if err := authority.DeleteProfile(request.TargetName, profile); err != nil {
		return err
	}
	return nil
}

type localSessionAuthentication interface {
	Profile(string) (cliapi.TargetProfile, error)
	RebindLoopbackOrigin(string, cliapi.TargetProfile, string) error
	Resolve(context.Context, string) (accesscli.ResolvedCredential, error)
	Login(context.Context, accesscli.LoginRequest, func(accesscli.DeviceChallenge)) (accesscli.LoginResult, error)
}

type localSessionAuthority struct{ *accesscli.Authenticator }

func (authority localSessionAuthority) Profile(name string) (cliapi.TargetProfile, error) {
	return authority.Profiles.Get(name)
}

func (authority localSessionAuthority) RebindLoopbackOrigin(name string, expected cliapi.TargetProfile, origin string) error {
	return authority.Profiles.RebindLoopbackOrigin(name, expected, origin)
}

func establishLocalAuthoringSessionsWith(ctx context.Context, authenticator localSessionAuthentication, request localruntime.SessionRequest, out io.Writer) (localruntime.SessionResult, error) {
	if authenticator == nil {
		return localruntime.SessionResult{}, errors.New("local authoring session authority is required")
	}
	if existing, profileErr := authenticator.Profile(request.TargetName); profileErr == nil {
		if existing.InstanceID != request.InstanceID || existing.Environment != request.Environment || existing.ProjectID != request.ProjectID {
			return localruntime.SessionResult{}, fmt.Errorf("local authoring target %q identifies a different runtime; log it out before replacement", request.TargetName)
		}
		if existing.Origin != request.Origin {
			if err := authenticator.RebindLoopbackOrigin(request.TargetName, existing, request.Origin); err != nil {
				return localruntime.SessionResult{}, fmt.Errorf("rebind local authoring target after port change: %w", err)
			}
		}
		if resolved, resolveErr := authenticator.Resolve(ctx, request.TargetName); resolveErr == nil {
			return localruntime.SessionResult{TargetName: request.TargetName, SessionID: resolved.SessionID}, nil
		}
	} else if !errors.Is(profileErr, cliapi.ErrProfileNotFound) {
		return localruntime.SessionResult{}, profileErr
	}
	result, err := authenticator.Login(ctx, accesscli.LoginRequest{
		Name: request.TargetName, Origin: request.Origin, InstanceID: request.InstanceID,
		Environment: request.Environment, ProjectID: request.ProjectID,
		Capabilities: []string{"PROJECT_ADMIN", "RESOURCE_USE", "RESOURCE_READ", "RESOURCE_EDIT", "RESOURCE_PUBLISH", "RESOURCE_MANAGE"},
	}, func(challenge accesscli.DeviceChallenge) {
		fmt.Fprintf(out, "Open %s and enter code %s\n", challenge.VerificationURI, challenge.UserCode)
	})
	if err != nil {
		return localruntime.SessionResult{}, err
	}
	return localruntime.SessionResult{TargetName: request.TargetName, SessionID: result.SessionID}, nil
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

type applicationProjectIdentity struct {
	profiles *cliapi.ProfileStore
}

func (identity applicationProjectIdentity) ProjectID(externallyIssuedUID string) (string, error) {
	if identity.profiles == nil {
		return "", fmt.Errorf("project authority state is unavailable")
	}
	authority, err := identity.profiles.ResolveProjectAuthority(externallyIssuedUID, validateProjectAuthorityResourceID)
	if err != nil {
		return "", err
	}
	return authority.ProjectUID, nil
}

func validateProjectAuthorityResourceID(value string) error {
	_, err := projectgraph.NewResourceID(value)
	return err
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
