package materialize

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
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

func TestResultEquivalenceAuthorizationProjectionIsSharedByScalarAndBundle(t *testing.T) {
	base := "sha256:" + strings.Repeat("a", 64)
	firstRequest := dataquery.Query{AuthorizationFields: []dataquery.Field{
		{Field: "orders.customer_email", Alias: "email"},
		{Field: "orders.customer_id", Alias: "id"},
		{Field: "orders.customer_email", Alias: "duplicate"},
	}}
	// Aliases, declaration order, and duplicate entries are not authorization
	// semantics. Only the canonical field-name set should affect the identity.
	sameRequest := dataquery.Query{AuthorizationFields: []dataquery.Field{
		{Field: "orders.customer_id", Alias: "renamed"},
		{Field: "orders.customer_email", Alias: "other"},
	}}
	differentRequest := dataquery.Query{AuthorizationFields: []dataquery.Field{{Field: "orders.customer_name"}}}

	first := materializeResultEquivalenceDigest(base, firstRequest)
	same := materializeResultEquivalenceDigest(base, sameRequest)
	different := materializeResultEquivalenceDigest(base, differentRequest)
	require.NotEqual(t, base, first)
	require.Equal(t, first, same)
	require.NotEqual(t, first, different)

	plan := semanticquery.BundlePlan{Branches: []semanticquery.BundleBranch{{ID: "orders", ResultEquivalenceDigest: base}}}
	require.Equal(t, first, branchResultEquivalenceDigest(plan, "orders", firstRequest))
	require.Equal(t, same, branchResultEquivalenceDigest(plan, "orders", sameRequest))
	require.NotEqual(t, first, branchResultEquivalenceDigest(plan, "orders", differentRequest))
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

func TestBundleDecimalMetadataSurvivesMissAndCacheHit(t *testing.T) {
	database := &decimalBundleDatabase{}
	runtime := bundleCacheRuntime(t, database)
	defer runtime.CloseView()
	for _, outcome := range []string{dataquery.CacheMiss, dataquery.CacheHit} {
		result, err := runtime.ExecuteDataQueryBundle(context.Background(), bundleCacheRequests())
		require.NoError(t, err)
		require.Len(t, result.Results, 2)
		for _, branch := range result.Results {
			require.Equal(t, outcome, branch.CacheOutcome)
			require.Equal(t, []dataquery.Column{{Name: "value", DecimalPrecision: 38, DecimalScale: 4}}, branch.Columns)
			require.Equal(t, []dataquery.Row{{"value": "12345678901234567890.1234"}}, branch.Rows)
		}
	}
	require.Equal(t, int32(1), database.queries.Load())
}

type decimalBundleDatabase struct {
	bundleCountingDatabase
}

func (d *decimalBundleDatabase) QueryArrow(_ context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	d.queries.Add(1)
	fields := make([]arrow.Field, len(plan.Columns))
	for index, name := range plan.Columns {
		fields[index] = arrow.Field{Name: name, Type: &arrow.Decimal128Type{Precision: 38, Scale: 4}}
		if name == semanticquery.BundleBranchColumn {
			fields[index].Type = arrow.PrimitiveTypes.Int64
		}
	}
	schema := arrow.NewSchema(fields, nil)
	builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer builder.Release()
	value, err := decimal128.FromString("12345678901234567890.1234", 38, 4)
	if err != nil {
		return err
	}
	for ordinal := int64(0); ordinal < 2; ordinal++ {
		for index, name := range plan.Columns {
			if name == semanticquery.BundleBranchColumn {
				builder.Field(index).(*array.Int64Builder).Append(ordinal)
			} else {
				builder.Field(index).(*array.Decimal128Builder).Append(value)
			}
		}
	}
	record := builder.NewRecordBatch()
	defer record.Release()
	if err := sink.WriteSchema(schema); err != nil {
		return err
	}
	return sink.WriteRecord(record)
}
