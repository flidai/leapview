package agent

import "context"

// ProviderAdmission protects credential-backed model construction and provider
// calls. A canceled context does not release its lease: the provider and final
// run cleanup must actually return before release is acknowledged.
type ProviderAdmission interface {
	Acquire(context.Context) (context.Context, func(), error)
	Wait(context.Context) (context.Context, func(), error)
}

func WithProviderAdmission(admission ProviderAdmission) ServiceOption {
	return func(service *Service) { service.providerAdmission = admission }
}

// ConfigureProviderAdmission is called during composition, before exposing a
// preconstructed service to requests or background dispatchers.
func (s *Service) ConfigureProviderAdmission(admission ProviderAdmission) {
	if s != nil {
		s.providerAdmission = admission
	}
}

func (s *Service) acquireProvider(ctx context.Context, wait bool) (context.Context, func(), error) {
	if s.providerAdmission == nil {
		return ctx, func() {}, nil
	}
	if wait {
		return s.providerAdmission.Wait(ctx)
	}
	return s.providerAdmission.Acquire(ctx)
}
