package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

// SavedExplorationAccessModule is the intentionally small access-module port
// consumed by the saved-exploration adapters. The adapters resolve subjects
// through the identity layer, while principal and credential context values
// are read through accessmodule's canonical context helpers below.
type SavedExplorationAccessModule interface {
	AuthorizationSubjects(context.Context, string) ([]accessmodule.SubjectRef, error)
}

// SavedExplorationAuthorizer evaluates saved-exploration lifecycle policy
// against the exact serving-generation lease supplied by the application
// service. Saved explorations are durable application objects, not graph
// resources; only their target semantic model is evaluated as a canonical
// resource capability.
type SavedExplorationAuthorizer struct {
	access     SavedExplorationAccessModule
	instanceID string
}

var _ analyticsmodule.SavedExplorationAuthorizer = (*SavedExplorationAuthorizer)(nil)

// NewSavedExplorationAuthorizer builds a fail-closed authorizer. A subject
// resolver is mandatory because principal-only authorization would silently
// drop group grants.
func NewSavedExplorationAuthorizer(accessModule SavedExplorationAccessModule, instanceID string) (*SavedExplorationAuthorizer, error) {
	if savedExplorationNil(accessModule) {
		return nil, errors.New("saved exploration access module is required")
	}
	return &SavedExplorationAuthorizer{access: accessModule, instanceID: strings.TrimSpace(instanceID)}, nil
}

