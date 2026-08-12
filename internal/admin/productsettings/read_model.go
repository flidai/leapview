// Package productsettings owns the page-stream projection for product and
// platform settings. It deliberately consumes the redacted product status
// assembled by the deployment layer; it does not inspect auth credentials.
package productsettings

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/admin/product"
	signals "github.com/flidai/leapview/internal/admin/ui/signals"
)

// Pinger is the small health dependency needed to render the runtime status.
// Keeping it narrow makes the projection usable with *sql.DB and test fakes.
type Pinger interface {
	PingContext(context.Context) error
}

type ReadModel struct {
	Service      *product.Service
	Status       product.Status
	ControlPlane Pinger
}

type Data struct {
	Identity  product.Identity
	Status    product.Status
	CanManage bool
	Active    string
}

func (m ReadModel) Data(ctx context.Context, active string, canManage bool) (Data, error) {
	if m.Service == nil {
		return Data{}, fmt.Errorf("product settings service is required")
	}
	identity, err := m.Service.Get(ctx)
	if err != nil {
		return Data{}, err
	}
	status := m.Status
	if status.System.ControlPlane == "" {
		status.System.ControlPlane = "unknown"
	}
	if m.ControlPlane != nil {
		status.System.ControlPlane = "available"
		if err := m.ControlPlane.PingContext(ctx); err != nil {
			status.System.ControlPlane = "unavailable"
		}
	}
	if active == "" {
		active = "general"
	}
	return Data{Identity: identity, Status: status, CanManage: canManage, Active: active}, nil
}

// Signal projects one complete, self-contained settings subtree. Keeping all
// three views in the signal lets the Lit component switch tabs without issuing
// ad hoc GET requests.
func Signal(data Data) signals.ProductSettingsSignal {
	status := data.Status
	identity := data.Identity
	general := signals.ProductGeneralSignal{
		DisplayName: identity.DisplayName, Revision: identity.Revision, UpdatedAt: identity.UpdatedAt,
		InstanceID: status.System.InstanceID, CanonicalOrigin: status.System.CanonicalOrigin,
		Environment: status.System.Environment,
	}
	if identity.Logo != nil {
		logo := identity.Logo
		general.Logo = &signals.ProductLogoSignal{
			URL: "/product/logo/" + logo.SHA256, Sha256: logo.SHA256,
			MediaType: logo.MediaType, SizeBytes: logo.SizeBytes, Width: int32(logo.Width), Height: int32(logo.Height),
		}
	}
	auth := status.Authentication
	managedBy := auth.ManagedBy
	if managedBy == "" {
		managedBy = "deployment"
	}
	availability := func(value product.Availability) signals.ProductAvailabilitySignal {
		return signals.ProductAvailabilitySignal{Available: value.Available, Enabled: value.Enabled}
	}
	named := func(value product.NamedAvailability) signals.ProductNamedAvailabilitySignal {
		return signals.ProductNamedAvailabilitySignal{Available: value.Available, Enabled: value.Enabled, Provider: optionalString(value.Provider)}
	}
	api := status.API
	system := status.System
	build := system.Build
	limits := system.Limits
	agent := system.Agent
	return signals.ProductSettingsSignal{
		Active: data.Active, CanManage: data.CanManage, General: general,
		Authentication: signals.ProductAuthenticationSignal{
			BrowserEnabled: auth.BrowserEnabled, APITokenOnly: auth.APITokenOnly,
			Local: availability(auth.Local), Oidc: named(auth.OIDC), Azure: availability(auth.Azure),
			Scim: availability(auth.SCIM), ManagedBy: managedBy,
		},
		API: signals.ProductAPIStatusSignal{
			BearerCredentials: availability(api.BearerCredentials), ServicePrincipals: availability(api.ServicePrincipals),
			Oauth: availability(api.OAuth), Mcp: availability(api.MCP), ExternalMcpIssuer: api.ExternalMCPIssuer,
		},
		System: signals.ProductSystemSignal{
			InstanceID: system.InstanceID, CanonicalOrigin: system.CanonicalOrigin, Environment: system.Environment,
			Build:          signals.ProductBuildSignal{Version: build.Version, Revision: build.Revision, BuildTime: build.BuildTime, Dirty: build.Dirty, Development: build.Development},
			StorageBackend: system.StorageBackend,
			Agent:          signals.ProductAgentSignal{Available: agent.Available, Configured: agent.Configured, Provider: optionalString(agent.Provider), ModelConfigured: agent.ModelConfigured},
			Limits: signals.ProductLimitsSignal{
				QueryResultMaxRows: int32(limits.QueryResultMaxRows), QueryResultMaxBytes: limits.QueryResultMaxBytes,
				ManagedDataMaxFiles: int32(limits.ManagedDataMaxFiles), ManagedDataMaxFileBytes: limits.ManagedDataMaxFileBytes,
				ManagedDataMaxRevisionBytes: limits.ManagedDataMaxRevisionBytes,
			},
			Runtime: signals.ProductRuntimeSignal{Health: runtimeHealth(system.ControlPlane), ControlPlane: system.ControlPlane, Environment: system.Environment},
		},
	}
}

// Payload keeps the generated model authoritative while preserving the
// explicit null required to remove a logo from Datastar's merge-patched state.
type productSettingsWire struct {
	signals.ProductSettingsSignal
	General productGeneralWire `json:"general"`
}

type productGeneralWire struct {
	signals.ProductGeneralSignal
	Logo *signals.ProductLogoSignal `json:"logo"`
}

func Payload(signal signals.ProductSettingsSignal) productSettingsWire {
	return productSettingsWire{
		ProductSettingsSignal: signal,
		General: productGeneralWire{
			ProductGeneralSignal: signal.General,
			Logo:                 signal.General.Logo,
		},
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func runtimeHealth(controlPlane string) string {
	switch controlPlane {
	case "available":
		return "healthy"
	case "unavailable":
		return "degraded"
	default:
		return "unknown"
	}
}
