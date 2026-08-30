// Package deploymentpostgres composes Deployment's native PostgreSQL
// persistence from process-owned capability authorities. Keeping concrete
// adapters at this application composition boundary lets the deployment
// module consume only its contract and persistence surfaces.
package deploymentpostgres

import (
	"errors"
	"fmt"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	deploymentaudit "github.com/flidai/leapview/internal/app/deploymentaudit"
	deploymentevents "github.com/flidai/leapview/internal/app/deploymentevents"
	deploymentoperation "github.com/flidai/leapview/internal/app/deploymentoperation"
	deploymentworkflow "github.com/flidai/leapview/internal/app/deploymentworkflow"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgresql "github.com/flidai/leapview/internal/deployment/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
)

// Authorities contains the already-composed sibling capability authorities
// needed by Deployment's native PostgreSQL persistence. Every adapter forwards
// the deployment transaction unchanged.
type Authorities struct {
	Access     *accesspostgres.AuditRepository
	Events     *eventspostgres.Repository
	Jobs       *jobspostgres.Repository
	Operations *operationpostgres.Repository
}

// NewPersistence constructs native Deployment persistence and all of its
// transactional consequence adapters from one control-plane database handle.
// It does not begin a transaction or perform schema work; callers retain
// lifecycle ownership of control.
func NewPersistence(control deploymentpostgresql.DBTX, authorities Authorities) (deploymentmodule.Persistence, error) {
	if control == nil {
		return deploymentmodule.Persistence{}, errors.New("deployment PostgreSQL control pool is required")
	}
	if authorities.Access == nil {
		return deploymentmodule.Persistence{}, errors.New("deployment PostgreSQL access authority is required")
	}
	if authorities.Events == nil {
		return deploymentmodule.Persistence{}, errors.New("deployment PostgreSQL event authority is required")
	}
	if authorities.Jobs == nil {
		return deploymentmodule.Persistence{}, errors.New("deployment PostgreSQL jobs authority is required")
	}
	if authorities.Operations == nil {
		return deploymentmodule.Persistence{}, errors.New("deployment PostgreSQL operation authority is required")
	}

	activationAudit := deploymentaudit.NewWithRepository(authorities.Access)
	repository := deploymentpostgresql.NewWithActivationAudit(control, activationAudit)
	persistence, err := deploymentmodule.NewPostgresPersistenceWithCapabilities(repository, deploymentmodule.NativePersistenceCapabilities{
		Events:     deploymentevents.NewWithRepository(authorities.Events),
		Audit:      deploymentaudit.NewWithRepository(authorities.Access),
		Workflow:   deploymentworkflow.New(authorities.Jobs),
		Operations: deploymentoperation.New(authorities.Operations),
	})
	if err != nil {
		return deploymentmodule.Persistence{}, fmt.Errorf("construct deployment PostgreSQL persistence: %w", err)
	}
	return persistence, nil
}
