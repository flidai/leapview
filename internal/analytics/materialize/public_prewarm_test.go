package materialize

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/stretchr/testify/require"
)

// These are execution-boundary tests: a public warm request has exactly the
// foreground query identity, with no warmup field in the key. Dashboard-module
// tests separately cover publication resolution and refresh preparation.
func TestPublicPrewarmExecutionCacheContract(t *testing.T) {
	for _, scenario := range []string{"compatible", "private_compatible", "surface_isolation", "policy", "dependency", "unsupported", "store_rejected"} {
		t.Run(scenario, func(t *testing.T) {
			bytes := int64(1 << 20)
			if scenario == "store_rejected" {
				bytes = 1
			}
			pool, err := resultcache.New(resultcache.Limits{RuntimeEntries: 8, RuntimeBytes: bytes, NodeEntries: 8, NodeBytes: bytes})
			require.NoError(t, err)
			defer pool.Close()
			database := &cutoverQualificationDatabase{value: 42}
			runtime := newCutoverQualificationRuntime(t, pool, "public", materializeTestPartition(t, resultidentity.PartitionProduction, ""), cutoverQualificationEvidence(t, '1'), database)
			defer runtime.Close()
			if scenario == "unsupported" {
				runtime.dependencyEvidence = resultidentity.Evidence{}
			}
			request := cutoverQualificationRequest()
			request.Surface = dataquery.SurfacePublicDashboard
			request.PrincipalID = "dashboard_publication:project:test.public"
			if scenario == "private_compatible" {
				request.Surface = dataquery.SurfaceDashboard
			}
			warm, err := runtime.ExecuteDataQuery(context.Background(), request)
			require.NoError(t, err)
			require.Equal(t, int64(42), cutoverQualificationValue(t, warm))
			if scenario != "unsupported" && scenario != "store_rejected" {
				require.Equal(t, 1, pool.Stats().Entries, "warm execution must retain an entry")
			}
			if scenario == "surface_isolation" {
				// Governed private requests carry a different audience policy
				// fingerprint; surface alone is not planner result identity.
				request.Surface = dataquery.SurfaceDashboard
				request.PrincipalID = "authenticated-viewer"
				request.EffectivePolicyFingerprint = materializeTestDigest('7')
			}
			if scenario == "policy" {
				request.EffectivePolicyFingerprint = materializeTestDigest('8')
			}
			if scenario == "dependency" {
				runtime.dependencyEvidence = cutoverQualificationEvidence(t, '2')
			}
			foreground, err := runtime.ExecuteDataQuery(context.Background(), request)
			require.NoError(t, err)
			require.Equal(t, int64(42), cutoverQualificationValue(t, foreground))
			if scenario == "compatible" || scenario == "private_compatible" {
				require.Equal(t, dataquery.CacheHit, foreground.CacheOutcome)
				require.Equal(t, int32(1), database.queries.Load())
			} else {
				require.NotEqual(t, dataquery.CacheHit, foreground.CacheOutcome)
				require.Equal(t, int32(2), database.queries.Load())
			}
			if scenario == "unsupported" || scenario == "store_rejected" {
				require.Zero(t, pool.Stats().Entries)
			}
		})
	}
}
