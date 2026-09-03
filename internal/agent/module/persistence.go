package module

import (
	"fmt"

	"github.com/flidai/leapview/internal/agent"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
)

type persistenceBackend uint8

const (
	backendPostgres persistenceBackend = iota + 1
)

// Persistence is the typed agent storage selection passed into module
// composition. backend is intentionally private so callers cannot forge a
// native marker.
type Persistence struct {
	Repository agent.Repository
	backend    persistenceBackend
}

func NewPostgresPersistence(repository *agentpostgres.Repository) (Persistence, error) {
	if repository == nil {
		return Persistence{}, fmt.Errorf("agent PostgreSQL repository is required")
	}
	if !repository.Configured() || !repository.TransactionCapable() {
		return Persistence{}, fmt.Errorf("agent PostgreSQL repository must be configured with a transactional database")
	}
	if !repository.WorkflowCapable() || !repository.JobsCapable() || !repository.AuditCapable() || !repository.DomainEventCapable() {
		return Persistence{}, fmt.Errorf("agent PostgreSQL workflow, jobs, audit, and domain-event authorities are required")
	}
	return Persistence{Repository: repository, backend: backendPostgres}, nil
}

func (p *Persistence) isPostgres() bool { return p != nil && p.backend == backendPostgres }

func (p Persistence) validate() error {
	if p.Repository == nil {
		return fmt.Errorf("agent persistence is required")
	}
	if !p.isPostgres() {
		return fmt.Errorf("agent persistence backend is invalid")
	}
	if p.isPostgres() {
		native, ok := p.Repository.(*agentpostgres.Repository)
		if !ok || !native.Configured() || !native.TransactionCapable() || !native.WorkflowCapable() || !native.JobsCapable() || !native.AuditCapable() || !native.DomainEventCapable() {
			return fmt.Errorf("agent PostgreSQL persistence is not fully configured")
		}
	}
	return nil
}
