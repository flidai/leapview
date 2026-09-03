package deploymentpostgres

// Native build heartbeats extend every lease that protects one external
// materialization while holding a single control-plane PostgreSQL transaction.
// No database handle or catalog capability crosses this application boundary.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
)

const nativeBuildHeartbeatMaxLease = 24 * time.Hour

// ErrNativeBuildHeartbeatLost means an admitted build no longer owns all of
// its leases. The coordinator must treat the result as indeterminate because
// the external writer may have continued after the renewal failed.
var ErrNativeBuildHeartbeatLost = errors.New("native build heartbeat lost")

// NativeBuildHeartbeatInput identifies one already-admitted build. Operation
// ownership (the command attempt) is intentionally separate from the delivery
// attempt owner (the authenticated builder principal).
type NativeBuildHeartbeatInput struct {
	OperationLease      deploymentmodule.NativeOperationLease
	TargetLease         deploymentnative.LeaseFence
	AttemptID           string
	AttemptOwnerID      string
	AttemptFencingEpoch int64
	Duration            time.Duration
}

// NativeBuildHeartbeatResult returns only value evidence from the operation,
// target, and delivery-attempt lease authorities after a successful atomic
// renewal.
type NativeBuildHeartbeatResult struct {
	OperationLease  deploymentmodule.NativeOperationLease
	TargetLease     deploymentnative.DeliveryLease
	DeliveryAttempt deploymentnative.DeliveryBuildAttempt
}

// nativeBuildHeartbeatGuard owns the renewal goroutine for one admitted
// build. Its context is independent from the build context: a failed renewal
// cancels the build, while Stop can still join the goroutine before the
// coordinator settles the attempt using the caller's context.
type nativeBuildHeartbeatGuard struct {
	heartbeat NativeBuildHeartbeatRunner
	interval  time.Duration
	ctx       context.Context
	cancel    context.CancelFunc
	buildStop context.CancelFunc
	done      chan struct{}
	stopOnce  sync.Once
	stateMu   sync.RWMutex
	input     NativeBuildHeartbeatInput
	lost      error
}

func newNativeBuildHeartbeatGuard(parent context.Context, heartbeat NativeBuildHeartbeatRunner, interval time.Duration, input NativeBuildHeartbeatInput, buildStop context.CancelFunc) *nativeBuildHeartbeatGuard {
	ctx, cancel := context.WithCancel(contextOrBackground(parent))
	guard := &nativeBuildHeartbeatGuard{heartbeat: heartbeat, interval: interval, ctx: ctx, cancel: cancel, buildStop: buildStop, done: make(chan struct{}), input: input}
	go guard.run()
	return guard
}

func (g *nativeBuildHeartbeatGuard) run() {
	defer close(g.done)
	timer := time.NewTimer(g.interval)
	defer timer.Stop()
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-timer.C:
		}
		g.stateMu.RLock()
		input := g.input
		g.stateMu.RUnlock()
		result, err := g.heartbeat.Renew(g.ctx, input)
		if err != nil {
			// Stop and parent cancellation are expected shutdown paths, not a
			// lost lease. A real renewal error cancels physical execution.
			if g.ctx.Err() == nil {
				g.stateMu.Lock()
				g.lost = fmt.Errorf("%w: %w", ErrNativeBuildHeartbeatLost, err)
				g.stateMu.Unlock()
				if g.buildStop != nil {
					g.buildStop()
				}
				g.cancel()
			}
			return
		}
		g.stateMu.Lock()
		g.input.OperationLease = result.OperationLease
		g.input.TargetLease = deploymentnative.LeaseFence{LeaseID: result.TargetLease.LeaseID, TargetID: result.TargetLease.TargetID, OwnerID: result.TargetLease.OwnerID, FencingEpoch: result.TargetLease.FencingEpoch}
		g.stateMu.Unlock()
		timer.Reset(g.interval)
	}
}

// Stop cancels renewal and waits until any in-flight renewal has returned.
// The returned input contains the latest operation lease and fencing values.
func (g *nativeBuildHeartbeatGuard) Stop() (NativeBuildHeartbeatInput, error) {
	if g == nil {
		return NativeBuildHeartbeatInput{}, nil
	}
	g.stopOnce.Do(g.cancel)
	<-g.done
	g.stateMu.RLock()
	defer g.stateMu.RUnlock()
	return g.input, g.lost
}

