package module

import (
	"context"
	"errors"
	"fmt"

	jobpostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/pkg/jobs"
)

// Persistence is the complete authority bundle consumed by the jobs module.
// The backend marker is private and can only be set by one of the constructors
// below; a struct literal with a jobs.Repository therefore cannot accidentally
// opt into the production PostgreSQL path.
type Persistence struct {
	Repository jobs.Repository

	// NativeWorkflow and NativeCommitter are populated by the PostgreSQL
	// adapter. NativeWorkflow receives the caller-owned pgx transaction directly.
	NativeWorkflow  NativeWorkflowPort
	NativeCommitter jobs.WorkflowCommitter

	backend          persistenceBackend
	nativeRepository *jobpostgres.Repository
}

type persistenceBackend uint8

const (
	backendPostgres persistenceBackend = iota + 1
)

// NativeWorkflowPort is the production transaction-bound workflow surface.
// Callers own begin/commit/rollback of the pgx transaction.
type NativeWorkflowPort interface {
	RecordWorkflow(context.Context, jobpostgres.Tx, jobs.WorkflowIntent) error
}

// NewPostgresPersistence adapts the canonical PostgreSQL jobs repository.
// The concrete repository requirement prevents an arbitrary jobs.Repository
// from being mislabeled as production PostgreSQL authority.
func NewPostgresPersistence(repository *jobpostgres.Repository) (Persistence, error) {
	if repository == nil {
		return Persistence{}, errors.New("PostgreSQL jobs repository is required")
	}
	if !repository.Configured() {
		return Persistence{}, errors.New("PostgreSQL jobs repository is not configured")
	}
	return Persistence{
		Repository: repository, NativeWorkflow: repository,
		NativeCommitter: repository, backend: backendPostgres, nativeRepository: repository,
	}, nil
}

func (p Persistence) validate() error {
	if p.Repository == nil {
		return errors.New("jobs repository is required")
	}
	switch p.backend {
	case backendPostgres:
		if p.nativeRepository != nil {
			if p.Repository != p.nativeRepository || !p.nativeRepository.Configured() {
				return errors.New("PostgreSQL jobs repository does not match the configured native authority")
			}
			if any(p.NativeWorkflow) != any(p.nativeRepository) || any(p.NativeCommitter) != any(p.nativeRepository) {
				return errors.New("PostgreSQL jobs workflow authorities do not match the configured native repository")
			}
		}
		if p.NativeWorkflow == nil || p.NativeCommitter == nil {
			return errors.New("PostgreSQL jobs workflow and committer are required")
		}
	default:
		return fmt.Errorf("jobs persistence backend is not configured")
	}
	return nil
}

func (p Persistence) isPostgres() bool { return p.backend == backendPostgres }
