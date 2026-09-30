package module

import "github.com/flidai/leapview/internal/analytics/connectionbinding"

type ConnectionPoolRetirement = connectionbinding.PoolRetirement

// RetireConnectionPool captures an existing exact pool manager without creating
// one. The internal lifecycle caller owns admission pauses, durable authority
// and replacement serialization; this is not an activation command.
func (m *Module) RetireConnectionPool(expected ConnectionTargetBinding) (*ConnectionPoolRetirement, error) {
	if m == nil {
		return nil, connectionbinding.ErrProviderUnavailable
	}
	if err := expected.Validate(); err != nil {
		return nil, err
	}
	if expected.TargetID.String() != m.targetID || expected.Scope.Environment != m.targetEnvironment {
		return nil, connectionbinding.ErrUnauthorizedBinding
	}
	m.connectionPoolsMu.Lock()
	directory, closed := m.connectionPools, m.connectionPoolsClosed
	m.connectionPoolsMu.Unlock()
	if closed {
		return nil, connectionbinding.ErrProviderUnavailable
	}
	if directory == nil {
		return nil, connectionbinding.ErrBindingNotFound
	}
	return directory.RetireBinding(expected)
}
