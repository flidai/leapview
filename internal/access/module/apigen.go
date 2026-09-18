package module

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/go-chi/chi/v5"
)

const apiGenObjectScopeExtension = "x-leapview-object-scope"

var errAPIGenResourceNotFound = errors.New("generated API resource not found")

type apiGenResourceScope struct {
	pathParameter string
	resolver      APIGenResourceResolver
	kind          projectgraph.Kind
}

// APIGenAuthorizer applies the same browser and immutable-snapshot guards to
// generated API operations. A runtime host is required only for project
// resource capability operations; platform-scoped operations use the durable
// platform-role evaluator and remain available without an active generation.
type APIGenAuthorizer struct {
	module        *Module
	runtime       apigenRuntimeHost
	scopes        map[string]apiGenResourceScope
	operations    map[string]APIGenOperationContract
	typed         *APIGenTypedOperationRequirementService
	instance      APIGenInstanceResolver
	delivery      APIGenDeliveryAuthorizer
	resourceShare APIGenResourceResolver
	// bootstrap is an explicit, narrow pre-activation authorization seam. It
	// is intentionally optional and is only consulted for candidate routes;
	// active generations continue through the immutable snapshot path.
	bootstrap APIGenBootstrapAuthorizer
}

type APIGenBootstrapDecision struct {
	// Handled distinguishes "there is no active generation, use bootstrap"
	// from "an active generation exists, continue with normal snapshot authz".
	Handled bool
	Allowed bool
}

// APIGenBootstrapAuthorizer is supplied by application composition. It is a
// read-only decision seam: inspect the durable exact project/environment claim
// and active-generation pointer, but never claim or mutate state here. It must
// not infer bootstrap from an arbitrary lease error. The operation ID lets the
// composition layer allow only the explicit pre-claim candidate and managed
// data staging operations; unrelated project operations remain denied.
type APIGenBootstrapAuthorizer func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (APIGenBootstrapDecision, error)

// SetBootstrapAuthorizer installs the explicit pre-activation candidate
// authorization seam. Keeping this as a setter avoids widening the existing
// constructor while allowing composition to provide the deployment-owned
// durable claim policy.
func (a *APIGenAuthorizer) SetBootstrapAuthorizer(authorizer APIGenBootstrapAuthorizer) {
	if a != nil {
		a.bootstrap = authorizer
	}
}

type apigenRuntimeHost interface {
	ProjectID() projectgraph.ResourceID
	Acquire(context.Context) (projectruntime.Lease, error)
}

func (m *Module) APIGenAuthorizer(runtime apigenRuntimeHost, operations map[string]APIGenOperationContract, resolvers APIGenResourceResolvers) (*APIGenAuthorizer, error) {
	if m == nil {
		return nil, fmt.Errorf("access module is required")
	}
	typed, err := NewAPIGenTypedOperationRequirementService(operations)
	if err != nil {
		return nil, err
	}
	authorizer := &APIGenAuthorizer{
		module:        m,
		runtime:       runtime,
		operations:    operations,
		typed:         typed,
		delivery:      resolvers.Delivery,
		instance:      resolvers.Instance,
		resourceShare: resolvers.ResourceShare,
		scopes: map[string]apiGenResourceScope{
			"dashboard":      {pathParameter: "dashboard", resolver: resolvers.Dashboard, kind: projectgraph.KindDashboard},
			"semantic-model": {pathParameter: "model", resolver: resolvers.SemanticModel, kind: projectgraph.KindSemanticModel},
			"connection":     {pathParameter: "connection", resolver: resolvers.Connection, kind: projectgraph.KindConnection},
			"source":         {pathParameter: "source", resolver: resolvers.Source, kind: projectgraph.KindSource},
			"model":          {pathParameter: "model", resolver: resolvers.Model, kind: projectgraph.KindModel},
			"pipeline":       {pathParameter: "pipeline", resolver: resolvers.Pipeline, kind: projectgraph.KindPipeline},
			"project":        {pathParameter: "project", resolver: resolvers.Project, kind: projectgraph.KindProjectNamespace},
		},
	}
	for operationID, contract := range operations {
		if err := authorizer.validateOperation(operationID, contract); err != nil {
			return nil, err
		}
		if runtime == nil && apiGenOperationNeedsRuntime(contract) && !isBootstrapAPIGenOperation(operationID) {
			return nil, fmt.Errorf("APIGen operation %q requires an active runtime host", operationID)
		}
	}
	return authorizer, nil
}