// NativeBuildHeartbeat renews all canonical operation, target, and delivery
// attempt leases in one caller-owned transaction.
// The transaction methods never begin, commit, or roll back; the convenience
// Renew method owns that lifecycle for callers that do not need composition.
type NativeBuildHeartbeat struct {
	delivery  *deploymentnative.Repository
	operation deploymentmodule.NativeBuildOperationAuthority
}

func NewNativeBuildHeartbeat(delivery *deploymentnative.Repository, operation deploymentmodule.NativeBuildOperationAuthority) (*NativeBuildHeartbeat, error) {
	if delivery == nil || !delivery.Configured() || !delivery.TransactionCapable() {
		return nil, errors.New("native build heartbeat requires a configured, transaction-capable delivery authority")
	}
	if nativeBuildAuthorityNil(operation) {
		return nil, errors.New("native build heartbeat requires a configured operation authority")
	}
	return &NativeBuildHeartbeat{delivery: delivery, operation: operation}, nil
}

func (h *NativeBuildHeartbeat) Renew(ctx context.Context, input NativeBuildHeartbeatInput) (NativeBuildHeartbeatResult, error) {
	if h == nil || h.delivery == nil || h.operation == nil {
		return NativeBuildHeartbeatResult{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	ctx = contextOrBackground(ctx)
	tx, err := h.delivery.Begin(ctx)
	if err != nil {
		return NativeBuildHeartbeatResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	result, err := h.RenewTx(ctx, tx, input)
	if err != nil {
		return NativeBuildHeartbeatResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return NativeBuildHeartbeatResult{}, err
	}
	committed = true
	return result, nil
}

// RenewTx renews operation, target, and delivery-attempt
// leases on exactly tx. The operation authority computes the shared absolute
// expiry; the target lease and delivery attempt must accept that same value or
// the transaction is rolled back by the caller.
func (h *NativeBuildHeartbeat) RenewTx(ctx context.Context, tx deploymentnative.Tx, input NativeBuildHeartbeatInput) (NativeBuildHeartbeatResult, error) {
	if h == nil || h.delivery == nil || h.operation == nil || tx == nil {
		return NativeBuildHeartbeatResult{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	ctx = contextOrBackground(ctx)
	if err := validateNativeBuildHeartbeatInput(input); err != nil {
		return NativeBuildHeartbeatResult{}, err
	}
	renewedOperation, err := h.operation.RenewLeaseTx(ctx, tx, input.OperationLease, input.Duration)
	if err != nil {
		return NativeBuildHeartbeatResult{}, err
	}
	if renewedOperation.Scope != input.OperationLease.Scope ||
		renewedOperation.IdempotencyKey != input.OperationLease.IdempotencyKey ||
		renewedOperation.OperationID != input.OperationLease.OperationID ||
		renewedOperation.OwnerID != input.OperationLease.OwnerID ||
		renewedOperation.FencingGeneration != input.OperationLease.FencingGeneration ||
		renewedOperation.AttemptID != input.AttemptID ||
		renewedOperation.AttemptIdentity != input.OperationLease.AttemptIdentity ||
		renewedOperation.LeaseExpiresAt.IsZero() ||
		!renewedOperation.LeaseExpiresAt.After(input.OperationLease.LeaseExpiresAt) {
		return NativeBuildHeartbeatResult{}, fmt.Errorf("%w: renewed operation lease attempt identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	expiresAt := renewedOperation.LeaseExpiresAt
	if err := h.delivery.RenewLeaseTx(ctx, tx, input.TargetLease, expiresAt); err != nil {
		return NativeBuildHeartbeatResult{}, err
	}
	lease, err := h.delivery.LeaseTx(ctx, tx, input.TargetLease.LeaseID)
	if err != nil {
		return NativeBuildHeartbeatResult{}, err
	}
	if lease.LeaseID != input.TargetLease.LeaseID || lease.TargetID != input.TargetLease.TargetID || lease.OwnerID != input.TargetLease.OwnerID || lease.FencingEpoch != input.TargetLease.FencingEpoch || lease.State != "active" || !lease.ExpiresAt.Equal(expiresAt) {
		return NativeBuildHeartbeatResult{}, fmt.Errorf("%w: renewed target lease identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	deliveryAttempt, err := h.delivery.RenewBuildAttemptLeaseTx(ctx, tx, input.AttemptID, input.AttemptOwnerID, input.AttemptFencingEpoch, expiresAt)
	if err != nil {
		return NativeBuildHeartbeatResult{}, err
	}
	if deliveryAttempt.AttemptID != input.AttemptID || deliveryAttempt.OwnerID != input.AttemptOwnerID || deliveryAttempt.FencingEpoch != input.AttemptFencingEpoch || deliveryAttempt.State != deploymentnative.AttemptRunning || !deliveryAttempt.LeaseExpiresAt.Equal(expiresAt) {
		return NativeBuildHeartbeatResult{}, fmt.Errorf("%w: renewed delivery attempt identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	return NativeBuildHeartbeatResult{OperationLease: renewedOperation, TargetLease: lease, DeliveryAttempt: deliveryAttempt}, nil
}

func validateNativeBuildHeartbeatInput(input NativeBuildHeartbeatInput) error {
	if _, err := canonicalUUIDv7(input.AttemptID); err != nil {
		return fmt.Errorf("%w: heartbeat attempt identity: %v", deploymentdomain.ErrDeliveryInvalid, err)
	}
	if input.OperationLease.Scope == "" || input.OperationLease.Scope != strings.TrimSpace(input.OperationLease.Scope) || len(input.OperationLease.Scope) > 255 ||
		input.OperationLease.IdempotencyKey == "" || input.OperationLease.IdempotencyKey != strings.TrimSpace(input.OperationLease.IdempotencyKey) || len(input.OperationLease.IdempotencyKey) > 512 ||
		input.OperationLease.OwnerID == "" || input.OperationLease.OwnerID != strings.TrimSpace(input.OperationLease.OwnerID) || len(input.OperationLease.OwnerID) > 255 ||
		input.OperationLease.FencingGeneration <= 0 || input.OperationLease.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("%w: operation heartbeat lease identity is invalid", deploymentdomain.ErrDeliveryInvalid)
	}
	if _, err := canonicalUUIDv7(input.OperationLease.OperationID); err != nil {
		return fmt.Errorf("%w: heartbeat operation identity: %v", deploymentdomain.ErrDeliveryInvalid, err)
	}
	if input.OperationLease.AttemptID != input.AttemptID || strings.TrimSpace(input.OperationLease.AttemptIdentity) == "" {
		return fmt.Errorf("%w: operation and heartbeat attempt identities differ", deploymentdomain.ErrDeliveryConflict)
	}
	if input.OperationLease.AttemptIdentity != strings.TrimSpace(input.OperationLease.AttemptIdentity) || len(input.OperationLease.AttemptIdentity) > 512 {
		return fmt.Errorf("%w: operation heartbeat attempt identity is invalid", deploymentdomain.ErrDeliveryInvalid)
	}
	if input.AttemptOwnerID == "" || input.AttemptOwnerID != strings.TrimSpace(input.AttemptOwnerID) || len(input.AttemptOwnerID) > 255 {
		return fmt.Errorf("%w: heartbeat attempt owner is invalid", deploymentdomain.ErrDeliveryInvalid)
	}
	if input.AttemptFencingEpoch <= 0 || input.TargetLease.FencingEpoch != input.AttemptFencingEpoch {
		return fmt.Errorf("%w: heartbeat fencing identities differ", deploymentdomain.ErrDeliveryConflict)
	}
	if input.TargetLease.OwnerID != input.AttemptOwnerID || input.TargetLease.TargetID == "" || input.TargetLease.LeaseID == "" {
		return fmt.Errorf("%w: heartbeat target lease identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if input.Duration < time.Microsecond || input.Duration > nativeBuildHeartbeatMaxLease {
		return fmt.Errorf("%w: heartbeat duration must be at least 1us and at most 24h", deploymentdomain.ErrDeliveryInvalid)
	}
	return nil
}

func nativeBuildAuthorityNil(authority any) bool {
	if authority == nil {
		return true
	}
	v := reflect.ValueOf(authority)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
