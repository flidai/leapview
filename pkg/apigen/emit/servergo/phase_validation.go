package servergo

import (
	"fmt"

	"github.com/Yacobolo/toolbelt/apigen/ir"
)

// prepareDocumentForEmit is the validation/normalization boundary for the
// server generator. Keeping this before any discovery or rendering means a
// renderer only ever sees a validated, detached document and cannot mutate the
// caller's IR while calculating generated output.
func prepareDocumentForEmit(doc ir.Document) (ir.Document, error) {
	if err := validateDocumentForEmit(doc); err != nil {
		return ir.Document{}, err
	}
	return normalizeDocumentForEmit(doc)
}

func validateDocumentForEmit(doc ir.Document) error {
	if err := ir.Validate(doc); err != nil {
		return fmt.Errorf("validate ir document: %w", err)
	}
	return nil
}
