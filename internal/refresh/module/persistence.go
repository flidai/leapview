package module

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

// Persistence is the refresh capability's storage bundle.  Domain services
// consume the narrow repository contracts they own; no handler or service
// needs to know which database adapter supplies them. The authority identity
// is set only by the PostgreSQL constructor below, so a repository-shaped
// struct literal cannot opt into production PostgreSQL.
//
// Recovery is the qualification-ledger repository and is intentionally
// optional because scheduled qualification is independently configured.
// TerminalRecovery is a separate startup authority for failing runs/jobs left
// live by an interrupted process. It is required whenever persistence is
// enabled so serving cannot start with stale live work; PostgreSQL composition
// injects an explicit implementation. Runs, schedules and publication are
// required whenever persistence is enabled.
type Persistence struct {
	Runs             RunPersistence
	Schedules        refreshschedule.Repository
	Publication      refreshrun.CanonicalPublicationUnitOfWork
	Recovery         RecoveryRepository
	nativeRepository *refreshpostgres.Repository
}

// RunPersistence is the complete module-facing run capability.  It embeds
// queue/workflow, read projection, lease-fenced completion, admission and
// cancellation contracts so a configured module never discovers a missing
// operation through a runtime type assertion.
type RunPersistence interface {
	refreshrun.WorkflowRepository
	refreshrun.RunRepository
	refreshrun.RunTreeRepository
	refreshrun.LeaseFencedRunRepository
	refreshrun.LeaseFencedSupersedeRepository
	refreshrun.InvocationAdmissionChecker
	refreshrun.ScheduledInvocationAdmissionChecker
	ListTargetRuns(context.Context, refreshrun.ReadScope, string, projectgraph.ResourceID, refreshrun.RunPage) ([]refreshrun.RunRecord, error)
	LatestSuccessfulTargetRun(context.Context, refreshrun.ReadScope, string, projectgraph.ResourceID) (refreshrun.RunRecord, bool, error)
	ListSemanticModelRuns(context.Context, refreshrun.ReadScope, projectgraph.ResourceID, refreshrun.RunPage) ([]refreshrun.RunRecord, error)
	LatestSuccessfulSemanticModelRun(context.Context, refreshrun.ReadScope, projectgraph.ResourceID) (refreshrun.RunRecord, bool, error)
	CancelRun(context.Context, projectgraph.ServingIdentity, string) (refreshrun.RunRecord, error)
	CancelRunWithAudit(context.Context, projectgraph.ServingIdentity, string, *access.AuditIntent) (refreshrun.RunRecord, error)
}

// KeyedCancelRunPersistence is the native cancellation capability. Keyed
// cancellation reserves the shared platform operation and commits its
// terminal run/job/audit evidence in the same PostgreSQL transaction.
type KeyedCancelRunPersistence interface {
	// The replay result lets command surfaces suppress post-commit callbacks
	// after the exact transaction outcome has already been returned once.
	CancelRunWithAuditKeyed(context.Context, projectgraph.ServingIdentity, string, string, string, string, *access.AuditIntent) (refreshrun.RunRecord, bool, error)
}

func (p Persistence) Validate() error {
	if p.Runs == nil {
		return errors.New("refresh run persistence is required")
	}
	if p.Schedules == nil {
		return errors.New("refresh schedule persistence is required")
	}
	if p.Publication == nil {
		return errors.New("refresh publication persistence is required")
	}
	// Native PostgreSQL bundles prove identity through nativeRepository. Test
	// adapters may still provide the narrow domain contracts directly; they do
	// not participate in production admission, which checks isNative first.
	if p.nativeRepository == nil {
		return nil
	}
	if !p.nativeRepository.Configured() {
		return errors.New("PostgreSQL refresh persistence is not configured")
	}
	runs, runsOK := p.Runs.(*postgresRunPersistence)
	schedules, schedulesOK := p.Schedules.(*postgresSchedulePersistence)
	publication, publicationOK := p.Publication.(*postgresPublicationPersistence)
	if !runsOK || runs == nil || runs.repository != p.nativeRepository ||
		!schedulesOK || schedules == nil || schedules.repository != p.nativeRepository ||
		!publicationOK || publication == nil || publication.repository != p.nativeRepository {
		return errors.New("PostgreSQL refresh persistence surfaces do not match the configured native authority")
	}
	return nil
}

func (p Persistence) isNative() bool {
	return p.nativeRepository != nil && p.nativeRepository.Configured()
}

func (m *Module) readRuns() (RunPersistence, error) {
	if m == nil || m.runs == nil {
		return nil, errors.New("refresh run persistence is not configured")
	}
	return m.runs, nil
}
