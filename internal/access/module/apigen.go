package module

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

const apiGenObjectScopeExtension = "x-leapview-object-scope"

type APIGenObjectResolvers struct {
	Dashboard          ObjectResolver
	SemanticModel      ObjectResolver
	WorkspaceAsset     ObjectResolver
	ProjectEnvironment ObjectResolver
}

type apiGenObjectScope struct {
	pathParameter string
	resolver      ObjectResolver
}

type APIGenOperationContract struct {
	OperationID string
	Method      string
	Path        string
	Protected   bool
	AuthzMode   string
	Command     *APIGenCommandContract
	Extensions  map[string]any
}

// APIGenCommandContract is the authorization subset of APIGen's normalized
// command descriptor.
type APIGenCommandContract struct {
	AuthzMode string
	Privilege string
}

// AuthorizeReplay re-runs the generated operation's current authorization
// policy without invoking its domain handler. Idempotency replay must pass
// through this check because the cached response path otherwise bypasses the
// APIGen authorization wrapper that protected the original execution.
func (a *APIGenAuthorizer) AuthorizeReplay(r *http.Request) bool {
	if a == nil || r == nil {
		return false
	}
	operationID := ""
	route := routePattern(r)
	for id, contract := range a.operations {
		if contract.Method == r.Method && (contract.Path == route || matchOperationPath(contract.Path, r.URL.Path)) {
			operationID = id
			break
		}
	}
	if operationID == "" {
		return false
	}
	allowed := false
	protected, ok := a.Protect(operationID, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		allowed = true
	}))
	if !ok {
		return false
	}
	protected.ServeHTTP(discardAuthorizationResponse{header: http.Header{}}, r)
	return allowed
}

func matchOperationPath(pattern, path string) bool {
	patternParts := strings.Split(strings.Trim(strings.TrimSpace(pattern), "/"), "/")
	pathParts := strings.Split(strings.Trim(strings.TrimSpace(path), "/"), "/")
	if len(patternParts) != len(pathParts) {
		return false
	}
	for index := range patternParts {
		part := patternParts[index]
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") && len(part) > 2 {
			if pathParts[index] == "" {
				return false
			}
			continue
		}
		if part != pathParts[index] {
			return false
		}
	}
	return true
}

func routePattern(r *http.Request) string {
	if routeContext := chi.RouteContext(r.Context()); routeContext != nil {
		return routeContext.RoutePattern()
	}
	return ""
}

type discardAuthorizationResponse struct{ header http.Header }

func (w discardAuthorizationResponse) Header() http.Header       { return w.header }
func (discardAuthorizationResponse) Write(p []byte) (int, error) { return len(p), nil }
func (discardAuthorizationResponse) WriteHeader(int)             {}

type APIGenAuthorizer struct {
	module     *Module
	scopes     map[string]apiGenObjectScope
	operations map[string]APIGenOperationContract
}

func (m *Module) APIGenAuthorizer(operations map[string]APIGenOperationContract, resolvers APIGenObjectResolvers) (*APIGenAuthorizer, error) {
	if m == nil {
		return nil, fmt.Errorf("access module is required")
	}
	authorizer := &APIGenAuthorizer{
		module:     m,
		operations: operations,
		scopes: map[string]apiGenObjectScope{
			"dashboard":           {pathParameter: "dashboard", resolver: resolvers.Dashboard},
			"semantic-model":      {pathParameter: "model", resolver: resolvers.SemanticModel},
			"workspace-asset":     {pathParameter: "assetId", resolver: resolvers.WorkspaceAsset},
			"project-environment": {resolver: resolvers.ProjectEnvironment},
			"grant-management": {resolver: func(_ *http.Request, workspaceID string) []ObjectRef {
				return []ObjectRef{PlatformObject(), WorkspaceObject(workspaceID)}
			}},
			"principal": {resolver: func(_ *http.Request, workspaceID string) []ObjectRef {
				return []ObjectRef{PlatformObject(), WorkspaceObject(workspaceID)}
			}},
			"platform": {resolver: func(*http.Request, string) []ObjectRef {
				return []ObjectRef{PlatformObject()}
			}},
		},
	}
	for name, scope := range authorizer.scopes {
		if scope.pathParameter != "" && scope.resolver == nil {
			return nil, fmt.Errorf("APIGen object resolver %q is required", name)
		}
	}
	return authorizer, nil
}

