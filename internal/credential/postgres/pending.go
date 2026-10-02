package postgres

import (
	"context"

	"github.com/flidai/leapview/internal/credential"
	credentialdb "github.com/flidai/leapview/internal/credential/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/jackc/pgx/v5"
)

// CheckNoPendingActivationTx checks that the deployment has no active
// preparation. Callers must first acquire the same target fence used by source
// publication and runtime activation. This function takes no row lock so it
// cannot invert target-fence and preparation-operation lock order. Its
// separate statement must run at READ COMMITTED after the fence wait so it
// sees commits made while the caller was waiting.
func CheckNoPendingActivationTx(ctx context.Context, tx pgx.Tx, deploymentID string) error {
	if ctx == nil || typednil.IsNil(tx) {
		return credential.ErrUnavailable
	}
	if !canonical(deploymentID, 255) {
		return credential.ErrInvalid
	}
	result, err := credentialdb.New(tx).CheckNoPendingActivation(ctx, deploymentID)
	if err != nil {
		return normalizeDatabaseError(err)
	}
	if !result.ReadCommitted {
		return credential.ErrUnavailable
	}
	if !result.NoPending {
		return credential.ErrConflict
	}
	return nil
}
