package runtimehost

import "errors"

// runtimeLeaseHealth follows ownership from preparation through publication or
// candidate registration, then remains live while readers and cleanup drain.
// Its closed flag and callback updates use the manager lock so late callbacks
// cannot recreate an entry after the runtime has relinquished its resources.
type runtimeLeaseHealth struct {
	manager *Manager
	closed  bool
}

func (h *runtimeLeaseHealth) report(err error) {
	if h.update(err) && err != nil && h.manager.onLeaseRenewalFailure != nil {
		h.manager.onLeaseRenewalFailure(err)
	}
}

// update admits a health change only while its resource owner is live. Snapshot
// heartbeats also notify successful renewals, unlike factory failure callbacks.
func (h *runtimeLeaseHealth) update(err error) bool {
	if h == nil || h.manager == nil {
		return false
	}
	m := h.manager
	m.mu.Lock()
	if h.closed {
		m.mu.Unlock()
		return false
	}
	if err == nil {
		delete(m.leaseRenewalErrors, h)
	} else {
		m.leaseRenewalErrors[h] = err
	}
	m.mu.Unlock()
	return true
}

func (h *runtimeLeaseHealth) close() {
	if h == nil || h.manager == nil {
		return
	}
	m := h.manager
	m.mu.Lock()
	h.closed = true
	delete(m.leaseRenewalErrors, h)
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
