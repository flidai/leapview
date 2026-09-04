package deployment

import (
	"fmt"
	"time"
)

type DeliveryGCStatus string

const (
	DeliveryGCRunning  DeliveryGCStatus = "running"
	DeliveryGCMarked   DeliveryGCStatus = "marked"
	DeliveryGCDeleting DeliveryGCStatus = "deleting"
	DeliveryGCComplete DeliveryGCStatus = "complete"
	DeliveryGCAborted  DeliveryGCStatus = "aborted"
)

// DeliveryGCCycle records a bounded mark-and-sweep operation. The mark digest
// is evidence; catalog file membership is read from each sealed catalog.
type DeliveryGCCycle struct {
	ID             string           `json:"id"`
	ActorID        string           `json:"actorId,omitempty"`
	PhysicalPoolID string           `json:"physicalPoolId"`
	Epoch          int64            `json:"epoch"`
	RootRevision   int64            `json:"rootRevision"`
	MarkDigest     string           `json:"markDigest,omitempty"`
	Status         DeliveryGCStatus `json:"status"`
	CreatedAt      time.Time        `json:"createdAt"`
	CompletedAt    time.Time        `json:"completedAt"`
	AbortReason    string           `json:"abortReason,omitempty"`
}

func (cycle DeliveryGCCycle) Validate() error {
	for name, value := range map[string]string{"GC cycle": cycle.ID, "pool": cycle.PhysicalPoolID} {
		if err := ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("%s id: %w", name, err)
		}
	}
	if cycle.Epoch < 1 || cycle.RootRevision < 0 {
		return fmt.Errorf("%w: GC epoch/revision is invalid", ErrDeliveryInvalid)
	}
	if err := validateDeliveryTime("GC cycle created at", cycle.CreatedAt, true); err != nil {
		return err
	}
	if cycle.Status == DeliveryGCMarked || cycle.Status == DeliveryGCDeleting || cycle.Status == DeliveryGCComplete {
		if err := ValidateDeliveryDigest(cycle.MarkDigest); err != nil {
			return fmt.Errorf("GC mark digest: %w", err)
		}
	}
	if cycle.Status == DeliveryGCRunning && (cycle.MarkDigest != "" || !cycle.CompletedAt.IsZero() || cycle.AbortReason != "") {
		return fmt.Errorf("%w: running GC cycle contains terminal evidence", ErrDeliveryInvalid)
	}
	if (cycle.Status == DeliveryGCMarked || cycle.Status == DeliveryGCDeleting) && !cycle.CompletedAt.IsZero() {
		return fmt.Errorf("%w: in-progress GC cycle contains completion evidence", ErrDeliveryInvalid)
	}
	if cycle.Status == DeliveryGCComplete || cycle.Status == DeliveryGCAborted {
		if err := validateDeliveryTime("GC cycle completed at", cycle.CompletedAt, true); err != nil {
			return err
		}
	}
	if cycle.Status != DeliveryGCAborted && cycle.AbortReason != "" {
		return fmt.Errorf("%w: non-aborted GC cycle contains abort evidence", ErrDeliveryInvalid)
	}
	switch cycle.Status {
	case DeliveryGCRunning, DeliveryGCMarked, DeliveryGCDeleting, DeliveryGCComplete, DeliveryGCAborted:
	default:
		return fmt.Errorf("%w: unsupported GC status %q", ErrDeliveryInvalid, cycle.Status)
	}
	return nil
}

func NewDeliveryGCCycle(cycle DeliveryGCCycle) (DeliveryGCCycle, error) {
	if err := validateDeliveryTime("GC cycle created at", cycle.CreatedAt, true); err != nil {
		return DeliveryGCCycle{}, err
	}
	cycle.Status = DeliveryGCRunning
	cycle.CreatedAt = cycle.CreatedAt.UTC()
	cycle.CompletedAt = time.Time{}
	cycle.MarkDigest, cycle.AbortReason = "", ""
	if err := cycle.Validate(); err != nil {
		return DeliveryGCCycle{}, err
	}
	return cycle, nil
}

func (cycle DeliveryGCCycle) Mark(markDigest string) (DeliveryGCCycle, error) {
	if err := ValidateDeliveryDigest(markDigest); err != nil {
		return DeliveryGCCycle{}, err
	}
	if err := cycle.Validate(); err != nil {
		return DeliveryGCCycle{}, err
	}
	if cycle.Status == DeliveryGCMarked || cycle.Status == DeliveryGCDeleting || cycle.Status == DeliveryGCComplete {
		if cycle.MarkDigest == markDigest {
			return cycle, nil
		}
		return DeliveryGCCycle{}, fmt.Errorf("%w: GC mark changed", ErrDeliveryConflict)
	}
	if cycle.Status != DeliveryGCRunning {
		return DeliveryGCCycle{}, fmt.Errorf("%w: GC cycle is %s", ErrDeliveryTransition, cycle.Status)
	}
	cycle.Status, cycle.MarkDigest = DeliveryGCMarked, markDigest
	return cycle, nil
}

func (cycle DeliveryGCCycle) BeginDelete() (DeliveryGCCycle, error) {
	if err := cycle.Validate(); err != nil {
		return DeliveryGCCycle{}, err
	}
	if cycle.Status == DeliveryGCDeleting {
		return cycle, nil
	}
	if cycle.Status != DeliveryGCMarked {
		return DeliveryGCCycle{}, fmt.Errorf("%w: GC cycle is %s", ErrDeliveryTransition, cycle.Status)
	}
	cycle.Status = DeliveryGCDeleting
	return cycle, nil
}

