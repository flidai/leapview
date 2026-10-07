package module

import "github.com/flidai/leapview/internal/credential"

// These narrow contracts let the application compose the server-owned probe
// through the credential module without importing the domain package.
type ValidationResource = credential.Resource
type ValidationScope = credential.Scope
type ValidationTarget = credential.ValidationTarget

var (
	ErrInvalidValidation     = credential.ErrInvalid
	ErrValidationConflict    = credential.ErrConflict
	ErrValidationUnavailable = credential.ErrUnavailable
	ErrValidationNotFound    = credential.ErrNotFound
)
