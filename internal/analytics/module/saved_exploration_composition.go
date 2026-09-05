package module

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
	savedapplication "github.com/flidai/leapview/internal/analytics/exploration/saved/application"
	savedsqlite "github.com/flidai/leapview/internal/analytics/exploration/saved/sqlite"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

// SavedExplorationLifecycle and SavedExplorationVisibility are the narrow
// lifecycle contracts exposed to composition. Callers should not import the
// saved-exploration implementation package merely to inspect policy metadata.
type SavedExplorationLifecycle = saved.Lifecycle
type SavedExplorationVisibility = saved.Visibility
type SavedExplorationStatus = saved.Status
type SavedExplorationRevisionID = saved.RevisionID
type SavedExplorationRevisionToken = saved.RevisionToken

const (
	SavedExplorationVisibilityPrivate      = saved.VisibilityPrivate
	SavedExplorationVisibilityRestricted   = saved.VisibilityRestricted
	SavedExplorationVisibilityOrganization = saved.VisibilityOrganization
	SavedExplorationStatusActive           = saved.StatusActive
	SavedExplorationStatusArchived         = saved.StatusArchived
)

var (
	ErrSavedExplorationInvalid          = saved.ErrInvalid
	ErrSavedExplorationUnavailable      = saved.ErrUnavailable
	ErrSavedExplorationNotFound         = saved.ErrNotFound
	ErrSavedExplorationUnauthorized     = saved.ErrUnauthorized
	ErrSavedExplorationMissingPrincipal = dataquery.ErrMissingPrincipal
)

// SavedExplorationAuthorizationAction and SavedExplorationAuthorizationRequest
// are the policy ports implemented by the process-owned cross-capability
// adapter. The saved-exploration application service remains hidden behind the
// analytics module surface.
type SavedExplorationAuthorizationAction = savedapplication.AuthorizationAction
type SavedExplorationAuthorizationRequest = savedapplication.AuthorizationRequest
type SavedExplorationAuthorizer = savedapplication.Authorizer
type SavedExplorationLeaseBoundExecutor = savedapplication.LeaseBoundExecutor
type SavedExplorationRuntimeProvider = projectruntime.Provider
type SavedExplorationRepository = saved.Repository
type SavedExplorationQuery = dataquery.Query
type SavedExplorationResult = dataquery.Result

const (
	SavedExplorationAuthorizationActionCreate  = savedapplication.AuthorizationActionCreate
	SavedExplorationAuthorizationActionView    = savedapplication.AuthorizationActionView
	SavedExplorationAuthorizationActionEdit    = savedapplication.AuthorizationActionEdit
	SavedExplorationAuthorizationActionArchive = savedapplication.AuthorizationActionArchive
	SavedExplorationAuthorizationActionExecute = savedapplication.AuthorizationActionExecute
)

// SavedExplorationServiceOptions contains only the durable persistence and
// module-facing application ports needed to build the saved-exploration
// service. The cross-capability authorizer and executor are supplied by the
// process composition root; repository construction, clock, and revision-ID
// allocation remain analytics-owned.
type SavedExplorationServiceOptions struct {
	Database            *sql.DB
	AuditIntentRecorder access.AuditIntentRecorder
	Authorizer          SavedExplorationAuthorizer
	Runtime             SavedExplorationRuntimeProvider
	Executor            SavedExplorationLeaseBoundExecutor
	// Now and NewRevisionID are optional seams for deterministic module tests.
	// Production callers leave them unset and receive the module defaults.
	Now           func() time.Time
	NewRevisionID func() (saved.RevisionID, error)
}

// BuildSavedExplorationService constructs the durable saved-exploration
// service behind the analytics module façade. It owns the SQLite repository,
// UTC clock, and opaque revision identity generation so process composition
// does not reach into analytics persistence or application packages.
func BuildSavedExplorationService(options SavedExplorationServiceOptions) (SavedExplorationService, error) {
	if options.Database == nil {
		return nil, fmt.Errorf("saved exploration database is required")
	}
	if savedExplorationModuleNil(options.AuditIntentRecorder) {
		return nil, fmt.Errorf("saved exploration audit intent recorder is required")
	}
	if savedExplorationModuleNil(options.Authorizer) {
		return nil, fmt.Errorf("saved exploration authorizer is required")
	}
	if savedExplorationModuleNil(options.Runtime) {
		return nil, fmt.Errorf("saved exploration runtime provider is required")
	}
	if savedExplorationModuleNil(options.Executor) {
		return nil, fmt.Errorf("saved exploration executor is required")
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	newRevisionID := options.NewRevisionID
	if newRevisionID == nil {
		newRevisionID = func() (saved.RevisionID, error) {
			return newSavedExplorationRevisionID(rand.Reader)
		}
	}
	repository := savedsqlite.NewRepositoryWithAudit(options.Database, options.AuditIntentRecorder)
	service, err := savedapplication.NewService(savedapplication.Options{
		Repository:    repository,
		Authorizer:    options.Authorizer,
		Runtime:       options.Runtime,
		Executor:      options.Executor,
		Now:           now,
		NewRevisionID: newRevisionID,
	})
	if err != nil {
		return nil, fmt.Errorf("build saved exploration application service: %w", err)
	}
	return service, nil
}

// NewSavedExplorationService is a descriptive alias for callers that use
// New-style module constructors.
func NewSavedExplorationService(options SavedExplorationServiceOptions) (SavedExplorationService, error) {
	return BuildSavedExplorationService(options)
}

// newSavedExplorationRevisionID allocates an opaque immutable revision
// identity. It intentionally has no relationship to authored title, slug,
// payload bytes, or serving-generation identity.
func newSavedExplorationRevisionID(reader io.Reader) (saved.RevisionID, error) {
	if reader == nil {
		return "", fmt.Errorf("generate saved exploration revision id: entropy reader is required")
	}
	const entropyBytes = 16
	entropy := make([]byte, entropyBytes)
	if _, err := io.ReadFull(reader, entropy); err != nil {
		return "", fmt.Errorf("generate saved exploration revision id: %w", err)
	}
	return saved.RevisionID("revision-" + hex.EncodeToString(entropy)), nil
}

func savedExplorationModuleNil(value any) bool {
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
