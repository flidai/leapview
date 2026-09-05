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
	mu        sync.Mutex
	calls     int
	err       error
	renewed   NativeBuildHeartbeatResult
	calledCh  chan struct{}
	releaseCh chan struct{}
}

func (f *nativeBuildHeartbeatRunnerFake) Renew(ctx context.Context, _ NativeBuildHeartbeatInput) (NativeBuildHeartbeatResult, error) {
	f.mu.Lock()
	f.calls++
	if f.calledCh != nil {
		select {
		case f.calledCh <- struct{}{}:
		default:
		}
	}
	err := f.err
	result := f.renewed
	f.mu.Unlock()
	if err != nil {
		return NativeBuildHeartbeatResult{}, err
	}
	select {
	case <-ctx.Done():
		return NativeBuildHeartbeatResult{}, ctx.Err()
	default:
	}
	if f.releaseCh != nil {
		<-f.releaseCh
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
	fake := &nativeBuildHeartbeatRunnerFake{calledCh: make(chan struct{}, 1), releaseCh: make(chan struct{}), renewed: NativeBuildHeartbeatResult{OperationLease: renewed, TargetLease: deploymentnative.DeliveryLease{LeaseID: "target-lease", TargetID: "target", OwnerID: "owner", FencingEpoch: 1}}}
	guard := newNativeBuildHeartbeatGuard(context.Background(), fake, time.Millisecond, input, nil)
	select {
	case <-fake.calledCh:
	case <-time.After(time.Second):
		t.Fatal("heartbeat runner was not called")
	}
	close(fake.releaseCh)
	latest, err := guard.Stop()
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
	fake := &nativeBuildHeartbeatRunnerFake{calledCh: make(chan struct{}, 1)}
	guard := newNativeBuildHeartbeatGuard(context.Background(), fake, time.Millisecond, nativeBuildHeartbeatGuardInput(), nil)
	select {
	case <-fake.calledCh:
	case <-time.After(time.Second):
		t.Fatal("heartbeat runner was not called")
	}
	if _, err := guard.Stop(); err != nil {
		t.Fatalf("intentional stop reported heartbeat loss: %v", err)
	}
}
