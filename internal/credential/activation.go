package credential

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/flidai/leapview/internal/platform/typednil"
)

// ActivationRecord binds public status to the exact server-resolved resource.
// Persisted readiness is intentionally absent: every process must establish it.
type ActivationRecord struct {
	Resource Resource
	Request  ActivationRequest
	Status   ActivationStatus
}

// ActivationAuthority owns transactions, current authorization and immutable
// candidate/configuration identities. Commit must update the serving pointer,
// consume the receipt and append audit within the SAME authority transaction.
// Every mutation serializes on the instance's existing publication fence.
type ActivationAuthority interface {
	AuthorizeMutation(context.Context, string, Resource) error
	Prepare(context.Context, string, Resource, ActivationRequest) (ActivationRecord, error)
	Read(context.Context, string, Resource, string) (ActivationRecord, error)
	Pending(context.Context) (ActivationRecord, error)
	Switch(context.Context, string, ActivationRecord, string) (ActivationRecord, error)
	Commit(context.Context, string, ActivationRecord) (ActivationRecord, error)
	Complete(context.Context, ActivationRecord) (ActivationRecord, error)
	Abort(context.Context, string, ActivationRecord) (ActivationRecord, error)
}

// ActivationRuntime installs only authoritative committed state. RestoreCurrent
// checks the current pointer before loading it; abort never republishes an old
// generation. Both calls return only after readiness and old handles are closed.
type ActivationRuntime interface {
	InstallCommitted(context.Context, ActivationRecord) error
	RestoreCurrent(context.Context) error
	// CheckCurrent verifies that the completed operation still names the
	// authoritative current runtime. It must not install, drain, or mutate it.
	CheckCurrent(context.Context, ActivationRecord) error
}

type ActivationCoordinator struct {
	authority      ActivationAuthority
	runtime        ActivationRuntime
	admission      *ProviderAdmission
	serial         chan struct{}
	drainTime      time.Duration
	readyOperation atomic.Pointer[string]
}

var _ ActivationService = (*ActivationCoordinator)(nil)

func NewActivationCoordinator(authority ActivationAuthority, runtime ActivationRuntime, admission *ProviderAdmission, drainTime time.Duration) (*ActivationCoordinator, error) {
	if typednil.IsNil(authority) || typednil.IsNil(runtime) || admission == nil || drainTime <= 0 {
		return nil, ErrUnavailable
	}
	return &ActivationCoordinator{authority: authority, runtime: runtime, admission: admission, serial: make(chan struct{}, 1), drainTime: drainTime}, nil
}

