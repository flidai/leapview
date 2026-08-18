package deployment

import (
	"fmt"
	"time"
)

type DeliveryLeaseStatus string

const (
	DeliveryLeaseActive   DeliveryLeaseStatus = "active"
	DeliveryLeaseReleased DeliveryLeaseStatus = "released"
	DeliveryLeaseExpired  DeliveryLeaseStatus = "expired"
)

// DeliveryQueryLease roots one complete catalog artifact. It deliberately
// cannot lease individual tables or output files.
type DeliveryQueryLease struct {
	ID             string              `json:"id"`
	HolderID       string              `json:"holderId"`
	CandidateID    string              `json:"candidateId,omitempty"`
	GenerationID   string              `json:"generationId,omitempty"`
	CatalogDigest  string              `json:"catalogDigest"`
	PhysicalPoolID string              `json:"physicalPoolId"`
	ExpiresAt      time.Time           `json:"expiresAt"`
	CreatedAt      time.Time           `json:"createdAt"`
	ReleasedAt     time.Time           `json:"releasedAt"`
	Status         DeliveryLeaseStatus `json:"status"`
}

func (lease DeliveryQueryLease) Validate() error {
	for name, value := range map[string]string{"lease": lease.ID, "holder": lease.HolderID, "pool": lease.PhysicalPoolID} {
		if err := ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("%s id: %w", name, err)
		}
	}
	if (lease.CandidateID == "") == (lease.GenerationID == "") {
		return fmt.Errorf("%w: query lease must reference exactly one candidate or generation", ErrDeliveryInvalid)
	}
	if lease.CandidateID != "" {
		if err := ValidateDeliveryID(lease.CandidateID); err != nil {
			return fmt.Errorf("candidate id: %w", err)
		}
	}
	if lease.GenerationID != "" {
		if err := ValidateDeliveryID(lease.GenerationID); err != nil {
			return fmt.Errorf("generation id: %w", err)
		}
	}
	if err := ValidateDeliveryDigest(lease.CatalogDigest); err != nil {
		return fmt.Errorf("catalog digest: %w", err)
	}
	if err := validateDeliveryTime("lease created at", lease.CreatedAt, true); err != nil {
		return err
	}
	if err := validateDeliveryTime("lease expiry", lease.ExpiresAt, true); err != nil {
		return err
	}
	if !lease.ExpiresAt.After(lease.CreatedAt) {
		return fmt.Errorf("%w: lease expiry must be after creation", ErrDeliveryInvalid)
	}
	if lease.Status == DeliveryLeaseReleased || lease.Status == DeliveryLeaseExpired {
		if err := validateDeliveryTime("lease released at", lease.ReleasedAt, true); err != nil {
			return err
		}
	} else if !lease.ReleasedAt.IsZero() {
		return fmt.Errorf("%w: active lease contains release evidence", ErrDeliveryInvalid)
	}
	switch lease.Status {
	case DeliveryLeaseActive, DeliveryLeaseReleased, DeliveryLeaseExpired:
	default:
		return fmt.Errorf("%w: unsupported lease status %q", ErrDeliveryInvalid, lease.Status)
	}
	return nil
}

func NewDeliveryQueryLease(lease DeliveryQueryLease) (DeliveryQueryLease, error) {
	if err := validateDeliveryTime("lease created at", lease.CreatedAt, true); err != nil {
		return DeliveryQueryLease{}, err
	}
	if err := validateDeliveryTime("lease expiry", lease.ExpiresAt, true); err != nil {
		return DeliveryQueryLease{}, err
	}
	lease.Status = DeliveryLeaseActive
	lease.CreatedAt = lease.CreatedAt.UTC()
	lease.ExpiresAt = lease.ExpiresAt.UTC()
	lease.ReleasedAt = time.Time{}
	if err := lease.Validate(); err != nil {
		return DeliveryQueryLease{}, err
	}
	return lease, nil
}

func (lease DeliveryQueryLease) Heartbeat(now, expiresAt time.Time) (DeliveryQueryLease, error) {
	if err := lease.Validate(); err != nil {
		return DeliveryQueryLease{}, err
	}
	if lease.Status != DeliveryLeaseActive {
		return DeliveryQueryLease{}, fmt.Errorf("%w: lease is %s", ErrDeliveryTransition, lease.Status)
	}
	now, expiresAt = now.UTC(), expiresAt.UTC()
	if now.IsZero() || !expiresAt.After(now) || now.After(lease.ExpiresAt) {
		return DeliveryQueryLease{}, fmt.Errorf("%w: lease heartbeat is outside active window", ErrDeliveryInvalid)
	}
	lease.ExpiresAt = expiresAt
	return lease, nil
}

func (lease DeliveryQueryLease) Release(now time.Time) (DeliveryQueryLease, error) {
	if err := lease.Validate(); err != nil {
		return DeliveryQueryLease{}, err
	}
	if lease.Status == DeliveryLeaseReleased {
		return lease, nil
	}
	if lease.Status != DeliveryLeaseActive {
		return DeliveryQueryLease{}, fmt.Errorf("%w: lease is %s", ErrDeliveryTransition, lease.Status)
	}
	if now.IsZero() {
		return DeliveryQueryLease{}, fmt.Errorf("%w: lease release time is required", ErrDeliveryInvalid)
	}
	lease.Status, lease.ReleasedAt = DeliveryLeaseReleased, now.UTC()
	return lease, nil
}

