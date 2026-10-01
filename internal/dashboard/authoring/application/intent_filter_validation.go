package application

import (
	"fmt"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/compiler"
)

func validateAssignedFieldFilterCompatibility(lifecycle authoring.DashboardLifecycle, revision authoring.Revision, command authoring.Command, model *semanticmodel.Model) error {
	// Remove/move reuse the governed field validator, but must not be treated
	// as a hypothetical assignment. No new filter contract exists without filters.
	if command.AssignField == nil || len(revision.Document.Spec.Filters) == 0 {
		return nil
	}
	if _, err := compiler.CompileCanonicalDashboardBuilderFilters(revision.Document, model); err != nil {
		// An existing invalid draft must remain editable so its author can
		// repair it. Reject only a newly introduced filter incompatibility.
		return nil
	}
	// Use the actual reducer on a detached revision, not a second approximation
	// of query/alias rules. This candidate is never persisted. The command ID
	// supplies its temporary identity; the transactional service owns real IDs.
	_, candidate, err := authoring.ApplyEdit(lifecycle, revision, command, authoring.RevisionID(command.ID), revision.Number+1, revision.CreatedAt)
	if err != nil {
		return err
	}
	if _, err := compiler.CompileCanonicalDashboardBuilderFilters(candidate.Document, model); err != nil {
		return fmt.Errorf("%w: field %q conflicts with existing report filters: %v; change the filter scope before assigning this field", authoring.ErrInvalidPayload, command.AssignField.FieldID, err)
	}
	return nil
}
