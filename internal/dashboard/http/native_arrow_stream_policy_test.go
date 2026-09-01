package http

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	stdhttp "net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/flidai/leapview/internal/workload"
)

func TestDashboardNativeArrowStreamPolicyLocksCapacityAndDeadlines(t *testing.T) {
	t.Parallel()
	policy := defaultDashboardNativeArrowStreamPolicy(2)
	if policy.InstanceLimit != 1 || policy.PrincipalLimit != 1 || policy.ProjectLimit != 1 {
		t.Fatalf("initial stream limits = %d/%d/%d, want 1/1/1", policy.InstanceLimit, policy.PrincipalLimit, policy.ProjectLimit)
	}
	if policy.MaximumLifetime != 30*time.Second || policy.IdleWriteTimeout != 5*time.Second ||
		policy.CleanupBound != 2*time.Second || policy.CleanupP95Target != time.Second {
		t.Fatalf("stream deadlines = %#v", policy)
	}
	for connections, want := range map[int]int{1: 0, 2: 1, 3: 1, 4: 1, 8: 2, 20: 5} {
		if got := dashboardNativeArrowStreamPoolLimit(connections); got != want {
			t.Fatalf("pool limit for %d connections = %d, want %d", connections, got, want)
		}
	}
	disabled, err := newDashboardNativeArrowStreamCapacity(defaultDashboardNativeArrowStreamPolicy(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.acquire(context.Background(), "principal-a", "project-a", false); dashboardNativeArrowStreamRejectionReasonOf(err) != dashboardNativeArrowStreamDisabled {
		t.Fatalf("one-connection rejection = %v", err)
	}
	invalid := defaultDashboardNativeArrowStreamPolicy(4)
	invalid.InstanceLimit = 2
	invalid.PrincipalLimit = 2
	invalid.ProjectLimit = 2
	if _, err := newDashboardNativeArrowStreamCapacity(invalid); err == nil {
		t.Fatal("stream limit above the 25 percent pool guard was accepted")
	}
}

func TestDashboardNativeArrowStreamRequiresAuditAndObservability(t *testing.T) {
	t.Parallel()
	fixture := newDashboardNativeArrowStreamFixture(t, dashboardNativeArrowTestStreamPolicy(), &dashboardNativeArrowTestEvents{}, 2)
	dependencies := fixture.dependencies()
	dependencies.Observer = nil
	_, err := runDashboardNativeArrowStream(context.Background(), dependencies, dashboardNativeArrowStreamRequest{
		PrincipalID: "principal-a", ProjectID: "project-a", Writer: fixture.writer,
		PublishCursor: func(context.Context, string) error { return nil },
	}, func(context.Context, *dashboardNativeArrowStream) (string, error) {
		t.Fatal("stream ran without required observability")
		return "", nil
	})
	if err == nil || fixture.admitter.calls.Load() != 0 || fixture.database.acquires.Load() != 0 {
		t.Fatalf("missing observability = err %v admission %d database %d", err, fixture.admitter.calls.Load(), fixture.database.acquires.Load())
	}
}

func TestDashboardNativeArrowStreamCapacityIsNonQueuingAndActorScoped(t *testing.T) {
	t.Parallel()
	policy := dashboardNativeArrowTestStreamPolicy()
	policy.AnalyticalConnections = 8
	policy.InstanceLimit = 2
	policy.PrincipalLimit = 1
	policy.ProjectLimit = 1
	capacity, err := newDashboardNativeArrowStreamCapacity(policy)
	if err != nil {
		t.Fatal(err)
	}
	one, err := capacity.acquire(context.Background(), "principal-a", "project-a", false)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := capacity.acquire(context.Background(), "principal-a", "project-b", false); dashboardNativeArrowStreamRejectionReasonOf(err) != dashboardNativeArrowStreamPrincipalLimit {
		t.Fatalf("principal rejection = %v", err)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("principal capacity rejection queued")
	}
	if _, err := capacity.acquire(context.Background(), "principal-b", "project-a", false); dashboardNativeArrowStreamRejectionReasonOf(err) != dashboardNativeArrowStreamProjectLimit {
		t.Fatalf("project rejection = %v", err)
	}
	two, err := capacity.acquire(context.Background(), "principal-b", "project-b", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capacity.acquire(context.Background(), "principal-c", "project-c", false); dashboardNativeArrowStreamRejectionReasonOf(err) != dashboardNativeArrowStreamInstanceLimit {
		t.Fatalf("instance rejection = %v", err)
	}
	two.Release()
	one.Release()
	if capacity.active != 0 || len(capacity.principals) != 0 || len(capacity.projects) != 0 {
		t.Fatalf("capacity leaked: active=%d principals=%v projects=%v", capacity.active, capacity.principals, capacity.projects)
	}

	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	admission, err := controller.Acquire(context.Background(), workload.Request{
		Class: workload.Interactive, PrincipalID: "principal-a", Operation: "outer", EstimatedMemoryBytes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()
	if _, err := capacity.acquire(admission.Context(), "principal-a", "project-a", true); dashboardNativeArrowStreamRejectionReasonOf(err) != dashboardNativeArrowStreamNestedAdmission {
		t.Fatalf("nested admission rejection = %v", err)
	}
	fixture := newDashboardNativeArrowStreamFixture(t, dashboardNativeArrowTestStreamPolicy(), &dashboardNativeArrowTestEvents{}, 2)
	_, err = runDashboardNativeArrowStream(admission.Context(), fixture.dependencies(), dashboardNativeArrowStreamRequest{
		PrincipalID: "principal-a", ProjectID: "project-a", Writer: fixture.writer,
		PublishCursor: func(context.Context, string) error { return nil },
	}, func(context.Context, *dashboardNativeArrowStream) (string, error) {
		t.Fatal("nested stream reached execution")
		return "", nil
	})
	if dashboardNativeArrowStreamRejectionReasonOf(err) != dashboardNativeArrowStreamNestedAdmission || fixture.admitter.calls.Load() != 0 || fixture.database.acquires.Load() != 0 {
		t.Fatalf("nested lifecycle rejection = %v admission=%d database=%d", err, fixture.admitter.calls.Load(), fixture.database.acquires.Load())
	}
}

func TestDashboardNativeArrowStreamLifecycleFastEmptyAndMultiBatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		batches    int
		rows       int
		probe      int
		cursor     string
		writeBytes int
	}{
		{name: "fast client", batches: 1, rows: 50, cursor: "d3.next", writeBytes: 256},
		{name: "empty result", batches: 0, writeBytes: 64},
		{name: "large multi-batch result", batches: 32, rows: 64, probe: 1, cursor: "d3.next", writeBytes: 4096},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := &dashboardNativeArrowTestEvents{}
			fixture := newDashboardNativeArrowStreamFixture(t, dashboardNativeArrowTestStreamPolicy(), events, 2)
			ctx := dataquery.WithResultBudget(context.Background(), dataquery.ResultLimits{MaxRows: 10_000, MaxBytes: 16 << 20})
			published := "not-called"
			observation, err := runDashboardNativeArrowStream(ctx, fixture.dependencies(), dashboardNativeArrowStreamRequest{
				PrincipalID: "principal-a", ProjectID: "project-a",
				Writer: fixture.writer, PublishCursor: func(_ context.Context, cursor string) error {
					events.add("cursor")
					published = cursor
					return nil
				},
			}, func(_ context.Context, stream *dashboardNativeArrowStream) (string, error) {
				reader := &dashboardNativeArrowTestReader{events: events, active: &fixture.activeReaders}
				fixture.activeReaders.Add(1)
				if err := stream.RegisterReader(reader); err != nil {
					return "", err
				}
				if err := stream.ChargeSchema(48, 16); err != nil {
					return "", err
				}
				payload := bytes.Repeat([]byte{0x42}, test.writeBytes/max(1, test.batches))
				if test.batches == 0 {
					if _, err := stream.Writer().Write(payload); err != nil {
						return "", err
					}
				}
				for batch := 0; batch < test.batches; batch++ {
					probe := 0
					if batch == test.batches-1 {
						probe = test.probe
					}
					if err := stream.ChargeBatch(test.rows, probe, int64(len(payload))); err != nil {
						return "", err
					}
					if _, err := stream.Writer().Write(payload); err != nil {
						return "", err
					}
				}
				events.add("ipc-close")
				if err := stream.MarkIPCClosed(); err != nil {
					return "", err
				}
				return test.cursor, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !observation.Success || observation.RowsEmitted != int64(test.rows*test.batches) || observation.ProbeRows != int64(test.probe) || observation.IPCBytes <= 0 {
				t.Fatalf("observation = %#v", observation)
			}
			if published != test.cursor {
				t.Fatalf("published cursor = %q, want %q", published, test.cursor)
			}
			terminal := fixture.terminal.last()
			if !terminal.Success || !terminal.CursorPublished || terminal.Err != nil {
				t.Fatalf("terminal success audit = %#v", terminal)
			}
			if terminal.PrincipalID != "principal-a" || terminal.ProjectID != "project-a" {
				t.Fatalf("terminal audit identity = %q/%q", terminal.PrincipalID, terminal.ProjectID)
			}
			fixture.assertReleased(t)
			if got := events.snapshot(); !dashboardNativeArrowEventsOrdered(got, "ipc-close", "reader", "cursor", "terminal", "database", "admission", "serving", "observe") {
				t.Fatalf("lifecycle events = %v", got)
			}
		})
	}
}

func TestDashboardNativeArrowStreamFailuresNeverPublishCursor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		operation func(context.Context, *dashboardNativeArrowStream, *dashboardNativeArrowStreamFixture) error
		committed bool
	}{
		{name: "client disconnect", committed: true, operation: func(_ context.Context, stream *dashboardNativeArrowStream, fixture *dashboardNativeArrowStreamFixture) error {
			if _, err := stream.Writer().Write([]byte("schema")); err != nil {
				return err
			}
			fixture.writer.fail = io.ErrClosedPipe
			_, err := stream.Writer().Write([]byte("record"))
			return err
		}},
		{name: "cancellation after commitment", committed: true, operation: func(_ context.Context, stream *dashboardNativeArrowStream, fixture *dashboardNativeArrowStreamFixture) error {
			if _, err := stream.Writer().Write([]byte("schema")); err != nil {
				return err
			}
			fixture.cancel()
			_, err := stream.Writer().Write([]byte("record"))
			return err
		}},
		{name: "IPC failure", committed: true, operation: func(_ context.Context, stream *dashboardNativeArrowStream, _ *dashboardNativeArrowStreamFixture) error {
			_, _ = stream.Writer().Write([]byte("schema"))
			return errors.New("close IPC")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := &dashboardNativeArrowTestEvents{}
			fixture := newDashboardNativeArrowStreamFixture(t, dashboardNativeArrowTestStreamPolicy(), events, 2)
			ctx, cancel := context.WithCancel(context.Background())
			fixture.cancel = cancel
			defer cancel()
			published := atomic.Int64{}
			observation, err := runDashboardNativeArrowStream(ctx, fixture.dependencies(), dashboardNativeArrowStreamRequest{
				PrincipalID: "principal-a", ProjectID: "project-a", Writer: fixture.writer,
				PublishCursor: func(context.Context, string) error { published.Add(1); return nil },
			}, func(ctx context.Context, stream *dashboardNativeArrowStream) (string, error) {
				reader := &dashboardNativeArrowTestReader{events: events, active: &fixture.activeReaders}
				fixture.activeReaders.Add(1)
				if err := stream.RegisterReader(reader); err != nil {
					return "", err
				}
				return "d3.must-not-publish", test.operation(ctx, stream, fixture)
			})
			if err == nil || published.Load() != 0 {
				t.Fatalf("failure/cursor = %v/%d", err, published.Load())
			}
			if observation.PostCommitAbort != test.committed || observation.Success {
				t.Fatalf("observation = %#v", observation)
			}
			terminal := fixture.terminal.last()
			if terminal.Success || terminal.CursorPublished || terminal.Err == nil {
				t.Fatalf("terminal failure audit = %#v", terminal)
			}
			fixture.assertReleased(t)
		})
	}
}

func TestDashboardNativeArrowStreamCancellationBeforeCommitSkipsResources(t *testing.T) {
	t.Parallel()
	fixture := newDashboardNativeArrowStreamFixture(t, dashboardNativeArrowTestStreamPolicy(), &dashboardNativeArrowTestEvents{}, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	published := atomic.Int64{}
	_, err := runDashboardNativeArrowStream(ctx, fixture.dependencies(), dashboardNativeArrowStreamRequest{
		PrincipalID: "principal-a", ProjectID: "project-a", Writer: fixture.writer,
		PublishCursor: func(context.Context, string) error { published.Add(1); return nil },
	}, func(context.Context, *dashboardNativeArrowStream) (string, error) {
		t.Fatal("operation ran after pre-commit cancellation")
		return "", nil
	})
	if !errors.Is(err, context.Canceled) || published.Load() != 0 || fixture.admitter.calls.Load() != 0 || fixture.database.acquires.Load() != 0 {
		t.Fatalf("pre-commit cancellation = err %v publish %d admission %d database %d", err, published.Load(), fixture.admitter.calls.Load(), fixture.database.acquires.Load())
	}
}

func TestDashboardNativeArrowStreamIdleDeadlineInterruptsSocketWrite(t *testing.T) {
	t.Parallel()
	policy := dashboardNativeArrowTestStreamPolicy()
	policy.IdleWriteTimeout = 40 * time.Millisecond
	policy.MaximumLifetime = time.Second
	events := &dashboardNativeArrowTestEvents{}
	fixture := newDashboardNativeArrowStreamFixture(t, policy, events, 2)
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	socketWriter := &dashboardNativeArrowSocketWriter{header: make(stdhttp.Header), connection: server}
	started := time.Now()
	published := atomic.Int64{}
	observation, err := runDashboardNativeArrowStream(context.Background(), fixture.dependencies(), dashboardNativeArrowStreamRequest{
		PrincipalID: "principal-a", ProjectID: "project-a", Writer: socketWriter,
		PublishCursor: func(context.Context, string) error { published.Add(1); return nil },
	}, func(_ context.Context, stream *dashboardNativeArrowStream) (string, error) {
		_, err := stream.Writer().Write(bytes.Repeat([]byte{1}, 1024))
		return "d3.must-not-publish", err
	})
	if !errors.Is(err, errDashboardNativeArrowStreamIdle) || published.Load() != 0 {
		t.Fatalf("idle socket result = %v cursor=%d", err, published.Load())
	}
	if elapsed := time.Since(started); elapsed < policy.IdleWriteTimeout || elapsed > 500*time.Millisecond {
		t.Fatalf("idle socket interruption took %s", elapsed)
	}
	if observation.TimeoutReason != "idle_write_timeout" || observation.CancellationCleanupLatency <= 0 {
		t.Fatalf("idle observation = %#v", observation)
	}
	fixture.assertReleased(t)
}

func TestDashboardNativeArrowStreamHardLimitStopsContinuouslyProgressingClient(t *testing.T) {
	t.Parallel()
	policy := dashboardNativeArrowTestStreamPolicy()
	policy.IdleWriteTimeout = 40 * time.Millisecond
	policy.MaximumLifetime = 80 * time.Millisecond
	fixture := newDashboardNativeArrowStreamFixture(t, policy, &dashboardNativeArrowTestEvents{}, 2)
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	socketWriter := &dashboardNativeArrowSocketWriter{header: make(stdhttp.Header), connection: server}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buffer := make([]byte, len("progress"))
		for {
			time.Sleep(10 * time.Millisecond)
			if _, err := io.ReadFull(client, buffer); err != nil {
				return
			}
		}
	}()
	published := atomic.Int64{}
	observation, err := runDashboardNativeArrowStream(context.Background(), fixture.dependencies(), dashboardNativeArrowStreamRequest{
		PrincipalID: "principal-a", ProjectID: "project-a", Writer: socketWriter,
		PublishCursor: func(context.Context, string) error { published.Add(1); return nil },
	}, func(_ context.Context, stream *dashboardNativeArrowStream) (string, error) {
		for {
			if _, err := stream.Writer().Write([]byte("progress")); err != nil {
				return "d3.must-not-publish", err
			}
		}
	})
	if !errors.Is(err, errDashboardNativeArrowStreamHardLimit) || published.Load() != 0 || observation.TimeoutReason != "maximum_lifetime" {
		t.Fatalf("hard-limit result = %v cursor=%d observation=%#v", err, published.Load(), observation)
	}
	if observation.IPCBytes == 0 || !observation.PostCommitAbort {
		t.Fatalf("continuously progressing client observation = %#v", observation)
	}
	_ = client.Close()
	<-readerDone
	fixture.assertReleased(t)
}

func TestDashboardNativeArrowStreamEarlierDeadlineWins(t *testing.T) {
	t.Parallel()
	policy := dashboardNativeArrowTestStreamPolicy()
	policy.MaximumLifetime = time.Second
	policy.IdleWriteTimeout = time.Second
	fixture := newDashboardNativeArrowStreamFixture(t, policy, &dashboardNativeArrowTestEvents{}, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	observation, err := runDashboardNativeArrowStream(ctx, fixture.dependencies(), dashboardNativeArrowStreamRequest{
		PrincipalID: "principal-a", ProjectID: "project-a", Writer: fixture.writer,
		PublishCursor: func(context.Context, string) error { return nil },
	}, func(ctx context.Context, _ *dashboardNativeArrowStream) (string, error) {
		<-ctx.Done()
		return "", context.Cause(ctx)
	})
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errDashboardNativeArrowStreamHardLimit) || observation.TimeoutReason != "request_deadline" {
		t.Fatalf("earlier deadline result = %v observation=%#v", err, observation)
	}
	fixture.assertReleased(t)
}

func TestDashboardNativeArrowStreamAdmissionRejectsBeforeDatabaseAcquisition(t *testing.T) {
	t.Parallel()
	fixture := newDashboardNativeArrowStreamFixture(t, dashboardNativeArrowTestStreamPolicy(), &dashboardNativeArrowTestEvents{}, 2)
	fixture.admitter.reject = errors.New("admission exhausted")
	published := atomic.Int64{}
	_, err := runDashboardNativeArrowStream(context.Background(), fixture.dependencies(), dashboardNativeArrowStreamRequest{
		PrincipalID: "principal-a", ProjectID: "project-a", Writer: fixture.writer,
		PublishCursor: func(context.Context, string) error { published.Add(1); return nil },
	}, func(context.Context, *dashboardNativeArrowStream) (string, error) {
		t.Fatal("operation ran after admission rejection")
		return "", nil
	})
	if !errors.Is(err, fixture.admitter.reject) || fixture.serving.acquires.Load() != 0 || fixture.database.acquires.Load() != 0 || published.Load() != 0 {
		t.Fatalf("admission rejection = %v serving=%d database=%d cursor=%d", err, fixture.serving.acquires.Load(), fixture.database.acquires.Load(), published.Load())
	}
	if fixture.capacity.active != 0 {
		t.Fatalf("stream slot leaked after admission rejection: %d", fixture.capacity.active)
	}
}

func TestDashboardNativeArrowStreamTwoConnectionPoolPreservesOrdinarySlot(t *testing.T) {
	policy := dashboardNativeArrowTestStreamPolicy()
	fixture := newDashboardNativeArrowStreamFixture(t, policy, &dashboardNativeArrowTestEvents{}, 2)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := runDashboardNativeArrowStream(context.Background(), fixture.dependencies(), dashboardNativeArrowStreamRequest{
			PrincipalID: "principal-a", ProjectID: "project-a", Writer: fixture.writer,
			PublishCursor: func(context.Context, string) error { return nil },
		}, func(_ context.Context, stream *dashboardNativeArrowStream) (string, error) {
			close(entered)
			<-release
			if _, err := stream.Writer().Write([]byte("stream")); err != nil {
				return "", err
			}
			return "", stream.MarkIPCClosed()
		})
		done <- err
	}()
	<-entered
	ordinary, err := fixture.database.Acquire(context.Background())
	if err != nil {
		t.Fatalf("ordinary query could not use reserved connection: %v", err)
	}
	thirdContext, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := fixture.database.Acquire(thirdContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third connection acquisition = %v, want deadline", err)
	}
	ordinary.Release()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	fixture.assertReleased(t)
}

