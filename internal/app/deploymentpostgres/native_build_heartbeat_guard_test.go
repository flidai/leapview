package deploymentpostgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
)

type nativeBuildHeartbeatRunnerFake struct {
	mu               sync.Mutex
	calls            int
	err              error
	renewed          NativeBuildHeartbeatResult
	calledCh         chan struct{}
	releaseCh        chan struct{}
	cancelObservedCh chan struct{}
	returnContextErr bool
}

func (f *nativeBuildHeartbeatRunnerFake) signal(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (f *nativeBuildHeartbeatRunnerFake) Renew(ctx context.Context, _ NativeBuildHeartbeatInput) (NativeBuildHeartbeatResult, error) {
	f.mu.Lock()
	f.calls++
	err := f.err
	result := f.renewed
	f.mu.Unlock()
	if err != nil {
		f.signal(f.calledCh)
		return NativeBuildHeartbeatResult{}, err
	}
	select {
	case <-ctx.Done():
		return NativeBuildHeartbeatResult{}, ctx.Err()
	default:
	}
	// Signal only after accepting the context. The test can now use
	// releaseCh to keep this renewal in flight while Stop joins it.
	f.signal(f.calledCh)
	if f.releaseCh != nil {
		select {
		case <-f.releaseCh:
		case <-ctx.Done():
			f.signal(f.cancelObservedCh)
			if f.returnContextErr {
				return NativeBuildHeartbeatResult{}, ctx.Err()
			}
			<-f.releaseCh
		}
	}
	return result, nil
}

func nativeBuildHeartbeatGuardInput() NativeBuildHeartbeatInput {
	operationLease := deploymentmodule.NativeOperationLease{OperationID: "operation", AttemptID: "attempt", AttemptIdentity: "identity", LeaseExpiresAt: time.Unix(10, 0).UTC()}
	return NativeBuildHeartbeatInput{OperationLease: operationLease, TargetLease: deploymentnative.LeaseFence{LeaseID: "target-lease", TargetID: "target", OwnerID: "owner", FencingEpoch: 1}, AttemptID: "attempt", AttemptOwnerID: "owner", AttemptFencingEpoch: 1, Duration: time.Minute}
}

func TestNativeBuildHeartbeatGuardPublishesLatestLeaseAndStops(t *testing.T) {
	input := nativeBuildHeartbeatGuardInput()
	renewed := input.OperationLease
	renewed.LeaseExpiresAt = renewed.LeaseExpiresAt.Add(time.Minute)
	releaseCh := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	t.Cleanup(release)
	fake := &nativeBuildHeartbeatRunnerFake{calledCh: make(chan struct{}, 1), releaseCh: releaseCh, cancelObservedCh: make(chan struct{}, 1), renewed: NativeBuildHeartbeatResult{OperationLease: renewed, TargetLease: deploymentnative.DeliveryLease{LeaseID: "target-lease", TargetID: "target", OwnerID: "owner", FencingEpoch: 1}}}
	guard := newNativeBuildHeartbeatGuard(context.Background(), fake, time.Millisecond, input, nil)
	select {
	case <-fake.calledCh:
	case <-time.After(time.Second):
		t.Fatal("heartbeat runner was not called")
	}
	stopDone := make(chan struct{})
	var latest NativeBuildHeartbeatInput
	var err error
	go func() {
		latest, err = guard.Stop()
		close(stopDone)
	}()
	select {
	case <-fake.cancelObservedCh:
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel the in-flight renewal")
	}
	select {
	case <-stopDone:
		t.Fatal("stop returned before the in-flight renewal was released")
	default:
	}
	release()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("stop did not join the in-flight renewal")
	}
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if latest.OperationLease.LeaseExpiresAt != renewed.LeaseExpiresAt || latest.TargetLease.LeaseID != input.TargetLease.LeaseID {
		t.Fatalf("latest heartbeat input = %+v", latest)
	}
}

func TestNativeBuildHeartbeatGuardCancelsBuildOnRenewalLoss(t *testing.T) {
	want := errors.New("lease fence lost")
	fake := &nativeBuildHeartbeatRunnerFake{err: want, calledCh: make(chan struct{}, 1)}
	buildCtx, buildCancel := context.WithCancel(context.Background())
	guard := newNativeBuildHeartbeatGuard(context.Background(), fake, time.Millisecond, nativeBuildHeartbeatGuardInput(), buildCancel)
	select {
	case <-fake.calledCh:
	case <-time.After(time.Second):
		t.Fatal("heartbeat runner was not called")
	}
	select {
	case <-buildCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("build context was not canceled after renewal loss")
	}
	_, err := guard.Stop()
	if !errors.Is(err, ErrNativeBuildHeartbeatLost) || !errors.Is(err, want) {
		t.Fatalf("stop error = %v, want heartbeat and renewal errors", err)
	}
}

func TestNativeBuildHeartbeatGuardStopDoesNotReportCancellationAsLoss(t *testing.T) {
	releaseCh := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseCh) }) })
	fake := &nativeBuildHeartbeatRunnerFake{calledCh: make(chan struct{}, 1), releaseCh: releaseCh, cancelObservedCh: make(chan struct{}, 1), returnContextErr: true}
	guard := newNativeBuildHeartbeatGuard(context.Background(), fake, time.Millisecond, nativeBuildHeartbeatGuardInput(), nil)
	select {
	case <-fake.calledCh:
	case <-time.After(time.Second):
		t.Fatal("heartbeat runner was not called")
	}
	stopDone := make(chan struct{})
	var err error
	go func() {
		_, err = guard.Stop()
		close(stopDone)
	}()
	select {
	case <-fake.cancelObservedCh:
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel the in-flight renewal")
	}
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("stop did not join the canceled renewal")
	}
	if err != nil {
		t.Fatalf("intentional stop reported heartbeat loss: %v", err)
	}
}
