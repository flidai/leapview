package authoring

import "context"

// SavedVisual retains a query definition for reuse under one project/account.
// Query result rows are never copied into the library.
type SavedVisual struct {
	ID              string
	Title           string
	SemanticModelID string
	SourceKey       string
	DefinitionJSON  string
}

type SavedVisualStore interface {
	UnsaveVisual(context.Context, string, string, string) error
	SaveVisual(context.Context, string, string, SavedVisual) (SavedVisual, error)
	SavedVisuals(context.Context, string, string) ([]SavedVisual, error)
	SavedVisual(context.Context, string, string, string) (SavedVisual, error)
}
