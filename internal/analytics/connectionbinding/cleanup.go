package connectionbinding

import "errors"

// ErrProviderCleanupUncertain means the runtime cannot prove that an opened
// provider client was destroyed. Admission must stay closed until restart.
var ErrProviderCleanupUncertain = errors.New("provider cleanup was not acknowledged")
