package cli

import (
	"errors"
	"strings"

	accessmodule "github.com/flidai/leapview/internal/access/module"
)

// Transition intent and plan types belong to the access module. These aliases
// preserve the admin CLI's serialized contract without making the application
// composition depend on its command package.
type AccessTransitionRoleIntent = accessmodule.AccessTransitionRoleIntent
type AccessTransitionGrantIntent = accessmodule.AccessTransitionGrantIntent
type AccessTransitionIntent = accessmodule.AccessTransitionIntent
type AccessTransitionPlan = accessmodule.AccessTransitionPlan

// StageAccessTransitionRequest binds semantic intent to a concrete admitted
// maintenance invocation. Apply requires an installation-owned verifier; the
// string fields alone are not proof that maintenance is active.
type StageAccessTransitionRequest struct {
	Intent                     AccessTransitionIntent `json:"intent"`
	MaintenanceOperationID     string                 `json:"maintenanceOperationId"`
	MaintenanceOperationDigest string                 `json:"maintenanceOperationDigest"`
	OperationID                string                 `json:"operationId"`
	Apply                      bool                   `json:"apply"`
}

// Plan validates the concrete invocation separately from semantic intent.
func (r StageAccessTransitionRequest) Plan() (AccessTransitionPlan, error) {
	if !stableTransitionID(r.OperationID) || !stableTransitionID(r.MaintenanceOperationID) || !canonicalTransitionDigest(r.MaintenanceOperationDigest) {
		return AccessTransitionPlan{}, errors.New("stable operation identity and canonical maintenance digest are required")
	}
	return r.Intent.Plan()
}

func stableTransitionID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 200 && !strings.ContainsAny(value, "\x00\r\n")
}

func canonicalTransitionDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, ch := range value[len("sha256:"):] {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}
