package module

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"

	"github.com/Yacobolo/toolbelt/apigen/runtime/agenttool"
	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agentapi "github.com/flidai/leapview/internal/agent/api"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	agenthttp "github.com/flidai/leapview/internal/agent/http"
	agentopenai "github.com/flidai/leapview/internal/agent/openai"
	"github.com/flidai/leapview/internal/agent/productdocs"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/agent/ui"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	"github.com/flidai/leapview/internal/platform/jobs"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

type Module struct {
	handler            *agenthttp.Handler
	service            *agent.Service
	jobs               JobStore
	runWorkloadClass   string
	projectID          projectgraph.ResourceID
	currentPrincipal   func(*http.Request) (Principal, bool)
	dashboardMetrics   func(string) (queryruntime.Metrics, bool)
	recordAudit        func(context.Context, access.AuditEventInput) error
	dispatchAPIGen     func(agent.Scope, string, http.ResponseWriter, *http.Request) bool
	catalog            agenttools.Catalog
	documentation      agenttools.Documentation
	queryMetadata      func(context.Context, string, string) agenttools.VisualQueryMetadata
	queryContext       func(context.Context, agent.Scope) context.Context
	enableSystemPrompt bool
	broker             *pagestream.Broker
	logger             *slog.Logger
	chatTitleMu        sync.Mutex
	pendingChatTitles  map[string]struct{}
	mcpScope           func(*http.Request) (agent.Scope, bool)
	mcpProtect         func(http.Handler) http.Handler
	productName        string
	buildVersion       string
	apiOperations      []agenttools.APIGenOperation
	dashboardAuthoring *authoringapplication.Application
	resolveResource    agenttools.ResourceResolver
	runExecution       apigencommand.AsyncExecutionContract
}

type Service = agent.Service
type AdminAgentResponse = agentapi.AdminAgentResponse
type APIGenOperation = agenttools.APIGenOperation
type APIGenOperationContract = agenttools.OperationContract
type Documentation = agenttools.Documentation
type DocumentationSearchIndex = productdocs.SearchIndex
type QueryFreshness = agentcontracts.QueryFreshness
type VisualQueryMetadata = agenttools.VisualQueryMetadata

func BuildDocumentation(
	files fs.FS,
	index DocumentationSearchIndex,
	sign func(string, []byte) string,
	verify func(string, string) ([]byte, error),
) (Documentation, error) {
	return productdocs.New(files, index, sign, verify)
}

func BuildAPIGenOperations(operationContracts map[string]APIGenOperationContract, toolContracts map[string]agenttool.Contract) []APIGenOperation {
	return agenttools.BuildAPIGenOperations(operationContracts, toolContracts)
}

type Config struct {
	Database           *sql.DB
	Model              ModelConfig
	Service            *agent.Service
	Jobs               JobStore
	RunWorkloadClass   string
	ProjectID          projectgraph.ResourceID
	DashboardMetrics   func(string) (queryruntime.Metrics, bool)
	RecordAudit        func(context.Context, access.AuditEventInput) error
	DispatchAPIGen     func(Scope, string, http.ResponseWriter, *http.Request) bool
	Catalog            agenttools.Catalog
	Documentation      agenttools.Documentation
	QueryMetadata      func(context.Context, string, string) agenttools.VisualQueryMetadata
	QueryContext       func(context.Context, Scope) context.Context
	EnableSystemPrompt bool
	Logger             *slog.Logger
	MCPScope           func(*http.Request) (Scope, bool)
	MCPProtect         func(http.Handler) http.Handler
	ProductName        string
	BuildVersion       string
	APIGenOperations   []agenttools.APIGenOperation
	DashboardAuthoring *authoringapplication.Application
	ResolveResource    agenttools.ResourceResolver
	HTTP               HTTPConfig
}

type Principal struct {
	ID            string
	DevAuthBypass bool
}

type ModelConfig struct {
	APIKey  string
	BaseURL string
	Model   string
}

