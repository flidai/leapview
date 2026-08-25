// Package connectionadmin exposes the analytics-owned connection binding
// administration contract to product surfaces. The implementation remains in
// the connectionbinding use-case package and is assembled by the application.
package connectionadmin

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
)

type BindingScope = connectionbinding.BindingScope
type BindingID = connectionbinding.BindingID
type TargetID = connectionbinding.TargetID
type BindingKey = connectionbinding.BindingKey
type BindingHealthStatus = connectionbinding.BindingHealthStatus
type BindingChangePlan = connectionbinding.BindingChangePlan
type TargetBinding = connectionbinding.TargetBinding
type TargetBindingInput = connectionbinding.TargetBindingInput
type TargetBindingConfiguration = connectionbinding.TargetBindingConfiguration
type UpdateConfigurationRequest = connectionbinding.UpdateConfigurationRequest
type AuthenticationMode = connectionbinding.AuthenticationMode
type EndpointConfig = connectionbinding.EndpointConfig
type CredentialReference = connectionbinding.CredentialReference

var (
	ErrInvalidBinding          = connectionbinding.ErrInvalidBinding
	ErrBindingNotFound         = connectionbinding.ErrBindingNotFound
	ErrIncompatibleBinding     = connectionbinding.ErrIncompatibleBinding
	ErrDisabledBinding         = connectionbinding.ErrDisabledBinding
	ErrUnauthorizedBinding     = connectionbinding.ErrUnauthorizedBinding
	ErrCredentialDenied        = connectionbinding.ErrCredentialDenied
	ErrCredentialNotFound      = connectionbinding.ErrCredentialNotFound
	ErrCredentialRateLimited   = connectionbinding.ErrCredentialRateLimited
	ErrProviderUnavailable     = connectionbinding.ErrProviderUnavailable
	ErrInvalidCredentialBundle = connectionbinding.ErrInvalidCredentialBundle
)

var ParseConnectionID = connectionbinding.ParseConnectionID

const AuthenticationExternalBundle = connectionbinding.AuthenticationExternalBundle

// Administration is the synchronous lifecycle surface consumed by the
// project resource UI. It deliberately carries references, never credential
// secret values.
type Administration interface {
	List(context.Context, string, BindingScope, TargetID) ([]TargetBinding, error)
	Create(context.Context, string, TargetBindingInput) (TargetBinding, error)
	PlanConfigurationChange(context.Context, string, BindingKey, TargetBindingConfiguration) (BindingChangePlan, error)
	UpdateConfiguration(context.Context, UpdateConfigurationRequest) (TargetBinding, error)
	Test(context.Context, string, BindingKey) (BindingHealthStatus, error)
	RefreshNow(context.Context, string, BindingKey) (BindingHealthStatus, error)
	Enable(context.Context, string, BindingKey) (TargetBinding, error)
	Disable(context.Context, string, BindingKey) (TargetBinding, error)
}
