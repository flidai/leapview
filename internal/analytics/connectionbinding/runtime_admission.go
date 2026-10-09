package connectionbinding

import (
	"context"
	"errors"
)

type RuntimeProviderAdmission interface {
	Acquire(context.Context) (context.Context, func(), error)
}

func (l *RuntimeBindingLeaser) ConfigureProviderAdmission(admission RuntimeProviderAdmission) error {
	if l == nil || admission == nil || l.providerAdmission != nil {
		return ErrProviderUnavailable
	}
	l.providerAdmission = admission
	return nil
}

// Acquire holds provider admission through the candidate's actual pool lease
// cleanup, including probes that still use an external credential provider.
func (l *RuntimeBindingLeaser) Acquire(ctx context.Context, request RuntimeBindingRequest) (*RuntimeBindingLeases, error) {
	if l == nil {
		return nil, ErrProviderUnavailable
	}
	release := func() {}
	var err error
	if l.providerAdmission != nil {
		ctx, release, err = l.providerAdmission.Acquire(ctx)
		if err != nil {
			return nil, err
		}
	}
	leases, err := l.acquireBindings(ctx, request)
	if err != nil {
		if !errors.Is(err, ErrProviderCleanupUncertain) {
			release()
		}
		return nil, err
	}
	leases.providerRelease = release
	return leases, nil
}
