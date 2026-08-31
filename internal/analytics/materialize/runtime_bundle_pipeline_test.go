package materialize

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/pkg/arrowresult"
	"github.com/stretchr/testify/require"
)

func TestBundlePipelineCancellationBoundariesReleaseArrowOwnership(t *testing.T) {
	stages := []bundleStage{
		bundleStageGovern,
		bundleStagePlan,
		bundleStageCache,
		bundleStageExecute,
		bundleStageSplitStoreDecode,
		bundleStageTransformObserve,
	}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			database := &bundleCountingDatabase{}
			runtime := bundleCacheRuntime(t, database)
			before := arrowresult.Stats()
			ctx, cancel := context.WithCancel(context.Background())
			ctx = withBundleStageObserver(ctx, func(current bundleStage) {
				if current == stage {
					cancel()
				}
			})
			_, err := runtime.ExecuteDataQueryBundle(ctx, bundleCacheRequests())
			require.ErrorIs(t, err, context.Canceled)
			require.NoError(t, runtime.CloseView())
			require.Eventually(t, func() bool { return before == arrowresult.Stats() }, time.Second, time.Millisecond)
		})
	}
}

func TestBundlePlanningCancellationEmitsCanceledAdmission(t *testing.T) {
	runtime := bundleCacheRuntime(t, &bundleCountingDatabase{})
	defer runtime.CloseView()
	observations := []dataquery.CacheObservation{}
	ctx := dataquery.WithCacheObserver(context.Background(), func(observation dataquery.CacheObservation) {
		observations = append(observations, observation)
	})
	ctx, cancel := context.WithCancel(ctx)
	ctx = withBundleStageObserver(ctx, func(stage bundleStage) {
		if stage == bundleStagePlan {
			cancel()
		}
	})

	_, err := runtime.ExecuteDataQueryBundle(ctx, bundleCacheRequests())
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, len(bundleCacheRequests()), countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationAdmission &&
			observation.Decision == dataquery.CacheAdmissionRejected &&
			observation.AdmissionReason == dataquery.CacheAdmissionReasonCanceled
	}))
	require.Zero(t, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationAdmission && observation.AdmissionReason == dataquery.CacheAdmissionReasonPlanningFailed
	}))
}

func TestBundlePlanningCancellationDuringPlannerCallEmitsCanceledAdmission(t *testing.T) {
	runtime := bundleCacheRuntime(t, &bundleCountingDatabase{})
	defer runtime.CloseView()
	observations := []dataquery.CacheObservation{}
	ctx := dataquery.WithCacheObserver(context.Background(), func(observation dataquery.CacheObservation) {
		observations = append(observations, observation)
	})
	ctx, cancel := context.WithCancel(ctx)
	planningStarted := make(chan struct{})
	releasePlanning := make(chan struct{})
	defer func() {
		select {
		case <-releasePlanning:
		default:
			close(releasePlanning)
		}
	}()
	ctx = withBundleStageObserver(ctx, func(stage bundleStage) {
		if stage == bundleStagePlanCall {
			close(planningStarted)
			<-releasePlanning
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := runtime.ExecuteDataQueryBundle(ctx, bundleCacheRequests())
		done <- err
	}()
	select {
	case <-planningStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bundle planner call")
	}
	cancel()
	close(releasePlanning)
	require.ErrorIs(t, <-done, context.Canceled)
	require.Equal(t, len(bundleCacheRequests()), countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationAdmission &&
			observation.Decision == dataquery.CacheAdmissionRejected &&
			observation.AdmissionReason == dataquery.CacheAdmissionReasonCanceled
	}))
	require.Zero(t, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationAdmission && observation.AdmissionReason == dataquery.CacheAdmissionReasonPlanningFailed
	}))
}

func TestCachePlanningAdmissionReasonDistinguishesCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want dataquery.CacheAdmissionReason
	}{
		{name: "canceled", err: context.Canceled, want: dataquery.CacheAdmissionReasonCanceled},
		{name: "deadline", err: context.DeadlineExceeded, want: dataquery.CacheAdmissionReasonCanceled},
		{name: "planner", err: errors.New("planner failed"), want: dataquery.CacheAdmissionReasonPlanningFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, cachePlanningAdmissionReason(test.err))
		})
	}
}

