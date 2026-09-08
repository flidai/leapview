// Package agentpostgres composes Agent's native PostgreSQL persistence from
// the already-owned process authorities. The composition boundary retains
// concrete adapters here so capability modules consume only their contract
// and persistence surfaces.
package agentpostgres

import (
	"errors"
	"fmt"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	agentpostgresql "github.com/flidai/leapview/internal/agent/postgres"
	agentaudit "github.com/flidai/leapview/internal/app/agentaudit"
	agentevents "github.com/flidai/leapview/internal/app/agentevents"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
)

// Authorities contains the already-composed sibling capability authorities
// needed by Agent's native PostgreSQL repository. The adapters retain these
// stateless/connection-bound authorities and pass the Agent transaction
// through unchanged.
type Authorities struct {
	Access *accesspostgres.AuditRepository
	Events *eventspostgres.Repository
	Jobs   *jobspostgres.Repository
}

// NewPersistence constructs Agent native persistence from a control-plane
// database handle and sibling authorities. It does not own pool or schema
// lifecycle; callers retain those responsibilities at process composition.
func NewPersistence(control agentpostgresql.DBTX, authorities Authorities) (agentmodule.Persistence, error) {
	if control == nil {
		return agentmodule.Persistence{}, errors.New("agent PostgreSQL control pool is required")
	}
	if authorities.Access == nil {
		return agentmodule.Persistence{}, errors.New("agent PostgreSQL access authority is required")
	}
	if authorities.Events == nil {
		return agentmodule.Persistence{}, errors.New("agent PostgreSQL event authority is required")
	}
	if authorities.Jobs == nil {
		return agentmodule.Persistence{}, errors.New("agent PostgreSQL jobs authority is required")
	}
	repository, err := agentpostgresql.NewProduction(control, agentpostgresql.Options{
		Workflow: authorities.Jobs,
		Jobs:     authorities.Jobs,
		Audit:    agentaudit.NewWithRepository(authorities.Access),
		Domain:   agentevents.NewWithRepository(authorities.Events),
	})
	if err != nil {
		return agentmodule.Persistence{}, fmt.Errorf("construct agent PostgreSQL repository: %w", err)
	}
	persistence, err := agentmodule.NewPostgresPersistence(repository)
	if err != nil {
		return agentmodule.Persistence{}, fmt.Errorf("construct agent PostgreSQL persistence: %w", err)
	}
	return persistence, nil
}
