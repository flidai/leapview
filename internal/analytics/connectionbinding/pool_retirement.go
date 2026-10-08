package connectionbinding

import (
	"context"
	"errors"
	"time"
)

// RetireAll is called only while the installation's provider admission is
// closed and all consumer leases have drained. Unlike Close, it preserves the
// directory for later acquisitions under the new immutable credential pins.
// Failed cleanup stays retained and cannot be mistaken for an empty directory.
func (directory *PoolDirectory) RetireAll(ctx context.Context) error {
	if directory == nil {
		return nil
	}
	directory.mu.Lock()
	defer directory.mu.Unlock()
	if directory.closed {
		return ErrProviderUnavailable
	}
	var result error
	for key, pool := range directory.pools {
		if err := pool.manager.RetireBounded(ctx, time.Now().Add(directory.refreshTimeout)); err != nil {
			result = errors.Join(result, err)
		} else {
			delete(directory.pools, key)
		}
	}
	return result
}
