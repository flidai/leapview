package runtimehost

import "errors"

// Legacy snapshot heartbeats are keyed by their durable lease ID. Factory
// callbacks instead belong to one actual runtime preparation; two runtimes can
// attach the same serving generation without sharing their lease health.
type leaseHealthKey struct {
	snapshotLeaseID string
	runtime         *runtimeLeaseHealth
}

// runtimeLeaseHealth follows ownership from preparation through publication or
// candidate registration, then remains live while readers and cleanup drain.
// Its closed flag and callback updates use the manager lock so late callbacks
// cannot recreate an entry after the runtime has relinquished its resources.
type runtimeLeaseHealth struct {
	manager *Manager
	closed  bool
}

func (h *runtimeLeaseHealth) report(err error) {
	if h == nil || h.manager == nil {
		return
	}
	m := h.manager
	m.mu.Lock()
	if h.closed {
		m.mu.Unlock()
		return
	}
	key := leaseHealthKey{runtime: h}
	if err == nil {
		delete(m.leaseRenewalErrors, key)
	} else {
		m.leaseRenewalErrors[key] = err
	}
	m.mu.Unlock()
	if err != nil && m.onLeaseRenewalFailure != nil {
		m.onLeaseRenewalFailure(err)
	}
}

func (h *runtimeLeaseHealth) close() {
	if h == nil || h.manager == nil {
		return
	}
	m := h.manager
	m.mu.Lock()
	h.closed = true
	delete(m.leaseRenewalErrors, leaseHealthKey{runtime: h})
	m.mu.Unlock()
}

func (m *Manager) LeaseRenewalError() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var errs []error
	for _, e := range m.leaseRenewalErrors {
		errs = append(errs, e)
	}
	if m.current != nil {
		if health, ok := m.current.runtime.(RuntimeLeaseHealth); ok {
			errs = append(errs, health.LeaseRenewalError())
		}
	}
	for _, retired := range m.retired {
		if retired == nil {
			continue
		}
		if health, ok := retired.runtime.(RuntimeLeaseHealth); ok {
			errs = append(errs, health.LeaseRenewalError())
		}
	}
	return errors.Join(errs...)
}