func TestBundlePipelineTransformFailureIsBranchAttributedAndReleasesArrowOwnership(t *testing.T) {
	database := &bundleCountingDatabase{}
	runtime := bundleCacheRuntime(t, database)
	before := arrowresult.Stats()
	want := errors.New("transform failed")
	governor := failingTransformGovernor{err: want}
	_, err := runtime.ExecuteDataQueryBundle(dataquery.WithGovernor(context.Background(), governor), bundleCacheRequests())
	var branchErr *dataquery.BundleBranchError
	require.ErrorAs(t, err, &branchErr)
	require.Equal(t, "orders", branchErr.ID)
	require.ErrorIs(t, err, want)
	require.Equal(t, int32(1), database.queries.Load())
	require.NoError(t, runtime.CloseView())
	require.Eventually(t, func() bool { return before == arrowresult.Stats() }, time.Second, time.Millisecond)
}

func TestBundlePipelineExecutionFailureRemainsPrimaryWhenTransformAlsoFails(t *testing.T) {
	executeErr := errors.New("execution failed")
	transformErr := errors.New("transform failed")
	database := &bundleExecutionFailureDatabase{err: executeErr}
	runtime := bundleCacheRuntime(t, database)
	defer runtime.CloseView()

	_, err := runtime.ExecuteDataQueryBundle(
		dataquery.WithGovernor(context.Background(), failingTransformGovernor{err: transformErr}),
		bundleCacheRequests(),
	)
	require.ErrorIs(t, err, executeErr)
	require.NotErrorIs(t, err, transformErr)
}

func TestBundlePipelineCanceledSingleMissDoesNotExposeSuccessfulResultToTransform(t *testing.T) {
	database := &bundleCountingDatabase{}
	runtime := bundleCacheRuntime(t, database)
	defer runtime.CloseView()
	requests := bundleCacheRequests()
	require.NoError(t, primeBundleBranch(runtime, requests[0]))
	governor := &canceledResultGovernor{}
	ctx, cancel := context.WithCancel(dataquery.WithGovernor(context.Background(), governor))
	ctx = withBundleStageObserver(ctx, func(stage bundleStage) {
		if stage == bundleStageTransformObserve {
			cancel()
		}
	})

	_, err := runtime.ExecuteDataQueryBundle(ctx, requests)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, governor.sawCanceledSuccess.Load())
}

func TestBundlePipelineAdmitsAndObservesOnePhysicalQuery(t *testing.T) {
	database := &bundleCountingDatabase{}
	runtime := bundleCacheRuntime(t, database)
	defer runtime.CloseView()
	observations := []dataquery.PhysicalQueryObservation{}
	ctx := dataquery.WithPhysicalQueryObserver(context.Background(), func(observation dataquery.PhysicalQueryObservation) {
		observations = append(observations, observation)
	})
	result, err := runtime.ExecuteDataQueryBundle(ctx, bundleCacheRequests())
	require.NoError(t, err)
	require.Len(t, observations, 1)
	require.Equal(t, 1, observations[0].Count)
	require.Equal(t, int32(1), database.queries.Load())
	require.Equal(t, dataquery.CacheMiss, result.Results["orders"].CacheOutcome)
	require.Equal(t, dataquery.CacheMiss, result.Results["events"].CacheOutcome)
}

type failingTransformGovernor struct{ err error }

func (governor failingTransformGovernor) GovernDataQuery(_ context.Context, request dataquery.Query) (dataquery.Query, dataquery.ResultTransformer, error) {
	return request, func(*dataquery.Result, error) error { return governor.err }, nil
}

type bundleExecutionFailureDatabase struct {
	bundleCountingDatabase
	err error
}

func (d *bundleExecutionFailureDatabase) QueryArrow(_ context.Context, _ semanticquery.Plan, _ arrowquery.Sink) error {
	d.queries.Add(1)
	return d.err
}

type canceledResultGovernor struct {
	sawCanceledSuccess atomic.Bool
}

func (governor *canceledResultGovernor) GovernDataQuery(_ context.Context, request dataquery.Query) (dataquery.Query, dataquery.ResultTransformer, error) {
	isSingleMiss := len(request.Metrics) == 1 && request.Metrics[0].Field == "event_count"
	return request, func(result *dataquery.Result, err error) error {
		if isSingleMiss && errors.Is(err, context.Canceled) && result.Status == dataquery.StatusSuccess {
			governor.sawCanceledSuccess.Store(true)
		}
		return nil
	}, nil
}

func primeBundleBranch(runtime *Runtime, branch dataquery.BundleRequest) error {
	_, err := runtime.ExecuteDataQuery(context.Background(), branch.Query)
	return err
}