func TestDashboardNativeArrowStreamBudgetsCoverSchemaMetadataProbeAndWire(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		limits    dataquery.ResultLimits
		operation func(*dashboardNativeArrowStream) error
		committed bool
	}{
		{name: "schema", limits: dataquery.ResultLimits{MaxRows: 10, MaxBytes: 4}, operation: func(stream *dashboardNativeArrowStream) error {
			return stream.ChargeSchema(5, 0)
		}},
		{name: "metadata", limits: dataquery.ResultLimits{MaxRows: 10, MaxBytes: 4}, operation: func(stream *dashboardNativeArrowStream) error {
			return stream.ChargeSchema(1, 4)
		}},
		{name: "probe row", limits: dataquery.ResultLimits{MaxRows: 1, MaxBytes: 1024}, operation: func(stream *dashboardNativeArrowStream) error {
			return stream.ChargeBatch(1, 1, 8)
		}},
		{name: "wire bytes", limits: dataquery.ResultLimits{MaxRows: 10, MaxBytes: 4}, committed: true, operation: func(stream *dashboardNativeArrowStream) error {
			_, err := stream.Writer().Write([]byte("12345"))
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDashboardNativeArrowStreamFixture(t, dashboardNativeArrowTestStreamPolicy(), &dashboardNativeArrowTestEvents{}, 2)
			ctx := dataquery.WithResultBudget(context.Background(), test.limits)
			published := atomic.Int64{}
			observation, err := runDashboardNativeArrowStream(ctx, fixture.dependencies(), dashboardNativeArrowStreamRequest{
				PrincipalID: "principal-a", ProjectID: "project-a", Writer: fixture.writer,
				PublishCursor: func(context.Context, string) error { published.Add(1); return nil },
			}, func(_ context.Context, stream *dashboardNativeArrowStream) (string, error) {
				return "d3.must-not-publish", test.operation(stream)
			})
			var limit *dataquery.ResultLimitError
			if !errors.As(err, &limit) || published.Load() != 0 || observation.PostCommitAbort != test.committed {
				t.Fatalf("budget result = %v cursor=%d observation=%#v", err, published.Load(), observation)
			}
			fixture.assertReleased(t)
		})
	}
}

