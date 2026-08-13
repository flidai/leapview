package app

import (
	"strings"

	adminmodule "github.com/flidai/leapview/internal/admin/module"
	appconfig "github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/platform/buildinfo"
)

func productAdministrationStatus(config appconfig.Config, instanceID, publicURL, environment string, build buildinfo.Identity) adminmodule.ProductStatus {
	authentication := adminmodule.ProductAuthenticationStatus{
		BrowserEnabled: !config.APITokenOnlyAuth,
		APITokenOnly:   config.APITokenOnlyAuth,
		Local:          adminmodule.ProductAvailability{Available: true, Enabled: config.LocalAuth},
		OIDC: adminmodule.ProductNamedAvailability{
			Available: true, Enabled: config.OIDCConfigured(), Provider: enabledLabel(config.OIDCConfigured(), config.OIDCProviderID),
		},
		Azure:     adminmodule.ProductAvailability{Available: true, Enabled: config.AzureConfigured()},
		SCIM:      adminmodule.ProductAvailability{Available: true, Enabled: strings.TrimSpace(config.SCIMBearerToken) != ""},
		ManagedBy: "deployment",
	}
	agentConfigured := strings.TrimSpace(config.AgentAPIKey) != ""
	storageBackend := strings.TrimSpace(config.ManagedDataBackend)
	if storageBackend == "" {
		storageBackend = "local"
	}
	return adminmodule.ProductStatus{
		Authentication: authentication,
		API: adminmodule.ProductAPIStatus{
			BearerCredentials: adminmodule.ProductAvailability{Available: true, Enabled: true},
			ServicePrincipals: adminmodule.ProductAvailability{Available: true, Enabled: true},
			OAuth:             adminmodule.ProductAvailability{Available: true, Enabled: true},
			MCP:               adminmodule.ProductAvailability{Available: true, Enabled: true},
			ExternalMCPIssuer: strings.TrimSpace(config.MCPOAuthIssuerURL) != "",
		},
		System: adminmodule.ProductSystemStatus{
			InstanceID: instanceID, CanonicalOrigin: publicURL, Environment: environment, Build: build,
			StorageBackend: storageBackend,
			Agent: adminmodule.ProductAgentStatus{
				Available: true, Configured: agentConfigured, Provider: enabledLabel(agentConfigured, "openai-compatible"),
				ModelConfigured: strings.TrimSpace(config.AgentModel) != "",
			},
			Limits: adminmodule.ProductLimits{
				QueryResultMaxRows: config.QueryResultMaxRows, QueryResultMaxBytes: config.QueryResultMaxBytes,
				ManagedDataMaxFiles: config.ManagedDataMaxFiles, ManagedDataMaxFileBytes: config.ManagedDataMaxFileBytes,
				ManagedDataMaxRevisionBytes: config.ManagedDataMaxRevisionBytes,
			},
		},
	}
}

func enabledLabel(enabled bool, value string) string {
	if !enabled {
		return ""
	}
	return strings.TrimSpace(value)
}
