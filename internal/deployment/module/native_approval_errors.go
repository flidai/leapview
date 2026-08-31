package module

import (
	"errors"
	"fmt"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
)

// mapNativeApprovalError projects the storage-native approval sentinels onto
// the deployment module's generated API failure vocabulary. The PostgreSQL
// authority deliberately does not import API concerns, so handlers must make
// this translation before handing an error to the generated transport.
//
// Publication approval commands currently declare conflict (rather than a
// dedicated forbidden/invalid variant) for authorization/input rejections;
// those native errors therefore use the closest declared approval_conflict
// contract instead of leaking as an undeclared 500.
func mapNativeApprovalError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, nativepostgres.ErrApprovalNotFound),
		errors.Is(err, nativepostgres.ErrNotFound),
		errors.Is(err, deployment.ErrNotFound),
		errors.Is(err, deployment.ErrApprovalNotFound),
		errors.Is(err, deployment.ErrApprovalScope):
		return fmt.Errorf("%w: %v", deployment.ErrApprovalNotFound, err)
	case errors.Is(err, nativepostgres.ErrApprovalExpired),
		errors.Is(err, deployment.ErrApprovalExpired):
		return fmt.Errorf("%w: %v", deployment.ErrApprovalExpired, err)
	case errors.Is(err, nativepostgres.ErrApprovalSeparationOfDuty),
		errors.Is(err, deployment.ErrApprovalSeparationOfDuty):
		return fmt.Errorf("%w: %v", deployment.ErrApprovalSeparationOfDuty, err)
	case errors.Is(err, deployment.ErrApprovalCredentialExpired):
		return fmt.Errorf("%w: %v", deployment.ErrApprovalCredentialExpired, err)
	case errors.Is(err, nativepostgres.ErrApprovalConflict),
		errors.Is(err, nativepostgres.ErrConflict),
		errors.Is(err, nativepostgres.ErrCASConflict),
		errors.Is(err, nativepostgres.ErrStaleFence),
		errors.Is(err, nativepostgres.ErrLeaseExpired),
		errors.Is(err, nativepostgres.ErrAlreadyActive),
		errors.Is(err, deployment.ErrApprovalConflict),
		errors.Is(err, deployment.ErrApprovalInvalid),
		errors.Is(err, deployment.ErrConflict),
		errors.Is(err, nativepostgres.ErrApprovalInvalid),
		errors.Is(err, nativepostgres.ErrApprovalUnauthorized),
		errors.Is(err, nativepostgres.ErrInvalid):
		return fmt.Errorf("%w: %v", deployment.ErrApprovalConflict, err)
	case errors.Is(err, nativepostgres.ErrApprovalRequired),
		errors.Is(err, deployment.ErrApprovalRequired):
		return fmt.Errorf("%w: %v", deployment.ErrApprovalRequired, err)
	default:
		// Unknown database/adapter failures are unavailable, not an internal
		// transport error. Preserve the cause for logs while exposing only the
		// generated public detail.
		return apigenfailure.Wrap("approval_unavailable", err)
	}
}

// mapNativeApprovalReadError avoids turning malformed or unauthorized
// publication approval lookups into a scope oracle. A query has no command
// failure contract, so the canonical not-found projection is used.
func mapNativeApprovalReadError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, nativepostgres.ErrApprovalInvalid) || errors.Is(err, nativepostgres.ErrApprovalUnauthorized) || errors.Is(err, nativepostgres.ErrInvalid) {
		return fmt.Errorf("%w: %v", deployment.ErrApprovalNotFound, err)
	}
	return mapNativeApprovalError(err)
}
