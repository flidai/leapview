package hostinstall

import "github.com/flidai/leapview/internal/platform/releasecontract"

// SourceCompatibility retains the host request wire contract while sharing
// immutable source compatibility with other release adapters.
type SourceCompatibility = releasecontract.SourceCompatibility
