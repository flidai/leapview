package module

import (
	"context"
	"net/http"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/avatar"
	adminhttp "github.com/flidai/leapview/internal/admin/http"
	"github.com/flidai/leapview/internal/admin/personalsettings"
	"github.com/flidai/leapview/internal/admin/product"
	"github.com/flidai/leapview/internal/admin/productsettings"
	adminsettings "github.com/flidai/leapview/internal/admin/settings"
	adminstorage "github.com/flidai/leapview/internal/admin/storage"
	"github.com/flidai/leapview/internal/agent/api"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	dashboardapi "github.com/flidai/leapview/internal/dashboard/api"
	"github.com/flidai/leapview/internal/dashboard/publication"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/flidai/leapview/pkg/pagestream"
)

type PublicationService interface {
	PublicationsConfigured() bool
	AllPublications(context.Context) ([]publication.Publication, error)
	PublicationEvents(context.Context, string) ([]publication.Event, error)
	PublicationDTO(publication.Publication) dashboardapi.PublicationResponse
	MutatePublicationWithInvocation(context.Context, string, string, string, publication.Action, publication.CommandInvocation) (publication.Publication, error)
}

// Principal is the authenticated identity information needed by platform
// administration. Transport-specific principal representations stay private to
// their adapters.
type Principal struct {
	ID          string
	Email       string
	DisplayName string
	DevBypass   bool
}

// AccessReader is the read-only access contract consumed by administration.
// Mutations remain owned by access.
type AccessReader interface {
	ListPrincipals(context.Context, access.PrincipalFilter) ([]access.Principal, error)
	ListAllGroups(context.Context) ([]access.Group, error)
	ListGroupMembersByGroup(context.Context, string) ([]access.GroupMember, error)
}

type SettingsAccess interface {
	access.Repository
	adminsettings.ServiceAccountReader
	access.AuditedPrincipalPreferences
	personalsettings.IdentityManagementReader
}

type PersonalAvatar interface {
	Current(context.Context, string) (avatar.Metadata, error)
}

type AuthoringSessions interface {
	ListSessions(context.Context, string) ([]access.AuthoringSession, error)
	RevokeSession(context.Context, string, string) error
}

type QueryAuditReaderProvider func() (queryaudit.Reader, error)

type StorageConfig struct {
	// Runtime is the active serving-generation provider. Production callers
	// supply this so storage reads use the sealed PostgreSQL DuckLake catalog,
	// never a node-local catalog path.
	Runtime      projectruntime.Provider
	ControlPlane productsettings.Pinger
}

type Config struct {
	Access                       AccessReader
	AgentDetails                 func(context.Context) (api.AdminAgentResponse, error)
	QueryAuditReader             QueryAuditReaderProvider
	CSRFToken                    func(*http.Request) string
	CurrentPrincipal             func(*http.Request) (Principal, bool)
	CurrentCredential            func(*http.Request) (access.APICredential, bool)
	AuthorizeAnyProject          func(context.Context, string, *access.APICredential, access.Capability) (bool, error)
	Publications                 PublicationService
	AgentConfigCommand           uicommand.Binding
	PublicationCommands          map[string]uicommand.Binding
	AuthConfigured               bool
	LocalPasswordEnabled         bool
	AccessConfigured             bool
	Storage                      StorageConfig
	Layout                       func(*http.Request) webpage.Provider
	EnsureClientID               func(http.ResponseWriter, *http.Request) bool
	Broker                       *pagestream.Broker
	Product                      *product.Service
	ProductCommands              product.CommandExecutor
	ProductUICommands            productsettings.CommandContract
	ProductCommandFailure        product.CommandFailureWriter
	ProductStatus                product.Status
	SettingsAccess               SettingsAccess
	AuthorizationProjection      adminsettings.AuthorizationProjectionReader
	CurrentEffectiveCapabilities func(context.Context, string) ([]access.Capability, error)
	PersonalAvatar               PersonalAvatar
	AuthoringSessions            AuthoringSessions
	CurrentSession               func(*http.Request) (string, bool)
}

type Module struct {
	handler                      adminhttp.Handler
	access                       AccessReader
	currentPrincipal             func(*http.Request) (Principal, bool)
	currentCredential            func(*http.Request) (access.APICredential, bool)
	authorizeAnyProject          func(context.Context, string, *access.APICredential, access.Capability) (bool, error)
	currentEffectiveCapabilities func(context.Context, string) ([]access.Capability, error)
	publications                 PublicationService
	product                      *product.Handler
	publicationCommands          map[string]uicommand.Binding
	productCommands              productsettings.CommandContract
}