func TestDashboardNativeArrowStreamServingGenerationStaysLeasedDuringBlockedStream(t *testing.T) {
	policy := dashboardNativeArrowTestStreamPolicy()
	fixture := newDashboardNativeArrowStreamFixture(t, policy, &dashboardNativeArrowTestEvents{}, 2)
	blocked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := runDashboardNativeArrowStream(context.Background(), fixture.dependencies(), dashboardNativeArrowStreamRequest{
			PrincipalID: "principal-a", ProjectID: "project-a", Writer: fixture.writer,
			PublishCursor: func(context.Context, string) error { return nil },
		}, func(_ context.Context, stream *dashboardNativeArrowStream) (string, error) {
			close(blocked)
			<-release
			if _, err := stream.Writer().Write([]byte("complete")); err != nil {
				return "", err
			}
			return "", stream.MarkIPCClosed()
		})
		done <- err
	}()
	<-blocked
	if fixture.serving.active.Load() != 1 || fixture.database.active.Load() != 1 || fixture.admitter.active.Load() != 1 {
		t.Fatalf("blocked stream resources = serving %d database %d admission %d", fixture.serving.active.Load(), fixture.database.active.Load(), fixture.admitter.active.Load())
	}
	fixture.serving.cutover("generation-b")
	replacement, err := fixture.serving.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Identity().GenerationID != "generation-b" || fixture.serving.activeFor("generation-a") != 1 {
		t.Fatalf("cutover identity/old leases = %q/%d", replacement.Identity().GenerationID, fixture.serving.activeFor("generation-a"))
	}
	replacement.Release()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if fixture.serving.activeFor("generation-a") != 0 {
		t.Fatalf("retired generation lease remains active: %d", fixture.serving.activeFor("generation-a"))
	}
	fixture.assertReleased(t)
}

