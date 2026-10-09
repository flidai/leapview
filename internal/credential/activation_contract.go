package credential

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ActivationRequest identifies an immutable, replayable intent. All destination,
// owner and publication identities are resolved by the service, never the caller.
type ActivationRequest struct {
	OperationID             string
	VersionID               string
	ReceiptID               string
	ExpectedBindingRevision int64
}

func (request ActivationRequest) Validate() error {
	if !activationUUID(request.OperationID) || !activationUUID(request.VersionID) ||
		!activationUUID(request.ReceiptID) || request.ExpectedBindingRevision < 0 {
		return ErrInvalid
	}
	return nil
}

// ActivationStatus contains only safe operation metadata. A committed pointer
// alone does not mean the replacement runtime is ready for provider work.
type ActivationStatus struct {
	OperationID, VersionID, State            string
	BindingRevision                          int64
	CreatedAt, UpdatedAt                     time.Time
	CandidateID, GenerationID, PublicationID string
	RuntimeReady                             bool
}

// ActivationService is shared by authenticated transports and bootstrap. Retry
// before commit requires a fresh receipt; retry after commit restores only the
// exact committed version and may omit the receipt. Abort never undoes commit.
type ActivationService interface {
	StartActivation(context.Context, string, Resource, ActivationRequest) (ActivationStatus, error)
	RetryActivation(context.Context, string, Resource, string, string) (ActivationStatus, error)
	GetActivation(context.Context, string, Resource, string) (ActivationStatus, error)
	AbortActivation(context.Context, string, Resource, string) (ActivationStatus, error)
}

func activationUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}
