package access

import "errors"

// ValidateAuditedMutationInputs enforces the shared transaction contract for
// both native audit repositories before backend-specific writes begin.
func ValidateAuditedMutationInputs(inputs []AuditEventInput) error {
	if len(inputs) == 0 {
		return errors.New("audited mutation requires at least one audit event")
	}
	return nil
}
