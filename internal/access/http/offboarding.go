package http

import (
	"errors"
	stdhttp "net/http"

	"github.com/flidai/leapview/internal/access"
)

// writeOffboardingError preserves object-level ownership evidence for an
// administrator while keeping credential material and internal SQL details
// out of the response. It returns true when the error has a documented
// lifecycle status and was written.
func writeOffboardingError(w stdhttp.ResponseWriter, err error, ownershipCode string) bool {
	var conflict *access.OwnershipConflictError
	if errors.As(err, &conflict) {
		writeJSON(w, stdhttp.StatusConflict, map[string]any{
			"code":         ownershipCode,
			"detail":       "Transfer owned objects before offboarding this principal.",
			"error":        conflict.Error(),
			"principalId":  conflict.Report.PrincipalID,
			"ownedObjects": conflict.Report.Objects,
		})
		return true
	}
	if errors.Is(err, access.ErrPlatformAdminLastAdmin) {
		writeJSON(w, stdhttp.StatusConflict, map[string]any{
			"code": "PLATFORM_ADMIN_LAST_ADMIN", "detail": err.Error(), "error": err.Error(),
		})
		return true
	}
	return false
}