func TestDashboardNativeArrowStreamReportsCleanupBoundViolation(t *testing.T) {
	t.Parallel()
	policy := dashboardNativeArrowTestStreamPolicy()
	policy.CleanupBound = 10 * time.Millisecond
	policy.CleanupP95Target = 5 * time.Millisecond
	fixture := newDashboardNativeArrowStreamFixture(t, policy, &dashboardNativeArrowTestEvents{}, 2)
	observation, err := runDashboardNativeArrowStream(context.Background(), fixture.dependencies(), dashboardNativeArrowStreamRequest{
		PrincipalID: "principal-a", ProjectID: "project-a", Writer: fixture.writer,
		PublishCursor: func(context.Context, string) error { return nil },
	}, func(_ context.Context, stream *dashboardNativeArrowStream) (string, error) {
		fixture.activeReaders.Add(1)
		if err := stream.RegisterReader(&dashboardNativeArrowTestReader{active: &fixture.activeReaders, delay: 20 * time.Millisecond}); err != nil {
			return "", err
		}
		if _, err := stream.Writer().Write([]byte("complete")); err != nil {
			return "", err
		}
		return "", stream.MarkIPCClosed()
	})
	if err != nil {
		t.Fatal(err)
	}
	if !observation.CleanupBoundExceeded || observation.CleanupDuration < 20*time.Millisecond {
		t.Fatalf("cleanup-bound observation = %#v", observation)
	}
	if fixture.capacity.active != 0 || fixture.admitter.active.Load() != 0 || fixture.serving.active.Load() != 0 || fixture.database.active.Load() != 0 || fixture.activeReaders.Load() != 0 {
		t.Fatalf("slow cleanup leaked resources: %#v", observation)
	}
}

