package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

// projectCatalogLeaseProvider narrows the runtime-host provider to the
// catalog lease contract while preserving the exact active lease object. No
// graph or authorization snapshot is cached here.
type projectCatalogLeaseProvider struct {
	provider runtimehostmodule.Provider
}

type projectCatalogSubjectResolver struct {
	resolve func(context.Context, string) ([]access.SubjectRef, error)
}

type deliveryTargetReader interface {
	DeliveryTargetRevision(context.Context, string) (deployment.DeliveryTarget, error)
}

func requestLocalDevelopmentAuthorization(ctx context.Context, actorID string) bool {
	principal, ok := accessmodule.PrincipalFromContext(ctx)
	return ok && principal.DevBypass && strings.TrimSpace(principal.ID) == strings.TrimSpace(actorID)
}

func (r projectCatalogSubjectResolver) AuthorizationSubjects(ctx context.Context, principalID string) ([]access.SubjectRef, error) {
	if r.resolve == nil {
		return nil, projectcatalog.ErrUnavailable
	}
	return r.resolve(ctx, principalID)
}

func (p projectCatalogLeaseProvider) Acquire(ctx context.Context) (projectcatalog.Lease, error) {
	if p.provider == nil {
		return nil, projectcatalog.ErrUnavailable
	}
	lease, err := p.provider.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	catalogLease, ok := lease.(projectcatalog.Lease)
	if !ok {
		lease.Release()
		return nil, fmt.Errorf("runtime lease does not expose catalog authorization snapshot")
	}
	return catalogLease, nil
}

func requiresDeliveryApproval(operation deployment.DeliveryOperationKind) bool {
	// Restatement is the native implementation of an authorized operational
	// refresh. Requiring a separate deployment approval for every scheduled or
	// manually requested refresh would leave the refresh dispatcher unable to
	// complete. Publication still crosses the live RBAC authorization boundary
	// and the exact target/seal CAS; only the code/policy change approval is not
	// applicable to this data-only operation.
	return operation != deployment.DeliveryOperationRestatement
}

func readClaimedProject(repository deploymentmodule.ProjectClaimReader, environment servingstatemodule.Environment) func(context.Context) (projectgraph.ResourceID, bool, error) {
	return func(ctx context.Context) (projectgraph.ResourceID, bool, error) {
		if repository == nil {
			return "", false, errors.New("project claim repository is required")
		}
		claim, err := repository.GetProjectClaim(ctx)
		if errors.Is(err, deployment.ErrProjectClaimNotFound) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("read claimed project: %w", err)
		}
		if claim.Environment != environment {
			return "", false, fmt.Errorf("claimed project environment %q does not match configured environment %q", claim.Environment, environment)
		}
		return claim.ProjectID, true, nil
	}
}

type claimedProjectBinder interface {
	BindClaimedProject(projectgraph.ResourceID, servingstatemodule.Environment) error
}

func bindClaimedProject(runtimeHost claimedProjectBinder, environment servingstatemodule.Environment) func(context.Context, projectgraph.ResourceID, servingstatemodule.Environment) error {
	return func(_ context.Context, projectID projectgraph.ResourceID, claimedEnvironment servingstatemodule.Environment) error {
		if claimedEnvironment != environment {
			return fmt.Errorf("claimed project environment %q does not match configured environment %q", claimedEnvironment, environment)
		}
		if runtimeHost == nil {
			return errors.New("runtime host is unavailable")
		}
		return runtimeHost.BindClaimedProject(projectID, claimedEnvironment)
	}
}

func firstConfigured(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func configuredListenURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}

func accessAuthConfig(cfg config.Config, production, cookieSecure bool) accessmodule.AuthConfig {
	if !production {
		return accessmodule.AuthConfig{DevBypass: true, DevAPIToken: cfg.DevAPIToken, CSRFKey: cfg.CSRFKey}
	}
	providers := []accessmodule.OIDCProviderConfig{}
	if cfg.OIDCConfigured() {
		providers = append(providers, accessmodule.OIDCProviderConfig{
			ID: cfg.OIDCProviderID, IssuerURL: cfg.OIDCIssuerURL, ClientID: cfg.OIDCClientID,
			ClientSecret: cfg.OIDCSecret, RedirectURL: cfg.OIDCCallbackURL, Scopes: cfg.OIDCScopesList(),
		})
	}
	return accessmodule.AuthConfig{
		DevBypass: cfg.DevAuthBypass, DevAPIToken: cfg.DevAPIToken, APITokenOnly: cfg.APITokenOnlyAuth,
		LocalAuth: cfg.LocalAuth, AzureClientID: cfg.AzureClientID, AzureSecret: cfg.AzureSecret,
		AzureCallback: cfg.AzureCallbackURL, AzureTenant: cfg.AzureTenant, CSRFKey: cfg.CSRFKey,
		CookieSecure: cookieSecure, BootstrapTenant: cfg.AzureTenant, OIDCProviders: providers,
	}
}
