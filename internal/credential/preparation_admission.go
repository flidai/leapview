package credential

import "context"

type preparationScopeKey struct{}
type preparationScope struct {
	gate   *ProviderAdmission
	active bool
}

// preparationContext is deliberately private to the serialized coordinator.
// It admits bounded candidate preparation during recovery without reopening
// ordinary work or carrying any credential identity. All work still receives
// tracked leases; revoke cancels escaped work and prevents new acquisitions.
func (gate *ProviderAdmission) preparationContext(ctx context.Context) (context.Context, func()) {
	scoped, cancel := context.WithCancel(ctx)
	scope := &preparationScope{gate: gate, active: true}
	return context.WithValue(scoped, preparationScopeKey{}, scope), func() {
		gate.mu.Lock()
		scope.active = false
		gate.mu.Unlock()
		cancel()
	}
}
func (gate *ProviderAdmission) preparationAllowed(ctx context.Context) bool {
	scope, _ := ctx.Value(preparationScopeKey{}).(*preparationScope)
	return scope != nil && scope.gate == gate && scope.active
}
func (service *ActivationCoordinator) prepare(ctx context.Context, run func(context.Context) (ActivationRecord, error)) (ActivationRecord, error) {
	scoped, revoke := service.admission.preparationContext(ctx)
	defer revoke()
	return run(scoped)
}