func apiGenOperationNeedsRuntime(contract APIGenOperationContract) bool {
	if !contract.Protected {
		return false
	}
	scope, ok := apiGenScope(contract)
	if !ok || scope == "platform" || scope == "instance" {
		return false
	}
	return contract.AuthzMode == "privilege"
}

// AuthorizeReplay re-runs the generated operation policy without dispatching
// its handler. Idempotent replay therefore cannot bypass current authz.
func (a *APIGenAuthorizer) AuthorizeReplay(r *http.Request) bool {
	if a == nil || r == nil {
		return false
	}
	operationID := ""
	matched := false
	parameters := 0
	for id, contract := range a.operations {
		if strings.EqualFold(contract.Method, r.Method) && apigencommand.MatchPath(contract.Path, r.URL.Path) {
			count := strings.Count(contract.Path, "{")
			if !matched || count < parameters {
				operationID, matched, parameters = id, true, count
			}
		}
	}
	if operationID == "" {
		return false
	}
	allowed := false
	protected, ok := a.Protect(operationID, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { allowed = true }))
	if !ok || protected == nil {
		return false
	}
	protected.ServeHTTP(discardAuthorizationResponse{header: http.Header{}}, r)
	return allowed
}

func (a *APIGenAuthorizer) Protect(operationID string, next http.Handler) (http.Handler, bool) {
	if a == nil || next == nil {
		return nil, false
	}
	contract, ok := a.operations[operationID]
	if !ok || !contract.Protected {
		if ok && contract.AuthzMode == "none" && !contract.Protected && contract.Command == nil && !apiGenHasNonNoneAuthzMetadata(contract) {
			return next, true
		}
		return nil, false
	}
	scope, scopeOK := apiGenScope(contract)
	if !scopeOK {
		return nil, false
	}
	if contract.AuthzMode != "authenticated" && contract.AuthzMode != "privilege" {
		return nil, false
	}
	if contract.Command != nil && contract.Command.AuthzMode != contract.AuthzMode {
		return nil, false
	}
	if !apiGenExtensionModeMatches(contract) {
		return nil, false
	}
	if strings.Contains(contract.Path, "{project}") && (scope == "platform" || contract.AuthzMode == "authenticated") {
		// A principal/platform-scoped operation can still carry a Project
		// locator (refresh, authoring, managed-data discovery). Authentication
		// or platform admin must never turn that locator into a context switch.
		// Resource and bootstrap operations use their existing snapshot/claim
		// checks below, including before the first serving generation exists.
		dispatch := next
		next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			projectID, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
			if err != nil {
				http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
				return
			}
			boundProjectID, ok := a.projectBoundaryProjectID(r.Context())
			if !ok || projectID != boundProjectID {
				http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
				return
			}
			dispatch.ServeHTTP(w, r)
		})
	}
	if scope == "platform" || scope == "instance" {
		if scope == "instance" {
			if requirement, ok := a.typedRequirement(operationID); ok && requirement.Resolver == access.TypedOperationResolverInstance {
				if a.instance == nil {
					return nil, false
				}
				return a.protectInstance(operationID, next), true
			}
			// An instance-scoped operation without a typed resolver is
			// ambiguous; it must not fall back to platform role authorization.
			return nil, false
		}
		return a.module.RequirePlatformAdmin(next), true
	}
	capability, hasCapability := apiGenOperationCapability(contract)
	if contract.AuthzMode == "authenticated" {
		if _, typed := a.typedRequirement(operationID); typed {
			// An authenticated operation with typed metadata is still an exact
			// authorization operation. It must resolve a graph target and use the
			// immutable typed principal/group snapshot path; plain authentication
			// must never become a legacy fallback. Missing scope/resolver metadata
			// therefore makes the operation unavailable until its contract is
			// completed.
			if scope == "" || scope == "platform" || scope == "instance" || a.runtime == nil {
				return nil, false
			}
			resolver, resolverOK := a.resourceResolverForContract(contract)
			if !resolverOK || resolver == nil {
				return nil, false
			}
			return a.protectResources(operationID, capability, resolver, next), true
		}
		if scope != "" && scope != "principal" {
			return nil, false
		}
		protected := a.module.Authenticate(next)
		if apiGenRequiresCSRF(operationID) {
			protected = a.module.CSRFMiddleware(protected)
		}
		return protected, true
	}
	if contract.AuthzMode != "privilege" {
		return nil, false
	}
	if scope == "" || scope == "principal" {
		return nil, false
	}
	if !hasCapability {
		// A typed operation may omit the legacy capability while its exact
		// action/resolver pair is still complete. Unknown/partial metadata is
		// rejected by the typed requirement service and never reaches here.
		if _, typed := a.typedRequirement(operationID); !typed {
			return nil, false
		}
	}
	if isDeliveryAPIGenOperation(contract) {
		if a.delivery == nil || a.runtime == nil {
			return nil, false
		}
		if isBootstrapDeliveryAPIGenOperation(operationID) && a.bootstrap != nil {
			return a.protectDeliveryBootstrapAware(operationID, capability, next), true
		}
		return a.protectDelivery(operationID, capability, next), true
	}
	resolver, ok := a.resourceResolverForContract(contract)
	if !ok {
		return nil, false
	}
	if resolver == nil {
		return nil, false
	}
	if isBootstrapAPIGenOperation(operationID) && a.bootstrap != nil {
		capability, ok := apiGenOperationCapability(contract)
		if !ok {
			return nil, false
		}
		return a.protectBootstrapOperation(operationID, capability, next), true
	}
	if a.runtime == nil {
		return nil, false
	}
	return a.protectResources(operationID, capability, resolver, next), true
}

