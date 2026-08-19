package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/go-chi/chi/v5"
)

const apiGenObjectScopeExtension = "x-leapview-object-scope"

var errAPIGenResourceNotFound = errors.New("generated API resource not found")

// APIGenResourceResolver resolves the exact graph resources named by one
// generated route. Resolvers must return canonical ResourceRefs; they may not
// infer a project or resource from an untyped fallback.
type APIGenResourceResolver func(*http.Request, projectgraph.ResourceID) []access.ResourceRef

// APIGenDeliveryAuthorizer handles target-owned delivery routes whose public
// project path is an authorization scope, not a graph ResourceRef. The
// callback authorizes against the leased active snapshot and exact operation
// capability; generated project-resource validation is intentionally bypassed
// for this narrow route family.
type APIGenDeliveryAuthorizer func(context.Context, *http.Request, string, string, projectgraph.ResourceID, access.Capability) (bool, error)

type APIGenResourceResolvers struct {
	Dashboard     APIGenResourceResolver
	SemanticModel APIGenResourceResolver
	Connection    APIGenResourceResolver
	Project       APIGenResourceResolver
	Delivery      APIGenDeliveryAuthorizer
}

type apiGenResourceScope struct {
	pathParameter string
	resolver      APIGenResourceResolver
	kind          projectgraph.Kind
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
// command descriptor. Privilege is retained as the generated field name at
// this boundary while its value is required to be a canonical capability.
type APIGenCommandContract struct {
	Owner       string
	AuthzMode   string
	Privilege   string
	Target      *APIGenCommandTarget
	Idempotency string
	Concurrency string
}

type APIGenCommandTarget struct {
	Parameter string
	Type      string
}

// APIGenAuthorizer applies the same browser and immutable-snapshot guards to
// generated API operations. A runtime host is required only for project
// resource capability operations; platform-scoped operations use the durable
// platform-role evaluator and remain available without an active generation.
type APIGenAuthorizer struct {
	module     *Module
	runtime    apigenRuntimeHost
	scopes     map[string]apiGenResourceScope
	operations map[string]APIGenOperationContract
	delivery   APIGenDeliveryAuthorizer
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
	authorizer := &APIGenAuthorizer{
		module:     m,
		runtime:    runtime,
		operations: operations,
		delivery:   resolvers.Delivery,
		scopes: map[string]apiGenResourceScope{
			"dashboard":      {pathParameter: "dashboard", resolver: resolvers.Dashboard, kind: projectgraph.KindDashboard},
			"semantic-model": {pathParameter: "model", resolver: resolvers.SemanticModel, kind: projectgraph.KindSemanticModel},
			"connection":     {pathParameter: "connection", resolver: resolvers.Connection, kind: projectgraph.KindConnection},
			"project":        {pathParameter: "project", resolver: resolvers.Project, kind: projectgraph.KindProject},
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
	if !ok || scope == "platform" {
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
	route := routePattern(r)
	for id, contract := range a.operations {
		if strings.EqualFold(contract.Method, r.Method) && (contract.Path == route || matchOperationPath(contract.Path, r.URL.Path)) {
			if route != "" && !matchOperationPath(contract.Path, r.URL.Path) {
				continue
			}
			operationID = id
			break
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
	if scope == "platform" {
		return a.module.RequirePlatformAdmin(next), true
	}
	if contract.AuthzMode == "authenticated" {
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
	capability, ok := apiGenOperationCapability(contract)
	if !ok {
		return nil, false
	}
	if isDeliveryAPIGenOperation(contract) {
		if a.delivery == nil || a.runtime == nil {
			return nil, false
		}
		if (operationID == "createDeliveryPlan" || operationID == "buildDeliveryPlan" || operationID == "publishDeliveryCandidate") && a.bootstrap != nil {
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
	return a.protectResources(capability, resolver, next), true
}

// isBootstrapAPIGenOperation is the exact pre-activation operation allowlist.
// Candidate lifecycle and managed-data staging operations are the only
// project-scoped routes that may run before an active serving generation; all
// other project resource operations must use immutable snapshot authorization.
func isBootstrapAPIGenOperation(operationID string) bool {
	switch operationID {
	case "startProjectCandidate", "getProjectCandidate", "replaceProjectCandidateArtifact", "retryProjectCandidate", "cancelProjectCandidate", "publishProjectCandidate", "reviewProjectCandidate", "cancelProjectCandidateByKey", "planProjectCandidateSynchronization", "uploadProjectCandidateSourceBlob", "retainProjectCandidateSource", "commitProjectCandidateSynchronization",
		"createManagedDataUploadSession", "getManagedDataUploadSession", "cancelManagedDataUploadSession", "finalizeManagedDataUploadSession",
		"createManagedDataS3MultipartUpload", "signManagedDataS3MultipartPart", "completeManagedDataS3MultipartUpload", "abortManagedDataS3MultipartUpload":
		return true
	default:
		return false
	}
}

func isDeliveryAPIGenOperation(contract APIGenOperationContract) bool {
	// Generated contracts carry the public API prefix (currently /api/v1),
	// while this authorizer only cares about the target-owned delivery suffix.
	return strings.Contains(contract.Path, "/projects/{project}/delivery")
}

func (a *APIGenAuthorizer) protectDelivery(operationID string, capability access.Capability, next http.Handler) http.Handler {
	return a.module.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.authorizeDeliveryRequest(w, r, operationID, capability, next)
	}))
}

// protectDeliveryBootstrapAware admits the initial delivery commands through
// the explicit pre-activation bootstrap decision. The opaque marker binds the
// exact principal/project/capability for the downstream coordinator, which
// rechecks the durable active-generation fence before committing publication.
func (a *APIGenAuthorizer) protectDeliveryBootstrapAware(operationID string, capability access.Capability, next http.Handler) http.Handler {
	return a.module.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := a.module.CurrentPrincipal(r)
		if !ok || strings.TrimSpace(principal.ID) == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		projectID, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
		if err != nil || projectID != a.runtime.ProjectID() {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		decision, err := a.bootstrap(r.Context(), r, operationID, projectID, capability)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if decision.Handled {
			if bearerToken(r) == "" {
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}
			authorized, err := a.module.AuthorizeBootstrapRequest(r.Context(), r, capability)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			if !authorized || !decision.Allowed {
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}
			marked := r.WithContext(withBootstrapAuthorization(r.Context(), projectID, principal.ID, capability))
			next.ServeHTTP(w, marked)
			return
		}
		a.authorizeDeliveryRequest(w, r, operationID, capability, next)
	}))
}

func (a *APIGenAuthorizer) authorizeDeliveryRequest(w http.ResponseWriter, r *http.Request, operationID string, capability access.Capability, next http.Handler) {
	principal, ok := a.module.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	projectID, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
	if err != nil || projectID != a.runtime.ProjectID() {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	allowed, err := a.delivery(r.Context(), r, operationID, deliveryObjectID(operationID, r), projectID, capability)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	if !allowed {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	next.ServeHTTP(w, r)
}

func deliveryObjectID(operationID string, r *http.Request) string {
	if r == nil {
		return ""
	}
	parameter := ""
	switch operationID {
	case "buildDeliveryPlan", "getDeliveryPlanPreview":
		parameter = "plan"
	case "publishDeliveryCandidate", "getDeliveryCandidateStatus":
		parameter = "candidate"
	case "rollbackDeliveryGeneration", "getDeliveryGenerationStatus":
		parameter = "generation"
	case "getDeliveryBuildStatus":
		parameter = "build"
	case "getDeliverySealStatus":
		parameter = "seal"
	case "getDeliveryPublicationEvidence":
		parameter = "publication"
	}
	if parameter == "" {
		return ""
	}
	return strings.TrimSpace(chi.URLParam(r, parameter))
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
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if !decision.Handled {
			// The project has an active generation. Bootstrap never grants an
			// active path; normal immutable-snapshot authz is authoritative.
			if a.runtime == nil {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			resolver := a.resourceResolverForContractMust(operationID)
			if resolver == nil {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			a.protectResources(capability, resolver, next).ServeHTTP(w, r)
			return
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

func (a *APIGenAuthorizer) resourceResolverForContractMust(operationID string) APIGenResourceResolver {
	contract := a.operations[operationID]
	resolver, _ := a.resourceResolverForContract(contract)
	return resolver
}

func (a *APIGenAuthorizer) protectResources(capability access.Capability, resolve APIGenResourceResolver, next http.Handler) http.Handler {
	return a.module.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := a.module.CurrentPrincipal(r)
		if !ok || strings.TrimSpace(principal.ID) == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		projectID := a.runtime.ProjectID()
		if err := projectID.Validate(); err != nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		resources := resolve(r, projectID)
		if len(resources) == 0 {
			http.NotFound(w, r)
			return
		}
		for _, resource := range resources {
			if err := resource.Validate(); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		if principal.DevBypass {
			next.ServeHTTP(w, r)
			return
		}
		allowed, err := a.authorizeResources(r.Context(), principal.ID, projectID, resources, capability)
		if err != nil {
			slog.Default().WarnContext(r.Context(), "generated API resource authorization failed", "capability", capability, "project", projectID, "error", err)
			if errors.Is(err, errAPIGenResourceNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if !allowed {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		effective, err := a.module.RequestEffectiveCapabilities(r.Context(), r, principal.ID)
		if err != nil {
			slog.Default().WarnContext(r.Context(), "generated API effective capability resolution failed", "capability", capability, "project", projectID, "error", err)
			if errors.Is(err, access.ErrForbidden) {
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if !containsCapability(effective, capability) {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func containsCapability(capabilities []access.Capability, expected access.Capability) bool {
	for _, capability := range capabilities {
		if capability == expected {
			return true
		}
	}
	return false
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
		graphResource, exists := snapshot.Project().Resource(resource.ID())
		if !exists || graphResource.Kind != resource.Kind() {
			return false, errAPIGenResourceNotFound
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
		if scope != "" && scope != "platform" && scope != "principal" {
			return fmt.Errorf("APIGen operation %q has invalid authenticated resource scope", operationID)
		}
		return nil
	}
	if contract.AuthzMode == "privilege" {
		if scope == "" || scope == "principal" {
			return fmt.Errorf("APIGen operation %q requires an exact resource or platform scope", operationID)
		}
		if _, ok := apiGenOperationCapability(contract); !ok {
			return fmt.Errorf("APIGen operation %q has invalid capability", operationID)
		}
		if isDeliveryAPIGenOperation(contract) {
			if a.delivery == nil {
				return fmt.Errorf("APIGen delivery operation %q has no target authorizer", operationID)
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
	case "dashboard", "semantic-model", "connection", "project", "platform", "principal":
		return scope, true
	default:
		return "", false
	}
}

func (a *APIGenAuthorizer) resourceResolverForContract(contract APIGenOperationContract) (APIGenResourceResolver, bool) {
	if contract.Command != nil && contract.Command.Target != nil {
		target := *contract.Command.Target
		scope, scopeOK := apiGenScope(contract)
		if !scopeOK {
			return nil, false
		}
		switch target.Type {
		case "dashboard", "semantic-model", "connection", "project":
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
	if scope == "platform" || scope == "principal" {
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
			if definition.kind == projectgraph.KindProject && resource.ID() != active {
				return nil
			}
		}
		return resources
	}
}

func apiGenRequiresCSRF(operationID string) bool { return operationID == "decideDeviceAuthorization" }

func matchOperationPath(pattern, path string) bool {
	patternParts := strings.Split(strings.Trim(strings.TrimSpace(pattern), "/"), "/")
	pathParts := strings.Split(strings.Trim(strings.TrimSpace(path), "/"), "/")
	if len(patternParts) != len(pathParts) {
		return false
	}
	for index, part := range patternParts {
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