func (a *APIGenAuthorizer) Protect(operationID string, next http.Handler) (http.Handler, bool) {
	contract, ok := a.operations[operationID]
	if !ok {
		return nil, false
	}
	if contract.AuthzMode == "none" && !contract.Protected {
		return next, true
	}
	if !contract.Protected {
		return nil, false
	}
	privilege, ok := apiGenOperationPrivilege(contract)
	if !ok {
		return nil, false
	}
	if isGlobalAgentOperation(operationID) {
		return a.module.ProtectGlobal(privilege, next.ServeHTTP), true
	}
	resolver, ok := a.objectResolverForContract(contract)
	if !ok {
		return nil, false
	}
	protected := a.module.ProtectHandlerWithObjects(privilege, resolver, next)
	if apiGenRequiresCSRF(operationID) {
		protected = a.module.CSRFMiddleware(protected)
	}
	return protected, true
}

func apiGenRequiresCSRF(operationID string) bool {
	return operationID == "decideDeviceAuthorization"
}

func apiGenOperationPrivilege(contract APIGenOperationContract) (Privilege, bool) {
	if contract.Command != nil {
		if contract.Command.AuthzMode != contract.AuthzMode {
			return "", false
		}
		if contract.Command.AuthzMode == "authenticated" {
			return "", true
		}
		if contract.Command.AuthzMode != "privilege" {
			return "", false
		}
		return ParsePrivilege(contract.Command.Privilege)
	}
	if contract.AuthzMode == "authenticated" {
		return "", true
	}
	if contract.AuthzMode != "privilege" {
		return "", false
	}
	authz, ok := contract.Extensions["x-authz"].(map[string]any)
	if !ok || authz["mode"] != "privilege" {
		return "", false
	}
	value, ok := authz["privilege"].(string)
	if !ok {
		return "", false
	}
	return ParsePrivilege(value)
}

func isGlobalAgentOperation(operationID string) bool {
	switch operationID {
	case "search", "listAgentConversations", "createAgentConversation", "archiveAgentConversation", "getAgentConversation", "updateAgentConversation",
		"listAgentMessages", "listAgentRuns", "createAgentRun", "getAgentRun", "cancelAgentRun", "listAgentEvents":
		return true
	default:
		return false
	}
}

func (a *APIGenAuthorizer) objectResolverForContract(contract APIGenOperationContract) (ObjectResolver, bool) {
	expectedScope, ambiguous := a.objectScopeForPath(contract.Path)
	if ambiguous {
		return nil, false
	}
	rawScope, hasScope := contract.Extensions[apiGenObjectScopeExtension]
	if !hasScope {
		return nil, expectedScope == ""
	}
	scope, ok := rawScope.(string)
	if !ok || scope == "" {
		return nil, false
	}
	definition, ok := a.scopes[scope]
	if !ok || definition.resolver == nil {
		return nil, false
	}
	if (expectedScope != "" && scope != expectedScope) || (expectedScope == "" && definition.pathParameter != "") {
		return nil, false
	}
	return definition.resolver, true
}

func (a *APIGenAuthorizer) objectScopeForPath(path string) (string, bool) {
	matched := ""
	for scope, definition := range a.scopes {
		if definition.pathParameter == "" || !strings.Contains(path, "{"+definition.pathParameter+"}") {
			continue
		}
		if matched != "" {
			return "", true
		}
		matched = scope
	}
	return matched, false
}