// Authorize uses only the lease and immutable snapshot passed by the saved
// exploration service. It never acquires a runtime or consults mutable access
// state for policy decisions.
func (a *SavedExplorationAuthorizer) Authorize(ctx context.Context, lease runtimehostmodule.Lease, request analyticsmodule.SavedExplorationAuthorizationRequest) error {
	if a == nil || savedExplorationNil(a.access) {
		return savedExplorationUnavailable("access module", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateSavedExplorationAuthorizationRequest(request); err != nil {
		return err
	}
	// A lifecycle projection is the authoritative identity for an existing
	// exploration. Bind the policy inputs to it before any authorization is
	// evaluated: otherwise a caller could label a private object as
	// organization-visible, or substitute a different semantic model, and
	// have the request-side value drive the capability check.
	request = bindSavedExplorationLifecycle(request)
	actorID, principal, err := savedExplorationActor(ctx, request.ActorID)
	if err != nil {
		return err
	}
	snapshot, err := savedExplorationSnapshot(lease)
	if err != nil {
		return savedExplorationUnavailable("authorization snapshot", err)
	}
	if snapshot.Identity().ProjectID != request.ProjectID {
		return savedExplorationUnavailable("authorization snapshot project does not match request", nil)
	}
	subjects, err := a.access.AuthorizationSubjects(ctx, actorID)
	if err != nil {
		return savedExplorationUnavailable("authorization subjects", err)
	}
	if err := validateSavedExplorationSubjects(subjects, actorID); err != nil {
		return savedExplorationUnavailable("authorization subjects", err)
	}
	credential, credentialPresent := accessmodule.APICredentialFromContext(ctx)
	if credentialPresent && !savedExplorationCredentialMatchesActor(credential, actorID) {
		return accessmodule.ErrForbidden
	}

	// Saved explorations are personal application objects. A role label or a
	// generic project capability never grants another actor mutation access.
	if request.Action == analyticsmodule.SavedExplorationAuthorizationActionCreate {
		if request.OwnerPrincipalID != "" && request.OwnerPrincipalID != actorID {
			return accessmodule.ErrForbidden
		}
	} else {
		owner := savedExplorationOwner(request)
		switch request.Action {
		case analyticsmodule.SavedExplorationAuthorizationActionView, analyticsmodule.SavedExplorationAuthorizationActionExecute:
			if owner != actorID && request.Visibility != analyticsmodule.SavedExplorationVisibilityOrganization {
				return accessmodule.ErrForbidden
			}
		case analyticsmodule.SavedExplorationAuthorizationActionEdit, analyticsmodule.SavedExplorationAuthorizationActionArchive:
			if owner != actorID {
				return accessmodule.ErrForbidden
			}
		}
	}
	if principal.DevBypass && !credentialPresent {
		return nil
	}
	required, err := savedExplorationRequiredPermissions(request)
	if err != nil {
		return err
	}
	granted, err := snapshot.EffectiveTypedPermissions(subjects)
	if err != nil {
		return savedExplorationUnavailable("typed semantic authorization", err)
	}
	for _, pair := range required {
		resource, exists := snapshot.Project().Resource(pair.Target.ResourceID)
		if !exists || resource.Kind != projectgraph.KindSemanticModel || !access.PermissionSetAllows(granted, pair) {
			return accessmodule.ErrForbidden
		}
	}
	if credentialPresent {
		return authorizeSavedExplorationCredential(a.instanceID, actorID, request.ProjectID, credential, required)
	}
	return nil
}

func savedExplorationActor(ctx context.Context, actorID string) (string, accessmodule.Principal, error) {
	principal, ok := accessmodule.PrincipalFromContext(ctx)
	if !ok || strings.TrimSpace(principal.ID) == "" || actorID == "" || actorID != strings.TrimSpace(actorID) || principal.ID != actorID {
		return "", accessmodule.Principal{}, accessmodule.ErrForbidden
	}
	return actorID, principal, nil
}

func validateSavedExplorationAuthorizationRequest(request analyticsmodule.SavedExplorationAuthorizationRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return fmt.Errorf("saved exploration project: %w", err)
	}
	if err := request.ExplorationID.Validate(); err != nil {
		return err
	}
	if err := request.Visibility.Validate(); err != nil {
		return err
	}
	if request.Visibility == analyticsmodule.SavedExplorationVisibilityRestricted {
		return fmt.Errorf("%w: restricted visibility is reserved", analyticsmodule.ErrSavedExplorationInvalid)
	}
	switch request.Action {
	case analyticsmodule.SavedExplorationAuthorizationActionCreate,
		analyticsmodule.SavedExplorationAuthorizationActionView,
		analyticsmodule.SavedExplorationAuthorizationActionEdit,
		analyticsmodule.SavedExplorationAuthorizationActionArchive,
		analyticsmodule.SavedExplorationAuthorizationActionExecute:
	default:
		return fmt.Errorf("%w: unsupported saved exploration authorization action %q", analyticsmodule.ErrSavedExplorationInvalid, request.Action)
	}
	if request.Lifecycle != (analyticsmodule.SavedExplorationLifecycle{}) {
		if err := request.Lifecycle.Validate(); err != nil {
			return err
		}
		if request.Lifecycle.ProjectID != request.ProjectID {
			return fmt.Errorf("%w: lifecycle project does not match request", analyticsmodule.ErrSavedExplorationInvalid)
		}
		if request.Lifecycle.ID != request.ExplorationID {
			return fmt.Errorf("%w: lifecycle id does not match request", analyticsmodule.ErrSavedExplorationInvalid)
		}
	}
	return nil
}

// bindSavedExplorationLifecycle returns the request with existing-object
// policy metadata taken from the validated lifecycle projection. Request
// fields remain useful for create authorization, and an edit request's model
// is the proposed authored target that must be capability-checked. All other
// existing-object actions authorize the lifecycle's durable model.
func bindSavedExplorationLifecycle(request analyticsmodule.SavedExplorationAuthorizationRequest) analyticsmodule.SavedExplorationAuthorizationRequest {
	if request.Lifecycle == (analyticsmodule.SavedExplorationLifecycle{}) {
		return request
	}
	request.OwnerPrincipalID = request.Lifecycle.OwnerPrincipalID
	request.Title = request.Lifecycle.Title
	request.Visibility = request.Lifecycle.Visibility
	request.Status = request.Lifecycle.Status
	if request.Action != analyticsmodule.SavedExplorationAuthorizationActionEdit {
		request.SemanticModelID = request.Lifecycle.SemanticModelID
	}
	return request
}

// authorizationSnapshotLease is deliberately narrower than runtimehost.Lease
// and retains the exact value-returning production snapshot contract.
type authorizationSnapshotLease interface {
	AuthorizationSnapshot() accessmodule.AuthorizationSnapshot
}

func savedExplorationSnapshot(lease runtimehostmodule.Lease) (accessmodule.AuthorizationSnapshot, error) {
	if savedExplorationNil(lease) {
		return accessmodule.AuthorizationSnapshot{}, errors.New("saved exploration runtime lease is required")
	}
	identity := lease.Identity()
	if err := identity.Validate(); err != nil {
		return accessmodule.AuthorizationSnapshot{}, fmt.Errorf("saved exploration lease identity: %w", err)
	}
	bound, ok := lease.(authorizationSnapshotLease)
	if !ok {
		return accessmodule.AuthorizationSnapshot{}, errors.New("saved exploration runtime lease does not expose authorization snapshot")
	}
	snapshot := bound.AuthorizationSnapshot()
	if err := snapshot.ValidateBound(); err != nil {
		return accessmodule.AuthorizationSnapshot{}, fmt.Errorf("saved exploration authorization snapshot: %w", err)
	}
	if snapshot.Identity() != identity {
		return accessmodule.AuthorizationSnapshot{}, errors.New("saved exploration authorization snapshot identity does not match lease")
	}
	return snapshot, nil
}

func validateSavedExplorationSubjects(subjects []accessmodule.SubjectRef, actorID string) error {
	if len(subjects) == 0 {
		return errors.New("saved exploration authorization subjects are unavailable")
	}
	hasPrincipal := false
	for _, subject := range subjects {
		if err := subject.Validate(); err != nil {
			return err
		}
		if subject.Kind == accessmodule.SubjectKindPrincipal && subject.ID == actorID {
			hasPrincipal = true
		}
	}
	if !hasPrincipal {
		return errors.New("saved exploration authorization subjects omit the authenticated principal")
	}
	return nil
}

// savedExplorationRequiredPermissions keeps metadata discovery separate from
// query construction. Rebinding an existing working copy additionally requires
// read access to its persisted model, so a replacement cannot launder access.
func savedExplorationRequiredPermissions(request analyticsmodule.SavedExplorationAuthorizationRequest) ([]access.PermissionPair, error) {
	models := []projectgraph.ResourceID{request.SemanticModelID}
	if request.Action == analyticsmodule.SavedExplorationAuthorizationActionEdit && request.Lifecycle.ID != "" && request.Lifecycle.SemanticModelID != request.SemanticModelID {
		models = append(models, request.Lifecycle.SemanticModelID)
	}
	var required []access.PermissionPair
	for index, modelID := range models {
		resource, err := access.NewResourceRef(modelID, projectgraph.KindSemanticModel)
		if err != nil {
			return nil, err
		}
		read, err := access.NewExactPermissionPair(access.ActionSemanticRead, request.ProjectID, resource)
		if err != nil {
			return nil, err
		}
		required = append(required, read)
		if index == 0 && request.Action != analyticsmodule.SavedExplorationAuthorizationActionView {
			query, err := access.NewExactPermissionPair(access.ActionSemanticQuery, request.ProjectID, resource)
			if err != nil {
				return nil, err
			}
			pairs, err := access.RequiredPermissionPairs(query)
			if err != nil {
				return nil, err
			}
			required = append(required, pairs...)
		}
	}
	return required, nil
}

func authorizeSavedExplorationCredential(instanceID, actorID string, projectID projectgraph.ResourceID, credential accessmodule.APICredential, required []access.PermissionPair) error {
	if credential.Token.ID == "" || credential.Token.PrincipalID != actorID {
		return accessmodule.ErrForbidden
	}
	if credential.Authoring != nil {
		session := credential.Authoring
		if instanceID == "" || session.PrincipalID != actorID || session.ID == "" ||
			session.Scope.AuthorizePairs(instanceID, projectID.String(), required) != nil {
			return accessmodule.ErrForbidden
		}
		// Authoring authentication synthesizes token identity without a REST
		// token ceiling. If one is supplied, it must attenuate the scope too.
		if credential.Token.PermissionProfile == "" && credential.Token.Permissions == nil && credential.Token.Capabilities == nil {
			return nil
		}
	}
	if credential.Token.PermissionProfile != access.PermissionCatalogProfile || credential.Token.Permissions == nil ||
		access.ValidatePermissionPairs(credential.Token.Permissions) != nil {
		return accessmodule.ErrForbidden
	}
	for _, pair := range required {
		if !access.PermissionSetAllows(credential.Token.Permissions, pair) {
			return accessmodule.ErrForbidden
		}
	}

	return nil
}

func savedExplorationOwner(request analyticsmodule.SavedExplorationAuthorizationRequest) string {
	if request.Lifecycle.ID != "" {
		return strings.TrimSpace(request.Lifecycle.OwnerPrincipalID)
	}
	return strings.TrimSpace(request.OwnerPrincipalID)
}

// SavedExplorationExecutor is the lease-bound governed query adapter. It
// wraps the exact runtime metrics value carried by lease.Runtime; it never
// opens or resolves another runtime generation.
type SavedExplorationExecutor struct {
	instanceID    string
	access        SavedExplorationAccessModule
	admitter      workloadmodule.Admitter
	auditRecorder accessmodule.CanonicalAuditRecorder
}

var _ analyticsmodule.SavedExplorationLeaseBoundExecutor = (*SavedExplorationExecutor)(nil)

type SavedExplorationExecutorOptions struct {
	InstanceID    string
	AccessModule  SavedExplorationAccessModule
	Admitter      workloadmodule.Admitter
	AuditRecorder accessmodule.CanonicalAuditRecorder
}

// NewSavedExplorationExecutor builds a fail-closed governed executor. The
// canonical audit recorder is required so query authorization cannot be
// silently deployed without its project-generation audit seam.
func NewSavedExplorationExecutor(options SavedExplorationExecutorOptions) (*SavedExplorationExecutor, error) {
	if savedExplorationNil(options.AccessModule) {
		return nil, errors.New("saved exploration access module is required")
	}
	if savedExplorationNil(options.Admitter) {
		return nil, errors.New("saved exploration workload admitter is required")
	}
	if savedExplorationNil(options.AuditRecorder) {
		return nil, errors.New("saved exploration canonical audit recorder is required")
	}
	return &SavedExplorationExecutor{instanceID: options.InstanceID, access: options.AccessModule, admitter: options.Admitter, auditRecorder: options.AuditRecorder}, nil
}

// Execute governs and executes through the exact lease-bound runtime. Context
// principal and request actor must agree, and a query cannot change project
// identity or principal while crossing this boundary.
func (e *SavedExplorationExecutor) Execute(ctx context.Context, lease runtimehostmodule.Lease, actorID string, query analyticsmodule.SavedExplorationQuery) (analyticsmodule.SavedExplorationResult, error) {
	if e == nil || savedExplorationNil(e.access) || savedExplorationNil(e.admitter) || savedExplorationNil(e.auditRecorder) {
		return analyticsmodule.SavedExplorationResult{}, savedExplorationUnavailable("executor dependencies", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	actorID, _, err := savedExplorationActor(ctx, actorID)
	if err != nil {
		return analyticsmodule.SavedExplorationResult{}, err
	}
	if credential, ok := accessmodule.APICredentialFromContext(ctx); ok && !savedExplorationCredentialMatchesActor(credential, actorID) {
		return analyticsmodule.SavedExplorationResult{}, accessmodule.ErrForbidden
	}
	snapshot, err := savedExplorationSnapshot(lease)
	if err != nil {
		return analyticsmodule.SavedExplorationResult{}, savedExplorationUnavailable("authorization snapshot", err)
	}
	if query.ProjectID != snapshot.Identity().ProjectID {
		return analyticsmodule.SavedExplorationResult{}, savedExplorationUnavailable("query project does not match authorization snapshot", nil)
	}
	if query.PrincipalID != actorID {
		return analyticsmodule.SavedExplorationResult{}, accessmodule.ErrForbidden
	}
	metrics, err := savedExplorationMetricsForLease(lease)
	if err != nil {
		return analyticsmodule.SavedExplorationResult{}, savedExplorationUnavailable("runtime serving identity", err)
	}

	governed := dashboardmodule.WithQueryAuthorization(metrics, dashboardmodule.QueryAuthorizationConfig{
		InstanceID: e.instanceID,
		SnapshotFromContext: func(context.Context) (accessmodule.AuthorizationSnapshot, error) {
			return snapshot, nil
		},
		SubjectsFromContext: func(subjectContext context.Context, principalID string) ([]accessmodule.SubjectRef, error) {
			if principalID != actorID {
				return nil, accessmodule.ErrForbidden
			}
			subjects, subjectErr := e.access.AuthorizationSubjects(subjectContext, principalID)
			if subjectErr != nil {
				return nil, savedExplorationUnavailable("authorization subjects", subjectErr)
			}
			if subjectErr := validateSavedExplorationSubjects(subjects, actorID); subjectErr != nil {
				return nil, savedExplorationUnavailable("authorization subjects", subjectErr)
			}
			return subjects, nil
		},
		PrincipalFromContext: func(principalContext context.Context) (dashboardmodule.QueryPrincipal, bool) {
			principal, ok := accessmodule.PrincipalFromContext(principalContext)
			return dashboardmodule.QueryPrincipal{ID: principal.ID, DevBypass: principal.DevBypass}, ok
		},
		CredentialFromContext: accessmodule.APICredentialFromContext,
		AuditRecorder:         e.auditRecorder,
	})
	if governed == nil {
		return analyticsmodule.SavedExplorationResult{}, savedExplorationUnavailable("query authorization", nil)
	}
	admitted := dashboardmodule.WithAdmission(governed, e.admitter)
	if admitted == nil {
		return analyticsmodule.SavedExplorationResult{}, savedExplorationUnavailable("workload admission", nil)
	}
	result, executeErr := admitted.ExecuteDataQuery(ctx, query)
	if executeErr != nil {
		if dashboardmodule.IsQueryAuthorizationDenied(executeErr) || errors.Is(executeErr, analyticsmodule.ErrSavedExplorationMissingPrincipal) {
			return result, accessmodule.ErrForbidden
		}
	}
	return result, executeErr
}

func savedExplorationMetricsForLease(lease runtimehostmodule.Lease) (dashboardmodule.Metrics, error) {
	if savedExplorationNil(lease) {
		return nil, errors.New("saved exploration runtime lease is required")
	}
	identity := lease.Identity()
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("saved exploration lease identity: %w", err)
	}
	runtime := lease.Runtime()
	if savedExplorationNil(runtime) {
		return nil, errors.New("saved exploration runtime is unavailable")
	}
	metrics, ok := runtime.(dashboardmodule.Metrics)
	if !ok || savedExplorationNil(metrics) {
		return nil, errors.New("saved exploration runtime query metrics are unavailable")
	}
	return metrics, nil
}

func savedExplorationCredentialMatchesActor(credential accessmodule.APICredential, actorID string) bool {
	if principalID := strings.TrimSpace(credential.Principal.ID); principalID != "" && principalID != actorID {
		return false
	}
	if principalID := strings.TrimSpace(credential.Token.PrincipalID); principalID != "" && principalID != actorID {
		return false
	}
	return true
}

func savedExplorationUnavailable(label string, err error) error {
	if errors.Is(err, accessmodule.ErrForbidden) {
		return err
	}
	if errors.Is(err, analyticsmodule.ErrSavedExplorationUnavailable) {
		return err
	}
	if err == nil {
		return fmt.Errorf("%w: %s", analyticsmodule.ErrSavedExplorationUnavailable, label)
	}
	return fmt.Errorf("%w: %s: %v", analyticsmodule.ErrSavedExplorationUnavailable, label, err)
}

func savedExplorationNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// SavedExplorationAdapters groups the two application-service ports used by
// composition roots that construct both lifecycle and execution behavior.
type SavedExplorationAdapters struct {
	Authorizer analyticsmodule.SavedExplorationAuthorizer
	Executor   analyticsmodule.SavedExplorationLeaseBoundExecutor
}

// NewSavedExplorationAdapters builds both ports with identical access and
// audit dependencies. It is useful to keep composition in internal/app while
// leaving the saved-exploration application service dashboard-independent.
func NewSavedExplorationAdapters(options SavedExplorationExecutorOptions) (SavedExplorationAdapters, error) {
	authorizer, err := NewSavedExplorationAuthorizer(options.AccessModule, options.InstanceID)
	if err != nil {
		return SavedExplorationAdapters{}, err
	}
	executor, err := NewSavedExplorationExecutor(options)
	if err != nil {
		return SavedExplorationAdapters{}, err
	}
	return SavedExplorationAdapters{Authorizer: authorizer, Executor: executor}, nil
}