func (cycle DeliveryGCCycle) Complete(now time.Time) (DeliveryGCCycle, error) {
	if err := cycle.Validate(); err != nil {
		return DeliveryGCCycle{}, err
	}
	if cycle.Status == DeliveryGCComplete {
		return cycle, nil
	}
	if cycle.Status != DeliveryGCDeleting || now.IsZero() {
		return DeliveryGCCycle{}, fmt.Errorf("%w: GC cycle is not deleting", ErrDeliveryTransition)
	}
	cycle.Status, cycle.CompletedAt = DeliveryGCComplete, now.UTC()
	return cycle, nil
}

func (cycle DeliveryGCCycle) Abort(reason string, now time.Time) (DeliveryGCCycle, error) {
	if reason == "" || reason != trim(reason) || now.IsZero() {
		return DeliveryGCCycle{}, fmt.Errorf("%w: GC abort evidence is required", ErrDeliveryInvalid)
	}
	if err := cycle.Validate(); err != nil {
		return DeliveryGCCycle{}, err
	}
	if cycle.Status == DeliveryGCAborted && cycle.AbortReason == reason {
		return cycle, nil
	}
	if cycle.Status == DeliveryGCComplete {
		return DeliveryGCCycle{}, fmt.Errorf("%w: completed GC cycle cannot abort", ErrDeliveryConflict)
	}
	cycle.Status, cycle.AbortReason, cycle.CompletedAt = DeliveryGCAborted, reason, now.UTC()
	return cycle, nil
}

type DeliveryGCDeleteIntentStatus string

const (
	DeliveryGCDeletePending   DeliveryGCDeleteIntentStatus = "pending"
	DeliveryGCDeleteDeleted   DeliveryGCDeleteIntentStatus = "deleted"
	DeliveryGCDeleteAmbiguous DeliveryGCDeleteIntentStatus = "ambiguous"
)

// DeliveryGCDeleteIntent is an exact bounded object-store operation. It is not
// a physical membership or reference-count table.
type DeliveryGCDeleteIntent struct {
	ID             string                       `json:"id"`
	CycleID        string                       `json:"cycleId"`
	PhysicalPoolID string                       `json:"physicalPoolId"`
	ObjectKey      string                       `json:"objectKey"`
	ObjectDigest   string                       `json:"objectDigest"`
	ObjectVersion  string                       `json:"objectVersion,omitempty"`
	Status         DeliveryGCDeleteIntentStatus `json:"status"`
	CreatedAt      time.Time                    `json:"createdAt"`
	CompletedAt    time.Time                    `json:"completedAt"`
}

func (intent DeliveryGCDeleteIntent) Validate() error {
	for name, value := range map[string]string{"delete intent": intent.ID, "cycle": intent.CycleID, "pool": intent.PhysicalPoolID} {
		if err := ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("%s id: %w", name, err)
		}
	}
	if intent.ObjectKey == "" || intent.ObjectKey != trim(intent.ObjectKey) {
		return fmt.Errorf("%w: delete object key is not canonical", ErrDeliveryInvalid)
	}
	if err := ValidateDeliveryDigest(intent.ObjectDigest); err != nil {
		return fmt.Errorf("object digest: %w", err)
	}
	if intent.ObjectVersion != "" && (intent.ObjectVersion != trim(intent.ObjectVersion) || len(intent.ObjectVersion) > 512) {
		return fmt.Errorf("%w: object version is not canonical", ErrDeliveryInvalid)
	}
	if err := validateDeliveryTime("delete intent created at", intent.CreatedAt, true); err != nil {
		return err
	}
	if intent.Status != DeliveryGCDeletePending {
		if err := validateDeliveryTime("delete intent completed at", intent.CompletedAt, true); err != nil {
			return err
		}
	}
	switch intent.Status {
	case DeliveryGCDeletePending, DeliveryGCDeleteDeleted, DeliveryGCDeleteAmbiguous:
	default:
		return fmt.Errorf("%w: unsupported delete intent status %q", ErrDeliveryInvalid, intent.Status)
	}
	return nil
}

func NewDeliveryGCDeleteIntent(intent DeliveryGCDeleteIntent) (DeliveryGCDeleteIntent, error) {
	if err := validateDeliveryTime("delete intent created at", intent.CreatedAt, true); err != nil {
		return DeliveryGCDeleteIntent{}, err
	}
	intent.Status = DeliveryGCDeletePending
	intent.CreatedAt = intent.CreatedAt.UTC()
	intent.CompletedAt = time.Time{}
	if err := intent.Validate(); err != nil {
		return DeliveryGCDeleteIntent{}, err
	}
	return intent, nil
}

func (intent DeliveryGCDeleteIntent) Complete(status DeliveryGCDeleteIntentStatus, now time.Time) (DeliveryGCDeleteIntent, error) {
	if status != DeliveryGCDeleteDeleted && status != DeliveryGCDeleteAmbiguous {
		return DeliveryGCDeleteIntent{}, fmt.Errorf("%w: invalid delete result %q", ErrDeliveryInvalid, status)
	}
	if err := intent.Validate(); err != nil {
		return DeliveryGCDeleteIntent{}, err
	}
	if intent.Status == status {
		return intent, nil
	}
	if intent.Status != DeliveryGCDeletePending || now.IsZero() {
		return DeliveryGCDeleteIntent{}, fmt.Errorf("%w: delete intent is %s", ErrDeliveryTransition, intent.Status)
	}
	intent.Status, intent.CompletedAt = status, now.UTC()
	return intent, nil
}
