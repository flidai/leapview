package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/binding"
	manageddatapostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/jackc/pgx/v5"
)

// nativeManagedDataBindingAdmission adapts the managed-data binding
// capability to the native generation-admission transaction. The wrapped
// repository is rebound to the caller-owned pgx transaction for every
// admission, so binding-set writes commit or roll back with delivery,
// serving-state, and DuckLake evidence.
type nativeManagedDataBindingAdmission struct {
	repository *manageddatapostgres.Repository
}

const maxNativeManagedDataPins = 10_000

// NewNativeManagedDataBindingAdmission constructs the native managed-data
// binding admission port. The repository must be the process-owned control
// database repository; this constructor performs no I/O.
func NewNativeManagedDataBindingAdmission(repository *manageddatapostgres.Repository) (NativeManagedDataBindingAdmission, error) {
	if repository == nil || repository.DB() == nil {
		return nil, errors.New("native managed-data repository is required")
	}
	return &nativeManagedDataBindingAdmission{repository: repository}, nil
}

func (a *nativeManagedDataBindingAdmission) AdmitServingStateBindingsTx(ctx context.Context, tx deploymentnative.Tx, identity projectgraph.ServingIdentity, pins []release.ManagedDataPin) error {
	if a == nil || a.repository == nil {
		return fmt.Errorf("%w: native managed-data binding authority is unavailable", deploymentnative.ErrInvalid)
	}
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("%w: managed-data serving identity: %v", deploymentnative.ErrInvalid, err)
	}
	pgxTx, ok := tx.(pgx.Tx)
	if !ok || pgxTx == nil {
		return fmt.Errorf("%w: native managed-data binding admission requires a PostgreSQL transaction", deploymentnative.ErrInvalid)
	}
	revisions, err := normalizeNativeManagedDataPins(pins)
	if err != nil {
		return err
	}
	binder, err := binding.New(a.repository.WithTx(pgxTx))
	if err != nil {
		return err
	}
	// Binder's hook is intentionally reused here: it validates every content
	// digest against a ready revision and installs an empty binding marker when
	// revisions is empty, which keeps runtime resolution deterministic.
	return binder.AfterArtifactValidation(ctx, servingstate.State{
		ID: servingstate.ID(identity.GenerationID), ProjectID: identity.ProjectID,
		Environment: servingstate.Environment(identity.Environment),
	}, servingstate.Validation{ProjectID: identity.ProjectID, ManagedDataRevisions: revisions})
}

// normalizeNativeManagedDataPins enforces the canonical identity contract
// before lowering release pins into Binder's map form. An empty map is
// intentionally non-nil: Binder uses it to publish an immutable empty marker.
func normalizeNativeManagedDataPins(pins []release.ManagedDataPin) (map[string]string, error) {
	if len(pins) > maxNativeManagedDataPins {
		return nil, fmt.Errorf("%w: managed-data pin count exceeds limit", deploymentnative.ErrInvalid)
	}
	canonical := append([]release.ManagedDataPin(nil), pins...)
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].ConnectionID != canonical[j].ConnectionID {
			return canonical[i].ConnectionID < canonical[j].ConnectionID
		}
		return canonical[i].RevisionID < canonical[j].RevisionID
	})
	revisions := make(map[string]string, len(canonical))
	for _, pin := range canonical {
		if pin.ConnectionID == "" || pin.ConnectionID != strings.TrimSpace(pin.ConnectionID) {
			return nil, fmt.Errorf("%w: managed-data pin connection identity is not canonical", deploymentnative.ErrInvalid)
		}
		connection, err := projectgraph.NewResourceID(pin.ConnectionID)
		if err != nil || connection.String() != pin.ConnectionID {
			return nil, fmt.Errorf("%w: managed-data pin connection identity is invalid", deploymentnative.ErrInvalid)
		}
		if pin.RevisionID == "" || pin.RevisionID != strings.TrimSpace(pin.RevisionID) || manageddata.ValidateRevisionID(pin.RevisionID) != nil {
			return nil, fmt.Errorf("%w: managed-data pin revision digest is invalid", deploymentnative.ErrInvalid)
		}
		if _, exists := revisions[pin.ConnectionID]; exists {
			return nil, fmt.Errorf("%w: duplicate managed-data pin connection %q", deploymentnative.ErrInvalid, pin.ConnectionID)
		}
		revisions[pin.ConnectionID] = pin.RevisionID
	}
	return revisions, nil
}

var _ NativeManagedDataBindingAdmission = (*nativeManagedDataBindingAdmission)(nil)
