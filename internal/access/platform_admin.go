package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PlatformAdministrator is an enabled principal with an active durable
// platform-role binding. Binding identity is retained for audit and stable
// response replay; callers must not infer platform authority from project
// snapshots.
type PlatformAdministrator struct {
	BindingID string
	Principal Principal
	Role      PlatformRole
	CreatedAt string
	// RevokedAt is populated by the authority-history reader. An empty value
	// means that the durable binding is still current; it is intentionally
	// omitted from active-only API responses.
	RevokedAt string
}

// PlatformAdministratorState is the current durable platform delegation
// state. Revision is an opaque compare-and-swap token over active bindings.
type PlatformAdministratorState struct {
	Administrators []PlatformAdministrator
	Revision       string
}

type PlatformAdminGrantInput struct {
	PrincipalID      string
	ExpectedRevision string
	IdempotencyKey   string
	// RequestDigestBinding optionally binds an external recovery/request
	// identity into the idempotency digest. Empty preserves the historical
	// digest for ordinary grants.
	RequestDigestBinding string
}

type PlatformAdminGrantResult struct {
	Administrator PlatformAdministrator
	State         PlatformAdministratorState
	Replayed      bool
}

type PlatformAdminRevokeInput struct {
	PrincipalID      string
	ExpectedRevision string
	IdempotencyKey   string
}

// PlatformRoleApprovalStatus is the durable state of a requested platform
// role change. The request row is deliberately retained after a terminal
// transition so retries and audit investigations remain restart-safe.
type PlatformRoleApprovalStatus string

const (
	PlatformRoleApprovalPending  PlatformRoleApprovalStatus = "pending"
	PlatformRoleApprovalApproved PlatformRoleApprovalStatus = "approved"
	PlatformRoleApprovalCanceled PlatformRoleApprovalStatus = "canceled"
	PlatformRoleApprovalExpired  PlatformRoleApprovalStatus = "expired"
	PlatformRoleApprovalExecuted PlatformRoleApprovalStatus = "executed"
)

// PlatformRoleApproval is immutable request identity plus its durable
// lifecycle projection. Result fields are populated only after execution.
type PlatformRoleApproval struct {
	ID               string
	Action           string
	PrincipalID      string
	RequesterID      string
	ApproverID       string
	CanceledBy       string
	ExpiredBy        string
	Status           PlatformRoleApprovalStatus
	ExpectedRevision string
	Revision         int64
	IdempotencyKey   string
	ExpiresAt        string
	CreatedAt        string
	ApprovedAt       string
	CanceledAt       string
	ExpiredAt        string
	ExecutedAt       string
	BindingID        string
	ResultRevision   string
}

type PlatformRoleApprovalRequestInput struct {
	Action           string
	PrincipalID      string
	RequesterID      string
	ExpectedRevision string
	IdempotencyKey   string
}

type PlatformRoleApprovalDecisionInput struct {
	ApprovalID       string
	ActorID          string
	ExpectedRevision int64
	IdempotencyKey   string
}

type PlatformRoleApprovalExecuteInput struct {
	ApprovalID     string
	ActorID        string
	IdempotencyKey string
}

type PlatformRoleApprovalWriter interface {
	RequestPlatformRoleApproval(context.Context, PlatformRoleApprovalRequestInput) (PlatformRoleApproval, error)
	ApprovePlatformRoleApproval(context.Context, PlatformRoleApprovalDecisionInput) (PlatformRoleApproval, error)
	CancelPlatformRoleApproval(context.Context, PlatformRoleApprovalDecisionInput) (PlatformRoleApproval, error)
	ExpirePlatformRoleApproval(context.Context, PlatformRoleApprovalDecisionInput) (PlatformRoleApproval, error)
	ExecutePlatformRoleApproval(context.Context, PlatformRoleApprovalExecuteInput) (PlatformRoleApproval, error)
}

type PlatformRoleApprovalReader interface {
	GetPlatformRoleApproval(context.Context, string) (PlatformRoleApproval, error)
	ListPlatformRoleApprovals(context.Context, string) ([]PlatformRoleApproval, error)
}

var (
	ErrPlatformAdminNotFound                = errors.New("platform administrator was not found")
	ErrPlatformAdminConflict                = errors.New("platform administrator conflicts with current state")
	ErrPlatformAdminStaleRevision           = errors.New("platform administrator revision is stale")
	ErrPlatformAdminIdempotency             = errors.New("platform administrator idempotency key conflicts with current state")
	ErrPlatformAdminInvalid                 = errors.New("platform administrator request is invalid")
	ErrPlatformAdminLastAdmin               = errors.New("cannot revoke the last usable platform administrator")
	ErrPlatformAdminApprovalRequired        = errors.New("platform administrator changes require a second administrator approval")
	ErrPlatformRoleApprovalNotFound         = errors.New("platform role approval was not found")
	ErrPlatformRoleApprovalConflict         = errors.New("platform role approval conflicts with current state")
	ErrPlatformRoleApprovalExpired          = errors.New("platform role approval has expired")
	ErrPlatformRoleApprovalInvalid          = errors.New("platform role approval request is invalid")
	ErrPlatformRoleApprovalSeparationOfDuty = errors.New("platform role approval separation of duty violated")
	ErrPlatformRoleApprovalNotDue           = errors.New("platform role approval is not due to expire")
)

const PlatformRoleApprovalLifetime = 24 * time.Hour
const RecentInteractiveAuthenticationWindow = 15 * time.Minute

type PlatformAdminLister interface {
	ListPlatformAdministrators(context.Context) (PlatformAdministratorState, error)
}

// PlatformAdminAuthorityLister exposes current and revoked platform-role
// bindings for operator Settings. It is separate from PlatformAdminLister so
// the authorization path can continue to consume only currently usable
// administrators.
type PlatformAdminAuthorityLister interface {
	ListPlatformAdminAuthorities(context.Context) (PlatformAdministratorState, error)
}

// PlatformAdminWriter owns durable delegation changes. Implementations must
// validate the target principal's enabled state and perform the mutation and
// operation replay record in one transaction.
type PlatformAdminWriter interface {
	GrantPlatformAdmin(context.Context, PlatformAdminGrantInput) (PlatformAdminGrantResult, error)
	RevokePlatformAdmin(context.Context, PlatformAdminRevokeInput) (PlatformAdministratorState, error)
}

// PlatformAdministratorRevision returns a deterministic opaque CAS token for
// a state returned by the platform administrator authority.
func PlatformAdministratorRevision(items []PlatformAdministrator) (string, error) {
	canonical := append([]PlatformAdministrator(nil), items...)
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].BindingID != canonical[j].BindingID {
			return canonical[i].BindingID < canonical[j].BindingID
		}
		return canonical[i].Principal.ID < canonical[j].Principal.ID
	})
	type revisionItem struct {
		BindingID   string `json:"bindingId"`
		PrincipalID string `json:"principalId"`
		Role        string `json:"role"`
		CreatedAt   string `json:"createdAt"`
	}
	values := make([]revisionItem, 0, len(canonical))
	for _, item := range canonical {
		if strings.TrimSpace(item.BindingID) == "" || strings.TrimSpace(item.Principal.ID) == "" {
			return "", fmt.Errorf("%w: platform administrator identity is incomplete", ErrPlatformAdminInvalid)
		}
		if _, err := ParsePlatformRole(string(item.Role)); err != nil {
			return "", err
		}
		values = append(values, revisionItem{BindingID: item.BindingID, PrincipalID: item.Principal.ID, Role: string(item.Role), CreatedAt: item.CreatedAt})
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode platform administrator revision: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