func (lease DeliveryQueryLease) Expire(now time.Time) (DeliveryQueryLease, error) {
	if err := lease.Validate(); err != nil {
		return DeliveryQueryLease{}, err
	}
	if lease.Status == DeliveryLeaseExpired {
		return lease, nil
	}
	if lease.Status != DeliveryLeaseActive || now.UTC().Before(lease.ExpiresAt) {
		return DeliveryQueryLease{}, fmt.Errorf("%w: lease is not expired", ErrDeliveryTransition)
	}
	lease.Status, lease.ReleasedAt = DeliveryLeaseExpired, now.UTC()
	return lease, nil
}

// DeliveryWriterLease fences a private writer in a physical pool. It is
// durable control state, not a process-local mutex.
type DeliveryWriterLease struct {
	ID             string              `json:"id"`
	AttemptID      string              `json:"attemptId"`
	PhysicalPoolID string              `json:"physicalPoolId"`
	OwnerID        string              `json:"ownerId"`
	Epoch          int64               `json:"epoch"`
	ExpiresAt      time.Time           `json:"expiresAt"`
	CreatedAt      time.Time           `json:"createdAt"`
	ReleasedAt     time.Time           `json:"releasedAt"`
	Status         DeliveryLeaseStatus `json:"status"`
}

func (lease DeliveryWriterLease) Validate() error {
	for name, value := range map[string]string{"writer lease": lease.ID, "attempt": lease.AttemptID, "pool": lease.PhysicalPoolID, "owner": lease.OwnerID} {
		if err := ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("%s id: %w", name, err)
		}
	}
	if lease.Epoch < 1 {
		return fmt.Errorf("%w: writer lease epoch must be positive", ErrDeliveryInvalid)
	}
	if err := validateDeliveryTime("writer lease created at", lease.CreatedAt, true); err != nil {
		return err
	}
	if err := validateDeliveryTime("writer lease expiry", lease.ExpiresAt, true); err != nil {
		return err
	}
	if !lease.ExpiresAt.After(lease.CreatedAt) {
		return fmt.Errorf("%w: writer lease expiry must be after creation", ErrDeliveryInvalid)
	}
	switch lease.Status {
	case DeliveryLeaseActive:
		if !lease.ReleasedAt.IsZero() {
			return fmt.Errorf("%w: active writer lease contains release evidence", ErrDeliveryInvalid)
		}
	case DeliveryLeaseReleased, DeliveryLeaseExpired:
		if err := validateDeliveryTime("writer lease released at", lease.ReleasedAt, true); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unsupported writer lease status %q", ErrDeliveryInvalid, lease.Status)
	}
	return nil
}

func NewDeliveryWriterLease(lease DeliveryWriterLease) (DeliveryWriterLease, error) {
	if err := validateDeliveryTime("writer lease created at", lease.CreatedAt, true); err != nil {
		return DeliveryWriterLease{}, err
	}
	if err := validateDeliveryTime("writer lease expiry", lease.ExpiresAt, true); err != nil {
		return DeliveryWriterLease{}, err
	}
	lease.Status = DeliveryLeaseActive
	lease.CreatedAt = lease.CreatedAt.UTC()
	lease.ExpiresAt = lease.ExpiresAt.UTC()
	lease.ReleasedAt = time.Time{}
	if err := lease.Validate(); err != nil {
		return DeliveryWriterLease{}, err
	}
	return lease, nil
}

func (lease DeliveryWriterLease) Release(now time.Time) (DeliveryWriterLease, error) {
	if err := lease.Validate(); err != nil {
		return DeliveryWriterLease{}, err
	}
	if lease.Status == DeliveryLeaseReleased {
		return lease, nil
	}
	if lease.Status != DeliveryLeaseActive || now.IsZero() {
		return DeliveryWriterLease{}, fmt.Errorf("%w: writer lease cannot release", ErrDeliveryTransition)
	}
	lease.Status, lease.ReleasedAt = DeliveryLeaseReleased, now.UTC()
	return lease, nil
}

func (lease DeliveryWriterLease) Heartbeat(now, expiresAt time.Time) (DeliveryWriterLease, error) {
	if err := lease.Validate(); err != nil {
		return DeliveryWriterLease{}, err
	}
	if lease.Status != DeliveryLeaseActive {
		return DeliveryWriterLease{}, fmt.Errorf("%w: writer lease is %s", ErrDeliveryTransition, lease.Status)
	}
	now, expiresAt = now.UTC(), expiresAt.UTC()
	if now.IsZero() || !expiresAt.After(now) || now.After(lease.ExpiresAt) {
		return DeliveryWriterLease{}, fmt.Errorf("%w: writer heartbeat is outside active window", ErrDeliveryInvalid)
	}
	lease.ExpiresAt = expiresAt
	return lease, nil
}

func (lease DeliveryWriterLease) Expire(now time.Time) (DeliveryWriterLease, error) {
	if err := lease.Validate(); err != nil {
		return DeliveryWriterLease{}, err
	}
	if lease.Status == DeliveryLeaseExpired {
		return lease, nil
	}
	if lease.Status != DeliveryLeaseActive || now.IsZero() || now.UTC().Before(lease.ExpiresAt) {
		return DeliveryWriterLease{}, fmt.Errorf("%w: writer lease is not expired", ErrDeliveryTransition)
	}
	lease.Status, lease.ReleasedAt = DeliveryLeaseExpired, now.UTC()
	return lease, nil
}

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