func Build(_ context.Context, config Config) (*Module, error) {
	m := &Module{
		access: config.Access, currentPrincipal: config.CurrentPrincipal,
		currentCredential: config.CurrentCredential, authorizeAnyProject: config.AuthorizeAnyProject,
		currentEffectiveCapabilities: config.CurrentEffectiveCapabilities,
		publications:                 config.Publications, publicationCommands: config.PublicationCommands, productCommands: config.ProductUICommands,
	}
	readModel := adminhttp.ReadModel{
		Access: config.Access, Avatars: config.PersonalAvatar, AgentDetails: config.AgentDetails,
		StorageService:   adminstorage.Service{Runtime: config.Storage.Runtime},
		QueryAuditReader: adminhttp.QueryAuditReaderProvider(config.QueryAuditReader), CSRFToken: config.CSRFToken,
		CurrentPrincipal: func(r *http.Request) (adminhttp.Principal, bool) {
			if config.CurrentPrincipal == nil {
				return adminhttp.Principal{}, false
			}
			principal, ok := config.CurrentPrincipal(r)
			return adminhttp.Principal{
				ID: principal.ID, Email: principal.Email, DisplayName: principal.DisplayName, DevBypass: principal.DevBypass,
			}, ok
		},
		CurrentEffectiveCapabilities: config.CurrentEffectiveCapabilities,
		Publications:                 m.adminPublications,
		AgentConfigCommand:           config.AgentConfigCommand,
		PublicationCommands:          config.PublicationCommands,
		ProductCommands:              config.ProductUICommands.Bindings,
		AuthConfigured:               config.AuthConfigured,
		AccessConfigured:             config.AccessConfigured,
	}
	m.handler = adminhttp.Handler{
		ReadModel: readModel, Layout: config.Layout,
		EnsureClientID: config.EnsureClientID, Broker: config.Broker,
		PublicationMutation:     m.mutatePublication,
		SettingsRepository:      config.SettingsAccess,
		AuthorizationProjection: config.AuthorizationProjection,
		CurrentCredential:       config.CurrentCredential,
	}
	if config.SettingsAccess != nil {
		personalService := &personalsettings.Service{
			Repository: config.SettingsAccess, IdentityManagement: config.SettingsAccess,
			Preferences: config.SettingsAccess,
			Avatar:      config.PersonalAvatar, Authoring: config.AuthoringSessions,
			CurrentEffectiveCapabilities: config.CurrentEffectiveCapabilities,
			LocalPasswordEnabled:         config.LocalPasswordEnabled,
		}
		m.handler.PersonalSettings = &personalsettings.Handler{
			Service: personalService, CurrentSession: config.CurrentSession,
			CurrentPrincipal: func(r *http.Request) (string, bool) {
				if config.CurrentPrincipal == nil {
					return "", false
				}
				principal, ok := config.CurrentPrincipal(r)
				return principal.ID, ok
			},
		}
	}
	if config.Product != nil {
		config.Product.ConfigureCommandExecutor(config.ProductCommands)
		settingsHandler, err := productsettings.NewHandler(productsettings.HTTPConfig{
			ReadModel: productsettings.ReadModel{Service: config.Product, Status: config.ProductStatus, ControlPlane: config.Storage.ControlPlane},
			CurrentPrincipal: func(r *http.Request) (product.Principal, bool) {
				if config.CurrentPrincipal == nil {
					return product.Principal{}, false
				}
				principal, ok := config.CurrentPrincipal(r)
				return product.Principal{ID: principal.ID}, ok
			},
			Commands: config.ProductUICommands,
		})
		if err != nil {
			return nil, err
		}
		m.handler.ProductSettings = settingsHandler
	}
	if config.Product != nil {
		var err error
		m.product, err = product.NewHandler(product.HTTPConfig{
			Service: config.Product, Status: config.ProductStatus,
			CommandFailure: config.ProductCommandFailure,
			CurrentPrincipal: func(r *http.Request) (product.Principal, bool) {
				if config.CurrentPrincipal == nil {
					return product.Principal{}, false
				}
				principal, ok := config.CurrentPrincipal(r)
				return product.Principal{ID: principal.ID}, ok
			},
		})
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Module) HTTP() adminhttp.Handler { return m.handler }

func RoleLabel(authConfigured bool, principal Principal, ok bool) string {
	if !authConfigured {
		return "Local platform"
	}
	if ok && principal.DevBypass {
		return "Platform admin"
	}
	return "Platform access"
}