type Scope struct {
	ProjectID      string
	PrincipalID    string
	GroupIDs       []string
	ConversationID string
	Credential     CredentialScope
	DevAuthBypass  bool
}

type CredentialScope struct {
	ProjectID    string
	Capabilities []string
	Restricted   bool
}

type Settings interface {
	GetSetting(context.Context, string) (string, error)
	UpsertSetting(context.Context, string, string) error
}

type HTTPConfig struct {
	Settings           Settings
	PlatformAdmin      func(context.Context, string) (bool, error)
	CurrentPrincipal   func(*http.Request) (Principal, bool)
	CurrentCredential  func(*http.Request) (access.APICredential, bool)
	Broker             *pagestream.Broker
	CSRFToken          func(*http.Request) string
	CurrentRoleLabel   func(*http.Request) string
	Layout             func(*http.Request) webpage.Provider
	SearchReferences   func(*http.Request, agent.TurnContext, string, int) ([]ui.AgentReferenceSignal, error)
	ResolveTurnContext func(*http.Request, agent.Scope, agent.TurnContext) (agent.TurnContext, error)
}

func Build(_ context.Context, config Config) (*Module, error) {
	runExecution, err := loadRunExecutionContract()
	if err != nil {
		return nil, err
	}
	if config.RunWorkloadClass == "" {
		config.RunWorkloadClass = "background"
	}
	if err := config.ProjectID.Validate(); err != nil {
		return nil, fmt.Errorf("agent active project: %w", err)
	}
	service := config.Service
	workflow, durableWorkflow := config.Jobs.(jobs.WorkflowRecorder)
	if service == nil && config.Database != nil {
		repository := newRepository(config.Database, workflow)
		service = agent.NewService(repository, agent.Config{
			APIKey: config.Model.APIKey, BaseURL: config.Model.BaseURL, Model: config.Model.Model,
		})
	}
	if service != nil {
		if config.RecordAudit == nil {
			return nil, fmt.Errorf("agent command audit recorder is required")
		}
		service.ConfigureDefaultModel(func(modelConfig agent.Config) agentcore.Model {
			return agentopenai.NewModel(modelConfig, nil)
		})
		if durableWorkflow {
			if err := service.ConfigureRunWorkflow(workflow); err != nil {
				return nil, fmt.Errorf("configure transactional agent run workflow: %w", err)
			}
		}
	}
	var dispatchAPIGen func(agent.Scope, string, http.ResponseWriter, *http.Request) bool
	if config.DispatchAPIGen != nil {
		dispatchAPIGen = func(scope agent.Scope, operationID string, writer http.ResponseWriter, request *http.Request) bool {
			return config.DispatchAPIGen(scopeFromAgent(scope), operationID, writer, request)
		}
	}
	var queryContext func(context.Context, agent.Scope) context.Context
	if config.QueryContext != nil {
		queryContext = func(ctx context.Context, scope agent.Scope) context.Context {
			return config.QueryContext(ctx, scopeFromAgent(scope))
		}
	}
	var mcpScope func(*http.Request) (agent.Scope, bool)
	if config.MCPScope != nil {
		mcpScope = func(r *http.Request) (agent.Scope, bool) {
			scope, ok := config.MCPScope(r)
			return scopeToAgent(scope), ok
		}
	}
	m := &Module{
		service: service, jobs: config.Jobs,
		runWorkloadClass: config.RunWorkloadClass,
		projectID:        config.ProjectID,
		currentPrincipal: config.HTTP.CurrentPrincipal,
		dashboardMetrics: config.DashboardMetrics,
		recordAudit:      config.RecordAudit, dispatchAPIGen: dispatchAPIGen,
		catalog: config.Catalog, documentation: config.Documentation,
		queryMetadata: config.QueryMetadata, queryContext: queryContext,
		enableSystemPrompt: config.EnableSystemPrompt, broker: config.HTTP.Broker, logger: config.Logger,
		pendingChatTitles: map[string]struct{}{},
		mcpScope:          mcpScope, mcpProtect: config.MCPProtect,
		productName: config.ProductName, buildVersion: config.BuildVersion,
		apiOperations:      append([]agenttools.APIGenOperation(nil), config.APIGenOperations...),
		dashboardAuthoring: config.DashboardAuthoring,
		resolveResource:    config.ResolveResource,
		runExecution:       runExecution,
	}
	if err := validateRunJobHandlers(runExecution, m.JobHandlers(nil)); err != nil {
		return nil, err
	}
	if service != nil && durableWorkflow {
		service.SetPromptWorkflow(m.runWorkflow)
	}
	searchReferences := config.HTTP.SearchReferences
	if searchReferences == nil {
		searchReferences = m.SearchReferences
	}
	resolveTurnContext := config.HTTP.ResolveTurnContext
	if resolveTurnContext == nil {
		resolveTurnContext = m.ResolveTurnContext
	}
	currentPrincipal := func(r *http.Request) (agenthttp.Principal, bool) {
		if config.HTTP.CurrentPrincipal == nil {
			return agenthttp.Principal{}, false
		}
		principal, ok := config.HTTP.CurrentPrincipal(r)
		return agenthttp.Principal{ID: principal.ID, DevAuthBypass: principal.DevAuthBypass}, ok
	}
	m.handler = agenthttp.NewHandler(agenthttp.Options{
		Service: service, ActiveProjectID: m.projectID.String(), Settings: config.HTTP.Settings,
		PlatformAdmin:    config.HTTP.PlatformAdmin,
		CurrentPrincipal: currentPrincipal, CurrentCredential: config.HTTP.CurrentCredential,
		Broker: config.HTTP.Broker, CSRFToken: config.HTTP.CSRFToken,
		CurrentRoleLabel: config.HTTP.CurrentRoleLabel, Layout: config.HTTP.Layout, ChatSignal: m.chatSignal,
		ChatSignalWith: m.ChatSignalWith, SearchReferences: searchReferences,
		ResolveTurnContext: resolveTurnContext, QueueMissingTitle: m.queueMissingChatTitle,
		ExecuteStartedChatTurn: m.executeStartedChatTurn,
		EnqueueRun:             m.EnqueueRun, EnqueueChatRun: m.EnqueueChatRun,
		CancelQueuedRun: m.CancelQueuedRun, RecordCommandAudit: m.recordCommandAudit, Logger: config.Logger,
		APIGenToolContracts: apiGenToolContracts(m.apiOperations),
	})
	m.configureTools()
	return m, nil
}