// projectBoundaryProjectID resolves the authoritative project identity for
// principal/platform operations that carry a project locator. Application
// composition supplies Module.CurrentProjectID even when no serving runtime
// host exists; when configured, that resolver is authoritative and any
// non-empty runtime-host identity must agree with it. Direct authorizer users
// without the resolver retain the valid runtime-host path.
func (a *APIGenAuthorizer) projectBoundaryProjectID(ctx context.Context) (projectgraph.ResourceID, bool) {
	if a == nil || a.module == nil {
		return "", false
	}
	if a.module.currentProjectID != nil {
		boundProjectID, err := a.module.CurrentProjectID(ctx)
		if err != nil || boundProjectID.Validate() != nil {
			return "", false
		}
		if a.runtime != nil {
			runtimeProjectID := a.runtime.ProjectID()
			if runtimeProjectID != "" && (runtimeProjectID.Validate() != nil || runtimeProjectID != boundProjectID) {
				return "", false
			}
		}
		return boundProjectID, true
	}
	if a.runtime == nil {
		return "", false
	}
	boundProjectID := a.runtime.ProjectID()
	if boundProjectID.Validate() != nil {
		return "", false
	}
	return boundProjectID, true
}

// isBootstrapAPIGenOperation is the exact pre-activation operation allowlist.
// Candidate source retention, managed-data staging, and the narrowly scoped
// target-policy role-binding creation command are the only project-scoped
// routes that may run before an active serving generation; all other project
// resource operations must use immutable snapshot authorization.
func isBootstrapAPIGenOperation(operationID string) bool {
	switch operationID {
	case "planProjectCandidateSynchronization", "uploadProjectCandidateSourceBlob", "retainProjectCandidateSource",
		"createManagedDataUploadSession", "getManagedDataUploadSession", "cancelManagedDataUploadSession", "finalizeManagedDataUploadSession",
		"createManagedDataS3MultipartUpload", "signManagedDataS3MultipartPart", "completeManagedDataS3MultipartUpload", "abortManagedDataS3MultipartUpload",
		"createProjectRoleBinding", "listProjectRoleBindings":
		return true
	default:
		return false
	}
}