func TestDashboardNativeArrowStreamPanicCannotRecordSuccessOrLeak(t *testing.T) {
	t.Parallel()
	fixture := newDashboardNativeArrowStreamFixture(t, dashboardNativeArrowTestStreamPolicy(), &dashboardNativeArrowTestEvents{}, 2)
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		_, _ = runDashboardNativeArrowStream(context.Background(), fixture.dependencies(), dashboardNativeArrowStreamRequest{
			PrincipalID: "principal-a", ProjectID: "project-a", Writer: fixture.writer,
			PublishCursor: func(context.Context, string) error { return nil },
		}, func(_ context.Context, stream *dashboardNativeArrowStream) (string, error) {
			_, _ = stream.Writer().Write([]byte("partial"))
			panic("fixture panic")
		})
	}()
	if !panicked {
		t.Fatal("stream panic was swallowed")
	}
	terminal := fixture.terminal.last()
	if terminal.Success || terminal.CursorPublished || terminal.Err == nil {
		t.Fatalf("panic recorded successful terminal audit: %#v", terminal)
	}
	fixture.assertReleased(t)
}

func dashboardNativeArrowTestStreamPolicy() dashboardNativeArrowStreamPolicy {
	policy := defaultDashboardNativeArrowStreamPolicy(2)
	policy.MaximumLifetime = 2 * time.Second
	policy.IdleWriteTimeout = 500 * time.Millisecond
	policy.CleanupBound = time.Second
	policy.CleanupP95Target = 500 * time.Millisecond
	return policy
}

