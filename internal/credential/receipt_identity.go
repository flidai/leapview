package credential

import (
	"github.com/flidai/leapview/internal/credential/encryption"
	"time"
)

func credentialAuditScope(binding encryption.Binding) (string, string) {
	if binding.ScopeKind == "agent" {
		return binding.DeploymentID, "instance"
	}
	return binding.ProjectID, "connection"
}

// Equal compares receipt identity without depending on a driver's time zone
// location pointers. PostgreSQL preserves the instant, not Go's time.Location.
func (receipt ValidationReceipt) Equal(other ValidationReceipt) bool {
	if !receipt.ValidatedAt.Equal(other.ValidatedAt) || !receipt.ExpiresAt.Equal(other.ExpiresAt) {
		return false
	}
	receipt.ValidatedAt, receipt.ExpiresAt = time.Time{}, time.Time{}
	other.ValidatedAt, other.ExpiresAt = time.Time{}, time.Time{}
	return receipt == other
}