func (a *APIGenAuthorizer) protectBootstrapOperation(operationID string, capability access.Capability, next http.Handler) http.Handler {
	return a.module.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		principal, ok := a.module.CurrentPrincipal(r)
		if !ok || strings.TrimSpace(principal.ID) == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		projectID, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
		if err != nil {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		decision, err := a.bootstrap(r.Context(), r, operationID, projectID, capability)
		if err != nil {
			a.module.logger.WarnContext(
				r.Context(),
				"generated API bootstrap authorization failed",
				"operation", operationID,
				"project", projectID,
				"capability", capability,
				"error", err,
			)
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if !decision.Handled {
			// The project has an active generation. Bootstrap never grants an
			// active path; normal immutable-snapshot authz is authoritative.
			if a.runtime == nil {
				a.module.logger.WarnContext(r.Context(), "generated API active authorization runtime is unavailable", "operation", operationID, "project", projectID)
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			resolver := a.resourceResolverForContractMust(operationID)
			if resolver == nil {
				a.module.logger.WarnContext(r.Context(), "generated API active authorization resolver is unavailable", "operation", operationID, "project", projectID)
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			a.protectResources(operationID, capability, resolver, next).ServeHTTP(w, r)
			return
		}
		if isAuthoringBootstrapOperation(operationID) {
			authorized, err := a.module.AuthorizeAuthoringBootstrapRequest(r.Context(), r, projectID.String(), capability)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			if authorized && decision.Allowed {
				next.ServeHTTP(w, r.WithContext(withBootstrapAuthorization(r.Context(), projectID, principal.ID, capability)))
				return
			}
		}
		// Bootstrap is intentionally narrower than normal project RBAC: only a
		// REST API token with an explicit capability allowlist may establish the
		// first project operation.
		if bearerToken(r) == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		authorized, err := a.module.AuthorizeBootstrapRequest(r.Context(), r, capability)
		if err != nil {
			a.module.logger.WarnContext(
				r.Context(),
				"generated API bootstrap credential authorization failed",
				"operation", operationID,
				"project", projectID,
				"capability", capability,
				"error", err,
			)
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if !authorized || !decision.Allowed {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(withBootstrapAuthorization(r.Context(), projectID, principal.ID, capability)))
	}))
}

func isAuthoringBootstrapOperation(operationID string) bool {
	switch operationID {
	case "planProjectCandidateSynchronization", "uploadProjectCandidateSourceBlob", "retainProjectCandidateSource",
		"listProjectRoleBindings", "createProjectRoleBinding":
		return true
	default:
		return false
	}
}

func (a *APIGenAuthorizer) resourceResolverForContractMust(operationID string) APIGenResourceResolver {
	contract := a.operations[operationID]
	resolver, _ := a.resourceResolverForContract(contract)
	return resolver
}

func (a *APIGenAuthorizer) authorizeResources(ctx context.Context, principalID string, projectID projectgraph.ResourceID, resources []access.ResourceRef, capability access.Capability) (bool, error) {
	lease, err := a.runtime.Acquire(ctx)
	if err != nil {
		return false, err
	}
	if lease == nil {
		return false, fmt.Errorf("runtime host returned a nil lease")
	}
	defer lease.Release()
	if lease.Identity().ProjectID != projectID {
		return false, fmt.Errorf("runtime project %q does not match requested project %q", lease.Identity().ProjectID, projectID)
	}
	authorizedLease, ok := lease.(interface {
		AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
	})
	if !ok {
		return false, fmt.Errorf("active runtime lease does not expose authorization snapshot")
	}
	subjects, err := a.module.AuthorizationSubjects(ctx, principalID)
	if err != nil {
		return false, err
	}
	snapshot := authorizedLease.AuthorizationSnapshot()
	if snapshot.Identity() != lease.Identity() {
		return false, fmt.Errorf("authorization snapshot identity does not match leased serving generation")
	}
	if err := snapshot.ValidateBound(); err != nil {
		return false, err
	}
	for _, resource := range resources {
		if resource.Kind() == projectgraph.KindProjectNamespace {
			if resource.ID() != projectID {
				return false, errAPIGenResourceNotFound
			}
		} else {
			graphResource, exists := snapshot.Project().Resource(resource.ID())
			if !exists || graphResource.Kind != resource.Kind() {
				return false, errAPIGenResourceNotFound
			}
		}
		if resource.Kind() == projectgraph.KindProjectNamespace && !access.SupportsCapability(resource.Kind(), capability) {
			if !accesssnapshot.RoleAllowsCapability(snapshot, subjects, capability) {
				return false, nil
			}
			continue
		}
		allowed := false
		for _, subject := range subjects {
			candidate, err := snapshot.Allows(subject, resource, capability)
			if err != nil {
				return false, err
			}
			if candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return false, nil
		}
	}
	return true, nil
}

func (a *APIGenAuthorizer) validateOperation(operationID string, contract APIGenOperationContract) error {
	if operationID == "" || contract.OperationID != operationID {
		return fmt.Errorf("invalid APIGen operation identity %q", operationID)
	}
	if !contract.Protected {
		if contract.AuthzMode != "none" {
			return fmt.Errorf("unprotected APIGen operation %q must use authz mode none", operationID)
		}
		if contract.Command != nil || apiGenHasNonNoneAuthzMetadata(contract) {
			return fmt.Errorf("unprotected APIGen operation %q carries authorization metadata", operationID)
		}
		if scope, ok := apiGenScope(contract); ok && scope != "" {
			return fmt.Errorf("unprotected APIGen operation %q carries resource scope metadata", operationID)
		}
		return nil
	}
	if contract.AuthzMode != "authenticated" && contract.AuthzMode != "privilege" {
		return fmt.Errorf("APIGen operation %q has unsupported authz mode %q", operationID, contract.AuthzMode)
	}
	scope, scopeOK := apiGenScope(contract)
	if !scopeOK {
		return fmt.Errorf("APIGen operation %q has malformed resource scope", operationID)
	}
	if contract.Command != nil && contract.Command.AuthzMode != contract.AuthzMode {
		return fmt.Errorf("APIGen operation %q command authz mode does not match operation", operationID)
	}
	if !apiGenExtensionModeMatches(contract) {
		return fmt.Errorf("APIGen operation %q extension authz mode does not match operation", operationID)
	}
	if contract.AuthzMode == "authenticated" {
		if scope != "" && scope != "platform" && scope != "principal" && scope != "instance" {
			if _, typed := a.typedRequirement(operationID); !typed {
				return fmt.Errorf("APIGen operation %q has invalid authenticated resource scope", operationID)
			}
		}
		return nil
	}
	if contract.AuthzMode == "privilege" {
		if scope == "" || scope == "principal" {
			return fmt.Errorf("APIGen operation %q requires an exact resource or platform scope", operationID)
		}
		if _, ok := apiGenOperationCapability(contract); !ok {
			// Typed operations may intentionally omit the legacy capability.
			// Their action/resolver pair is the complete authorization contract.
			if _, typed := a.typedRequirement(operationID); !typed {
				return fmt.Errorf("APIGen operation %q has invalid capability", operationID)
			}
		}
		if isDeliveryAPIGenOperation(contract) {
			if a.delivery == nil {
				return fmt.Errorf("APIGen delivery operation %q has no target authorizer", operationID)
			}
			return nil
		}
		if scope == "instance" {
			requirement, typed := a.typedRequirement(operationID)
			if !typed || requirement.Resolver != access.TypedOperationResolverInstance || a.instance == nil {
				return fmt.Errorf("APIGen instance operation %q requires an instance typed resolver", operationID)
			}
			return nil
		}
	}
	if _, ok := a.resourceResolverForContract(contract); !ok {
		return fmt.Errorf("APIGen operation %q has invalid resource scope", operationID)
	}
	return nil
}

func apiGenOperationCapability(contract APIGenOperationContract) (access.Capability, bool) {
	value := ""
	if contract.Command != nil {
		if contract.Command.AuthzMode != contract.AuthzMode {
			return "", false
		}
		value = contract.Command.Privilege
	} else if authz, ok := contract.Extensions["x-authz"].(map[string]any); ok {
		if mode, ok := authz["mode"].(string); ok && mode != contract.AuthzMode {
			return "", false
		}
		if privilege, ok := authz["privilege"].(string); ok {
			value = privilege
		}
	}
	capability, err := access.ParseCapability(value)
	return capability, err == nil
}

func apiGenExtensionModeMatches(contract APIGenOperationContract) bool {
	raw, present := contract.Extensions["x-authz"]
	if !present {
		return true
	}
	authz, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	mode, ok := authz["mode"].(string)
	return ok && mode == contract.AuthzMode
}

func apiGenHasAuthzMetadata(contract APIGenOperationContract) bool {
	if contract.Command != nil {
		return true
	}
	if _, present := contract.Extensions["x-authz"]; present {
		return true
	}
	if _, present := contract.Extensions[apiGenObjectScopeExtension]; present {
		return true
	}
	return false
}

func apiGenHasNonNoneAuthzMetadata(contract APIGenOperationContract) bool {
	raw, present := contract.Extensions["x-authz"]
	if !present {
		return false
	}
	authz, ok := raw.(map[string]any)
	if !ok {
		return true
	}
	mode, ok := authz["mode"].(string)
	return !ok || mode != "none"
}

func apiGenScope(contract APIGenOperationContract) (string, bool) {
	value, present := contract.Extensions[apiGenObjectScopeExtension]
	if !present {
		return "", true
	}
	scope, ok := value.(string)
	if !ok {
		return "", false
	}
	if scope == "" || scope != strings.TrimSpace(scope) {
		return "", false
	}
	switch scope {
	case "dashboard", "semantic-model", "connection", "source", "model", "pipeline", "project", "resource-share", "delivery", "instance", "platform", "principal":
		return scope, true
	default:
		return "", false
	}
}

func (a *APIGenAuthorizer) resourceResolverForContract(contract APIGenOperationContract) (APIGenResourceResolver, bool) {
	// Typed authorization metadata names the security target. A generated
	// command target may instead describe its protocol identity (for example,
	// a Project-scoped refresh command whose exact Pipeline target is carried
	// in the JSON body). Keep those contracts separate and let the product-owned
	// resolver extract the typed target; malformed or absent targets still fail
	// closed when the resolver returns no resources.
	if requirement, typed := a.typedRequirement(contract.OperationID); typed {
		scope, scopeOK := apiGenScope(contract)
		if !scopeOK || scope != string(requirement.Resolver) {
			return nil, false
		}
		if requirement.Resolver == access.TypedOperationResolverResourceShare {
			if a.resourceShare == nil {
				return nil, false
			}
			return a.boundResourceShareResolver(contract), true
		}
		if requirement.Resolver != access.TypedOperationResolverDelivery && requirement.Resolver != access.TypedOperationResolverInstance {
			definition, ok := a.scopes[scope]
			if !ok || definition.resolver == nil {
				return nil, false
			}
			return a.boundResourceResolver(definition, strings.Contains(contract.Path, "{project}")), true
		}
	}
	if contract.Command != nil && contract.Command.Target != nil {
		target := *contract.Command.Target
		scope, scopeOK := apiGenScope(contract)
		if !scopeOK {
			return nil, false
		}
		switch target.Type {
		case "dashboard", "semantic-model", "connection", "source", "model", "pipeline", "project":
			if scope != "" && scope != target.Type {
				return nil, false
			}
			definition, ok := a.scopes[target.Type]
			if !ok || definition.resolver == nil || target.Parameter != definition.pathParameter || !strings.Contains(contract.Path, "{"+target.Parameter+"}") {
				return nil, false
			}
			return a.boundResourceResolver(definition, strings.Contains(contract.Path, "{project}")), true
		case "principal", "session", "token", "servicePrincipal", "conversation":
			if scope != "" && scope != "principal" && scope != "platform" {
				return nil, false
			}
			return nil, true
		default:
			return nil, false
		}
	}
	scope, scopeOK := apiGenScope(contract)
	if !scopeOK {
		return nil, false
	}
	if scope == "platform" || scope == "principal" || scope == "instance" || scope == "delivery" {
		return nil, true
	}
	if scope == "" {
		return nil, true
	}
	definition, ok := a.scopes[scope]
	if !ok || definition.resolver == nil || !strings.Contains(contract.Path, "{"+definition.pathParameter+"}") {
		return nil, false
	}
	return a.boundResourceResolver(definition, strings.Contains(contract.Path, "{project}")), true
}

func (a *APIGenAuthorizer) boundResourceShareResolver(contract APIGenOperationContract) APIGenResourceResolver {
	return func(r *http.Request, active projectgraph.ResourceID) []access.ResourceRef {
		if strings.Contains(contract.Path, "{project}") {
			requestedProject, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
			if err != nil || requestedProject != active {
				return nil
			}
		}
		resources := a.resourceShare(r, active)
		if len(resources) == 0 {
			return nil
		}
		requirement, ok := a.typedRequirement(contract.OperationID)
		if !ok {
			return nil
		}
		for _, resource := range resources {
			if requirement.ValidateResource(resource) != nil {
				return nil
			}
		}
		return resources
	}
}

func (a *APIGenAuthorizer) boundResourceResolver(definition apiGenResourceScope, assertProject bool) APIGenResourceResolver {
	return func(r *http.Request, active projectgraph.ResourceID) []access.ResourceRef {
		if assertProject {
			requestedProject, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
			if err != nil || requestedProject != active {
				return nil
			}
		}
		resources := definition.resolver(r, active)
		if len(resources) == 0 {
			return nil
		}
		for _, resource := range resources {
			if resource.Kind() != definition.kind || resource.Validate() != nil {
				return nil
			}
			if definition.kind == projectgraph.KindProjectNamespace && resource.ID() != active {
				return nil
			}
		}
		return resources
	}
}

func apiGenRequiresCSRF(operationID string) bool { return operationID == "decideDeviceAuthorization" }

type discardAuthorizationResponse struct{ header http.Header }

func (w discardAuthorizationResponse) Header() http.Header       { return w.header }
func (discardAuthorizationResponse) Write(p []byte) (int, error) { return len(p), nil }
func (discardAuthorizationResponse) WriteHeader(int)             {}