func dashboardNativeArrowStreamRejectionReasonOf(err error) dashboardNativeArrowStreamRejectionReason {
	var rejection *dashboardNativeArrowStreamRejection
	if errors.As(err, &rejection) {
		return rejection.Reason
	}
	return ""
}

type dashboardNativeArrowStreamFixture struct {
	capacity      *dashboardNativeArrowStreamCapacity
	admitter      *dashboardNativeArrowTestAdmitter
	serving       *dashboardNativeArrowTestServingProvider
	database      *dashboardNativeArrowTestDatabaseProvider
	observer      *dashboardNativeArrowTestObserver
	terminal      *dashboardNativeArrowTestTerminalRecorder
	writer        *dashboardNativeArrowTestWriter
	activeReaders atomic.Int64
	cancel        context.CancelFunc
}

func newDashboardNativeArrowStreamFixture(t testing.TB, policy dashboardNativeArrowStreamPolicy, events *dashboardNativeArrowTestEvents, connections int) *dashboardNativeArrowStreamFixture {
	t.Helper()
	capacity, err := newDashboardNativeArrowStreamCapacity(policy)
	if err != nil {
		t.Fatal(err)
	}
	return &dashboardNativeArrowStreamFixture{
		capacity: capacity,
		admitter: &dashboardNativeArrowTestAdmitter{events: events},
		serving: &dashboardNativeArrowTestServingProvider{
			events: events, currentGeneration: "generation-a", activeGenerations: map[string]int{},
		},
		database: &dashboardNativeArrowTestDatabaseProvider{events: events, slots: make(chan struct{}, connections)},
		observer: &dashboardNativeArrowTestObserver{events: events},
		terminal: &dashboardNativeArrowTestTerminalRecorder{events: events},
		writer:   &dashboardNativeArrowTestWriter{header: make(stdhttp.Header)},
		cancel:   func() {},
	}
}