func (service *ActivationCoordinator) lock(ctx context.Context) (func(), error) {
	if service == nil || ctx == nil {
		return nil, ErrUnavailable
	}
	select {
	case service.serial <- struct{}{}:
		return func() { <-service.serial }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (service *ActivationCoordinator) StartActivation(ctx context.Context, actor string, resource Resource, request ActivationRequest) (ActivationStatus, error) {
	if !canonical(actor) || resource.Validate() != nil || request.Validate() != nil {
		return ActivationStatus{}, ErrInvalid
	}
	unlock, err := service.lock(ctx)
	if err != nil {
		return ActivationStatus{}, err
	}
	defer unlock()
	if err := service.authority.AuthorizeMutation(ctx, actor, resource); err != nil {
		return ActivationStatus{}, err
	}
	record, err := service.prepare(ctx, func(scoped context.Context) (ActivationRecord, error) {
		return service.authority.Prepare(scoped, actor, resource, request)
	})
	return service.status(record), err
}

func (service *ActivationCoordinator) GetActivation(ctx context.Context, actor string, resource Resource, operationID string) (ActivationStatus, error) {
	if service == nil || ctx == nil || !canonical(actor) || resource.Validate() != nil || !activationUUID(operationID) {
		return ActivationStatus{}, ErrInvalid
	}
	record, err := service.authority.Read(ctx, actor, resource, operationID)
	return service.status(record), err
}

func (service *ActivationCoordinator) RetryActivation(ctx context.Context, actor string, resource Resource, operationID, receiptID string) (ActivationStatus, error) {
	if !canonical(actor) || resource.Validate() != nil || !activationUUID(operationID) || (receiptID != "" && !activationUUID(receiptID)) {
		return ActivationStatus{}, ErrInvalid
	}
	unlock, err := service.lock(ctx)
	if err != nil {
		return ActivationStatus{}, err
	}
	defer unlock()
	if err := service.authority.AuthorizeMutation(ctx, actor, resource); err != nil {
		return ActivationStatus{}, err
	}
	record, err := service.authority.Read(ctx, actor, resource, operationID)
	if err != nil {
		return ActivationStatus{}, err
	}
	if record.Status.State == "aborted" {
		return service.status(record), ErrConflict
	}
	if record.Status.State == "completed" {
		// A historical completion cannot release another operation's fence or
		// disturb an already healthy runtime. Closed recovery still verifies
		// and installs the exact current pointer below.
		pending, pendingErr := service.authority.Pending(ctx)
		if pendingErr == nil && pending.Request.OperationID != operationID {
			return service.status(record), ErrConflict
		}
		if pendingErr != nil && !errors.Is(pendingErr, ErrNotFound) {
			return service.status(record), pendingErr
		}
		if service.admission.Ready() {
			// Startup can restore current runtime without a pending journal row.
			// Acknowledge only this exact current operation, never any historical
			// completion merely because admission is open.
			if err := service.runtime.CheckCurrent(ctx, record); err != nil {
				return service.status(record), err
			}
			service.readyOperation.Store(&operationID)
			return service.status(record), nil
		}
	}
	if record.Status.State != "committed" && record.Status.State != "completed" {
		if receiptID == "" {
			return service.status(record), ErrInvalid
		}
		// This transaction reauthorizes manage+use and records the fresh receipt
		// before admission closes. A timeout cannot discard the durable intent.
		record, err = service.prepare(ctx, func(scoped context.Context) (ActivationRecord, error) {
			return service.authority.Switch(scoped, actor, record, receiptID)
		})
		if err != nil {
			return service.status(record), err
		}
	}
	if err := service.pause(ctx); err != nil {
		return service.status(record), err
	}
	if record.Status.State == "switching" {
		record, err = service.authority.Commit(ctx, actor, record)
		if err != nil {
			// A lost commit acknowledgment is uncertain. Leave work closed; the
			// next attempt reads the journal before selecting any runtime.
			return service.status(record), err
		}
	}
	return service.finish(ctx, record)
}

func (service *ActivationCoordinator) AbortActivation(ctx context.Context, actor string, resource Resource, operationID string) (ActivationStatus, error) {
	if !canonical(actor) || resource.Validate() != nil || !activationUUID(operationID) {
		return ActivationStatus{}, ErrInvalid
	}
	unlock, err := service.lock(ctx)
	if err != nil {
		return ActivationStatus{}, err
	}
	defer unlock()
	if err := service.authority.AuthorizeMutation(ctx, actor, resource); err != nil {
		return ActivationStatus{}, err
	}
	record, err := service.authority.Read(ctx, actor, resource, operationID)
	if err != nil {
		return ActivationStatus{}, err
	}
	if record.Status.State == "committed" || record.Status.State == "completed" || record.Status.State == "aborted" {
		return service.status(record), ErrConflict
	}
	if err := service.pause(ctx); err != nil {
		return service.status(record), err
	}
	if err := service.runtime.RestoreCurrent(ctx); err != nil {
		return service.status(record), err
	}
	// Abort takes the current target fence, checks that publication still has
	// the expected predecessor and reauthorizes the caller before recording it.
	record, err = service.authority.Abort(ctx, actor, record)
	if err != nil {
		return service.status(record), err
	}
	if err := service.admission.Resume(); err != nil {
		return service.status(record), err
	}
	return service.status(record), nil
}

// Reconcile runs while startup admission is closed. Precommit operations require
// an explicit authorized retry or abort; committed operations recover only their
// durable replacement. Absence still requires current runtime readiness.
func (service *ActivationCoordinator) Reconcile(ctx context.Context) error {
	unlock, err := service.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := service.pause(ctx); err != nil {
		return err
	}
	record, err := service.authority.Pending(ctx)
	if errors.Is(err, ErrNotFound) {
		if err := service.runtime.RestoreCurrent(ctx); err != nil {
			return err
		}
		return service.admission.Resume()
	}
	if err != nil {
		return err
	}
	if record.Status.State != "committed" && record.Status.State != "completed" {
		return nil
	}
	_, err = service.finish(ctx, record)
	return err
}

func (service *ActivationCoordinator) pause(ctx context.Context) error {
	service.readyOperation.Store(nil)
	bounded, cancel := context.WithTimeout(ctx, service.drainTime)
	defer cancel()
	return service.admission.Pause(bounded)
}

func (service *ActivationCoordinator) finish(ctx context.Context, record ActivationRecord) (ActivationStatus, error) {
	if record.Status.State != "committed" && record.Status.State != "completed" {
		return service.status(record), ErrConflict
	}
	if err := service.runtime.InstallCommitted(ctx, record); err != nil {
		return service.status(record), err
	}
	if record.Status.State != "completed" {
		completed, err := service.authority.Complete(ctx, record)
		if err != nil {
			return service.status(record), err
		}
		record = completed
	}
	if err := service.admission.Resume(); err != nil {
		return service.status(record), err
	}
	operationID := record.Status.OperationID
	service.readyOperation.Store(&operationID)
	return service.status(record), nil
}

func (service *ActivationCoordinator) status(record ActivationRecord) ActivationStatus {
	status := record.Status
	status.RuntimeReady = false
	if service != nil && status.State == "completed" && service.admission.Ready() {
		ready := service.readyOperation.Load()
		status.RuntimeReady = ready != nil && *ready == status.OperationID
	}
	return status
}
