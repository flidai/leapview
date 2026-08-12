package module

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	adminhttp "github.com/flidai/leapview/internal/admin/http"
	adminstorage "github.com/flidai/leapview/internal/admin/storage"
	"github.com/flidai/leapview/internal/agent/api"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	"github.com/flidai/leapview/internal/analytics/resource"
	dashboardapi "github.com/flidai/leapview/internal/dashboard/api"
	"github.com/flidai/leapview/internal/dashboard/publication"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/flidai/leapview/internal/workload"
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
	ListRoles(context.Context) ([]access.Role, error)
	ListAllRoleBindings(context.Context) ([]access.RoleBinding, error)
	Authorize(context.Context, string, access.Privilege, access.ObjectRef) (access.AuthorizationDecision, error)
}

type QueryAuditReaderProvider func() (queryaudit.Reader, error)

type StorageConfig struct {
	CatalogPath  string
	DataPath     string
	Environment  string
	ControlPlane *sql.DB
	Analytics    interface {
		resource.Provider
		resource.SessionProvider
	}
	Admitter workload.Admitter
}

type Config struct {
	Access                AccessReader
	AgentDetails          func(context.Context) (api.AdminAgentResponse, error)
	QueryAuditReader      QueryAuditReaderProvider
	CSRFToken             func(*http.Request) string
	CurrentPrincipal      func(*http.Request) (Principal, bool)
	CurrentCredential     func(*http.Request) (access.APICredential, bool)
	AuthorizeAnyWorkspace func(context.Context, string, *access.APICredential, access.Privilege) (bool, error)
	Publications          PublicationService
	AgentConfigCommand    uicommand.Binding
	PublicationCommands   map[string]uicommand.Binding
	DefaultWorkspaceID    string
	AuthConfigured        bool
	AccessConfigured      bool
	Storage               StorageConfig
	Layout                func(*http.Request) webpage.Provider
	EnsureClientID        func(http.ResponseWriter, *http.Request)
	Broker                *pagestream.Broker
}

type Module struct {
	handler               adminhttp.Handler
	access                AccessReader
	currentPrincipal      func(*http.Request) (Principal, bool)
	currentCredential     func(*http.Request) (access.APICredential, bool)
	authorizeAnyWorkspace func(context.Context, string, *access.APICredential, access.Privilege) (bool, error)
	publications          PublicationService
	publicationCommands   map[string]uicommand.Binding
}

func Build(_ context.Context, config Config) (*Module, error) {
	m := &Module{
		access: config.Access, currentPrincipal: config.CurrentPrincipal,
		currentCredential: config.CurrentCredential, authorizeAnyWorkspace: config.AuthorizeAnyWorkspace,
		publications: config.Publications, publicationCommands: config.PublicationCommands,
	}
	readModel := adminhttp.ReadModel{
		Access: config.Access, AgentDetails: config.AgentDetails,
		StorageService: adminstorage.Service{
			CatalogPath: config.Storage.CatalogPath, DataPath: config.Storage.DataPath,
			Environment: config.Storage.Environment, ControlPlane: config.Storage.ControlPlane,
			Analytics: config.Storage.Analytics, Admitter: config.Storage.Admitter,
		},
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
		Publications:        m.adminPublications,
		AgentConfigCommand:  config.AgentConfigCommand,
		PublicationCommands: config.PublicationCommands,
		DefaultWorkspaceID:  config.DefaultWorkspaceID, AuthConfigured: config.AuthConfigured,
		AccessConfigured: config.AccessConfigured,
	}
	m.handler = adminhttp.Handler{
		ReadModel: readModel, Layout: config.Layout,
		EnsureClientID: config.EnsureClientID, Broker: config.Broker,
		PublicationMutation: m.mutatePublication,
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