func (f *dashboardNativeArrowStreamFixture) dependencies() dashboardNativeArrowStreamDependencies {
	return dashboardNativeArrowStreamDependencies{
		Capacity: f.capacity, Admitter: f.admitter,
		AlreadyAdmitted: func(ctx context.Context) bool {
			_, _, admitted := workload.Current(ctx)
			return admitted
		},
		Serving: f.serving, Database: f.database, Terminal: f.terminal, Observer: f.observer,
	}
}

func (f *dashboardNativeArrowStreamFixture) assertReleased(t testing.TB) {
	t.Helper()
	if f.capacity.active != 0 || f.admitter.active.Load() != 0 || f.serving.active.Load() != 0 || f.database.active.Load() != 0 || f.activeReaders.Load() != 0 {
		t.Fatalf("resource leak: slot=%d admission=%d serving=%d database=%d readers=%d", f.capacity.active, f.admitter.active.Load(), f.serving.active.Load(), f.database.active.Load(), f.activeReaders.Load())
	}
	observation := f.observer.last()
	if observation.CleanupBoundExceeded || observation.CleanupDuration > f.capacity.policy.CleanupBound {
		t.Fatalf("cleanup observation = %#v", observation)
	}
}

type dashboardNativeArrowTestEvents struct {
	mu     sync.Mutex
	values []string
}

func (e *dashboardNativeArrowTestEvents) add(value string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.values = append(e.values, value)
	e.mu.Unlock()
}

func (e *dashboardNativeArrowTestEvents) snapshot() []string {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.values...)
}

func dashboardNativeArrowEventsOrdered(events []string, values ...string) bool {
	position := -1
	for _, value := range values {
		found := -1
		for index := position + 1; index < len(events); index++ {
			if events[index] == value {
				found = index
				break
			}
		}
		if found < 0 {
			return false
		}
		position = found
	}
	return true
}

type dashboardNativeArrowTestAdmitter struct {
	events *dashboardNativeArrowTestEvents
	calls  atomic.Int64
	active atomic.Int64
	reject error
}

func (a *dashboardNativeArrowTestAdmitter) AcquireDashboardNativeArrowStream(ctx context.Context) (dashboardNativeArrowStreamAdmission, error) {
	a.calls.Add(1)
	if a.reject != nil {
		return nil, a.reject
	}
	a.active.Add(1)
	a.events.add("admission-acquire")
	return &dashboardNativeArrowTestAdmissionLease{ctx: ctx, owner: a}, nil
}

type dashboardNativeArrowTestAdmissionLease struct {
	ctx   context.Context
	owner *dashboardNativeArrowTestAdmitter
	once  sync.Once
}

func (l *dashboardNativeArrowTestAdmissionLease) Context() context.Context { return l.ctx }
func (*dashboardNativeArrowTestAdmissionLease) QueueWait() time.Duration   { return 0 }
func (l *dashboardNativeArrowTestAdmissionLease) Release() {
	l.once.Do(func() {
		l.owner.active.Add(-1)
		l.owner.events.add("admission")
	})
}

type dashboardNativeArrowTestServingProvider struct {
	events            *dashboardNativeArrowTestEvents
	acquires          atomic.Int64
	active            atomic.Int64
	mu                sync.Mutex
	currentGeneration string
	activeGenerations map[string]int
}

func (p *dashboardNativeArrowTestServingProvider) Acquire(context.Context) (projectruntime.Lease, error) {
	p.mu.Lock()
	generation := p.currentGeneration
	p.activeGenerations[generation]++
	p.mu.Unlock()
	p.acquires.Add(1)
	p.active.Add(1)
	p.events.add("serving-acquire")
	return &dashboardNativeArrowTestServingLease{owner: p, generation: generation}, nil
}

func (p *dashboardNativeArrowTestServingProvider) cutover(generation string) {
	p.mu.Lock()
	p.currentGeneration = generation
	p.mu.Unlock()
}

func (p *dashboardNativeArrowTestServingProvider) activeFor(generation string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeGenerations[generation]
}

type dashboardNativeArrowTestRuntime struct{}

