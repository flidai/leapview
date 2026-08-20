package servergo

import (
	"fmt"

	"github.com/Yacobolo/toolbelt/apigen/ir"
)

// normalizeDocumentForEmit detaches and canonicalizes the IR after validation.
// The clone keeps normalization side-effect free for callers that reuse a
// document for multiple target emitters.
func normalizeDocumentForEmit(doc ir.Document) (ir.Document, error) {
	normalized := cloneDocumentForEmit(doc)
	if err := ir.Normalize(&normalized); err != nil {
		return ir.Document{}, fmt.Errorf("normalize ir document: %w", err)
	}
	return normalized, nil
}