func scopeFromAgent(scope agent.Scope) Scope {
	return Scope{
		ProjectID: scope.ProjectID, PrincipalID: scope.PrincipalID, GroupIDs: append([]string(nil), scope.GroupIDs...), ConversationID: scope.ConversationID,
		DevAuthBypass: scope.DevAuthBypass,
		Credential: CredentialScope{
			ProjectID:    scope.Credential.ProjectID,
			Capabilities: append([]string(nil), scope.Credential.Capabilities...),
			Restricted:   scope.Credential.Restricted,
		},
	}
}

func scopeToAgent(scope Scope) agent.Scope {
	return agent.Scope{
		ProjectID: scope.ProjectID, PrincipalID: scope.PrincipalID, GroupIDs: append([]string(nil), scope.GroupIDs...), ConversationID: scope.ConversationID,
		DevAuthBypass: scope.DevAuthBypass,
		Credential: agent.CredentialScope{
			ProjectID:    scope.Credential.ProjectID,
			Capabilities: append([]string(nil), scope.Credential.Capabilities...),
			Restricted:   scope.Credential.Restricted,
		},
	}
}

func (m *Module) HTTP() *agenthttp.Handler { return m.handler }

func (m *Module) DispatchAPIGenOperation(operationID string, w http.ResponseWriter, r *http.Request, logger *slog.Logger) bool {
	return agentgen.DispatchAPIGenOperation(
		operationID,
		agenthttp.NewAPIGenDispatcher(m.handler),
		agenthttp.APIGenTransportErrorResponder{Logger: logger},
		w,
		r,
	)
}
