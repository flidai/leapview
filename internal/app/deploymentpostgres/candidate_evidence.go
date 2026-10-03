package deploymentpostgres

import nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"

// NativeReader candidate metadata aliases keep app composition on the narrow
// deployment adapter instead of importing the PostgreSQL implementation.
type CandidateGenerationResolution = nativepostgres.CandidateGenerationResolution
type DeliveryCandidate = nativepostgres.DeliveryCandidate
type SnapshotSeal = nativepostgres.SnapshotSeal