func (dashboardNativeArrowTestRuntime) Close() error { return nil }

type dashboardNativeArrowTestServingLease struct {
	owner      *dashboardNativeArrowTestServingProvider
	generation string
	once       sync.Once
}

func (*dashboardNativeArrowTestServingLease) Runtime() projectruntime.Runtime {
	return dashboardNativeArrowTestRuntime{}
}
func (l *dashboardNativeArrowTestServingLease) Identity() projectgraph.ServingIdentity {
	return projectgraph.ServingIdentity{ProjectID: "project-a", Environment: "test", GenerationID: l.generation}
}
func (l *dashboardNativeArrowTestServingLease) Release() {
	l.once.Do(func() {
		l.owner.mu.Lock()
		decrementDashboardNativeArrowStreamActor(l.owner.activeGenerations, l.generation)
		l.owner.mu.Unlock()
		l.owner.active.Add(-1)
		l.owner.events.add("serving")
	})
}

type dashboardNativeArrowTestDatabaseProvider struct {
	events   *dashboardNativeArrowTestEvents
	slots    chan struct{}
	acquires atomic.Int64
	active   atomic.Int64
}

func (p *dashboardNativeArrowTestDatabaseProvider) Acquire(ctx context.Context) (analyticsresource.Lease, error) {
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	p.acquires.Add(1)
	p.active.Add(1)
	p.events.add("database-acquire")
	return &dashboardNativeArrowTestDatabaseLease{ctx: ctx, owner: p}, nil
}

type dashboardNativeArrowTestDatabaseLease struct {
	ctx   context.Context
	owner *dashboardNativeArrowTestDatabaseProvider
	once  sync.Once
}

func (l *dashboardNativeArrowTestDatabaseLease) Context() context.Context { return l.ctx }
func (l *dashboardNativeArrowTestDatabaseLease) Release() {
	l.once.Do(func() {
		<-l.owner.slots
		l.owner.active.Add(-1)
		l.owner.events.add("database")
	})
}

type dashboardNativeArrowTestReader struct {
	events *dashboardNativeArrowTestEvents
	active *atomic.Int64
	delay  time.Duration
	once   sync.Once
}

func (r *dashboardNativeArrowTestReader) Close() error {
	r.once.Do(func() {
		if r.delay > 0 {
			time.Sleep(r.delay)
		}
		r.active.Add(-1)
		r.events.add("reader")
	})
	return nil
}

type dashboardNativeArrowTestObserver struct {
	events *dashboardNativeArrowTestEvents
	mu     sync.Mutex
	value  dashboardNativeArrowStreamObservation
}

type dashboardNativeArrowTestTerminalRecorder struct {
	events *dashboardNativeArrowTestEvents
	mu     sync.Mutex
	value  dashboardNativeArrowStreamTerminalEvent
}

func (r *dashboardNativeArrowTestTerminalRecorder) RecordDashboardNativeArrowStreamTerminal(value dashboardNativeArrowStreamTerminalEvent) {
	r.mu.Lock()
	r.value = value
	r.mu.Unlock()
	r.events.add("terminal")
}

func (r *dashboardNativeArrowTestTerminalRecorder) last() dashboardNativeArrowStreamTerminalEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.value
}

func (o *dashboardNativeArrowTestObserver) ObserveDashboardNativeArrowStream(value dashboardNativeArrowStreamObservation) {
	o.mu.Lock()
	o.value = value
	o.mu.Unlock()
	o.events.add("observe")
}

func (o *dashboardNativeArrowTestObserver) last() dashboardNativeArrowStreamObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.value
}

type dashboardNativeArrowTestWriter struct {
	header   stdhttp.Header
	mu       sync.Mutex
	buffer   bytes.Buffer
	deadline time.Time
	fail     error
}

func (w *dashboardNativeArrowTestWriter) Header() stdhttp.Header { return w.header }
func (*dashboardNativeArrowTestWriter) WriteHeader(int)          {}
func (w *dashboardNativeArrowTestWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadline = deadline
	w.mu.Unlock()
	return nil
}
func (w *dashboardNativeArrowTestWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fail != nil {
		return 0, w.fail
	}
	return w.buffer.Write(payload)
}

type dashboardNativeArrowSocketWriter struct {
	header     stdhttp.Header
	connection net.Conn
}

func (w *dashboardNativeArrowSocketWriter) Header() stdhttp.Header { return w.header }
func (*dashboardNativeArrowSocketWriter) WriteHeader(int)          {}
func (w *dashboardNativeArrowSocketWriter) Write(payload []byte) (int, error) {
	return w.connection.Write(payload)
}
func (w *dashboardNativeArrowSocketWriter) SetWriteDeadline(deadline time.Time) error {
	return w.connection.SetWriteDeadline(deadline)
}

var _ projectruntime.Provider = (*dashboardNativeArrowTestServingProvider)(nil)
