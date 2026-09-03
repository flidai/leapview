// Package resulttier defines the small contract shared by persistent query
// result tiers.  Cache identity remains owned by analytics/cache; a tier only
// stores immutable Arrow results and generation-neutral result metadata.
package resulttier

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/cache"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/pkg/arrowresult"
)

// Tier is an optional reusable query-result store (for example a node-local
// disk tier or shared object storage).
//
// On a hit Lookup transfers ownership of one *arrowresult.Result reference to
// the caller and returns a non-nil Admission for that exact durable value. The
// caller must call Result.Release exactly once. A miss is represented by
// found=false with nil result and admission. Implementations must treat errors
// and malformed entries as safe misses where possible; materialize deliberately
// fails open around this interface.
//
// Store borrows result and never consumes or releases the caller's reference.
// Metadata must be generation-neutral; in particular, diagnostic SQL must not
// be persisted by materialize callers.
type Tier interface {
	Lookup(context.Context, cache.Key) (*arrowresult.Result, resultcache.Metadata, Admission, bool, error)
	Store(context.Context, cache.Key, *arrowresult.Result, resultcache.Metadata) error
}

// Admission is the exact authority capability returned with a durable tier
// hit.  A caller may reject the admitted value after validating semantics; the
// implementation must retire only the manifest represented by this admission,
// never a key's current replacement.
type Admission interface {
	Reject(context.Context, string) error
}
