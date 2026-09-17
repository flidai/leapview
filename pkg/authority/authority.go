package authority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/flidai/leapview/pkg/permissions"
)

const AuthorityEnvelopeProfile = "leapview.jobs/authority/v1"

const (
	CredentialClassSession  = "session"
	CredentialClassAPIToken = "api_token"
)

var (
	ErrAuthorityInvalid       = errors.New("invalid async job authority envelope")
	ErrAuthorityRequired      = errors.New("async job authority envelope is required")
	ErrAuthorityRevalidator   = errors.New("async job authority revalidator is required")
	ErrAuthorityNoPermissions = errors.New("async job authority has no executable permissions")
)

// AuthorityMode identifies which durable authority governs a queued job.
// Infrastructure credentials are deliberately not a mode: they transport a
// job but never authorize its product operation.
type AuthorityMode string

const (
	CallerAuthorityMode   AuthorityMode = "caller"
	DelegatedWorkloadMode AuthorityMode = "delegated_workload"
)

// CredentialEvidence is a non-secret reference to the credential that
// initiated caller-authority work. Fingerprint is the verifier-bound identity,
// never the bearer secret.
type CredentialEvidence struct {
	Class       string    `json:"class,omitempty"`
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// ExecutionGrantEvidence keeps delegated workload mode structurally
// representable. A module must provide an explicit revalidator before this
// evidence can authorize execution.
type ExecutionGrantEvidence struct {
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// AuthorityTarget binds the operation to its instance/project/environment and
// optional exact product resource. It is independent from the queue's
// partition key, which is only a fairness and ordering primitive.
type AuthorityTarget struct {
	InstanceID   string `json:"instanceId,omitempty"`
	ProjectID    string `json:"projectId,omitempty"`
	Environment  string `json:"environment,omitempty"`
	ResourceKind string `json:"resourceKind,omitempty"`
	ResourceID   string `json:"resourceId,omitempty"`
}

// AuthorityEnvelope is the immutable security context recorded with a
// product job. Permissions preserve exact action/resource pairings from the
// permission contract; callers must not replace them with independent lists.
type AuthorityEnvelope struct {
	Profile              string                  `json:"profile"`
	Mode                 AuthorityMode           `json:"mode"`
	ActorPrincipalID     string                  `json:"actorPrincipalId"`
	ExecutionPrincipalID string                  `json:"executionPrincipalId"`
	Credential           *CredentialEvidence     `json:"credential,omitempty"`
	ExecutionGrant       *ExecutionGrantEvidence `json:"executionGrant,omitempty"`
	Target               AuthorityTarget         `json:"target"`
	Permissions          []permissions.Pair      `json:"permissions"`
}

// IsZero reports the migration-sentinel form that carries no usable
// authority. It is intentionally distinct from an explicit deny-all token or
// a malformed nonzero envelope.
func (a AuthorityEnvelope) IsZero() bool {
	return a.Profile == "" && a.Mode == "" && a.ActorPrincipalID == "" && a.ExecutionPrincipalID == "" &&
		a.Credential == nil && a.ExecutionGrant == nil && a.Target == (AuthorityTarget{}) && a.Permissions == nil
}

// Validate checks the durable shape. Current authority, credential
// revocation, and delegated-grant validity remain live checks performed by a
// module AuthorityRevalidator at dequeue and execution boundaries.
func (a AuthorityEnvelope) Validate() error {
	if a.Profile != AuthorityEnvelopeProfile {
		return fmt.Errorf("%w: unsupported profile %q", ErrAuthorityInvalid, a.Profile)
	}
	if !canonicalIdentity(a.ActorPrincipalID) || !canonicalIdentity(a.ExecutionPrincipalID) {
		return fmt.Errorf("%w: actor and execution principal are required", ErrAuthorityInvalid)
	}
	if err := a.Target.Validate(); err != nil {
		return fmt.Errorf("%w: target: %v", ErrAuthorityInvalid, err)
	}
	if err := permissions.ValidatePairSetShape(a.Permissions); err != nil {
		return fmt.Errorf("%w: permissions: %v", ErrAuthorityInvalid, err)
	}
	switch a.Mode {
	case CallerAuthorityMode:
		if a.ActorPrincipalID != a.ExecutionPrincipalID {
			return fmt.Errorf("%w: caller execution principal must equal actor", ErrAuthorityInvalid)
		}
		if a.Credential == nil {
			return fmt.Errorf("%w: caller credential evidence is required", ErrAuthorityInvalid)
		}
		if a.ExecutionGrant != nil {
			return fmt.Errorf("%w: caller mode cannot carry an execution grant", ErrAuthorityInvalid)
		}
		if err := a.Credential.Validate(); err != nil {
			return fmt.Errorf("%w: credential: %v", ErrAuthorityInvalid, err)
		}
	case DelegatedWorkloadMode:
		if a.ExecutionGrant == nil {
			return fmt.Errorf("%w: delegated execution grant evidence is required", ErrAuthorityInvalid)
		}
		if err := a.ExecutionGrant.Validate(); err != nil {
			return fmt.Errorf("%w: execution grant: %v", ErrAuthorityInvalid, err)
		}
		if a.Credential != nil {
			if err := a.Credential.Validate(); err != nil {
				return fmt.Errorf("%w: initiating credential: %v", ErrAuthorityInvalid, err)
			}
		}
	default:
		return fmt.Errorf("%w: unsupported mode %q", ErrAuthorityInvalid, a.Mode)
	}
	return nil
}

func (t AuthorityTarget) Validate() error {
	if t.InstanceID == "" && t.ProjectID == "" {
		return errors.New("instance or project target is required")
	}
	for name, value := range map[string]string{
		"instance id": t.InstanceID, "project id": t.ProjectID, "environment": t.Environment,
		"resource kind": t.ResourceKind, "resource id": t.ResourceID,
	} {
		if value != "" && !canonicalIdentity(value) {
			return fmt.Errorf("%s is not canonical", name)
		}
	}
	if t.Environment != "" && t.ProjectID == "" {
		return errors.New("environment requires a project target")
	}
	if (t.ResourceKind == "") != (t.ResourceID == "") {
		return errors.New("resource kind and resource id must be provided together")
	}
	return nil
}

func (e CredentialEvidence) Validate() error {
	if !canonicalIdentity(e.ID) || !canonicalIdentity(e.Fingerprint) || e.ExpiresAt.IsZero() {
		return errors.New("credential id, fingerprint, and expiry are required")
	}
	if e.Class != CredentialClassSession && e.Class != CredentialClassAPIToken {
		return fmt.Errorf("unsupported credential class %q", e.Class)
	}
	return nil
}

func (e ExecutionGrantEvidence) Validate() error {
	if !canonicalIdentity(e.ID) || !canonicalIdentity(e.Fingerprint) || e.ExpiresAt.IsZero() {
		return errors.New("execution grant id, fingerprint, and expiry are required")
	}
	return nil
}

// MarshalAuthority persists the canonical envelope. A zero envelope is
// encoded as an empty object for migration compatibility; it is intentionally
// not valid authority and the module revalidator closes it before dispatch.
func MarshalAuthority(a AuthorityEnvelope) ([]byte, error) {
	if a.Profile == "" && a.Mode == "" && a.ActorPrincipalID == "" && a.ExecutionPrincipalID == "" && a.Credential == nil && a.ExecutionGrant == nil && a.Target == (AuthorityTarget{}) && a.Permissions == nil {
		return []byte(`{}`), nil
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

// UnmarshalAuthority decodes persisted evidence but leaves current validity
// to Validate and the live revalidator. Empty legacy rows become zero
// envelopes so they can be terminalized rather than accidentally authorized.
func UnmarshalAuthority(encoded []byte) (AuthorityEnvelope, error) {
	trimmed := bytes.TrimSpace(encoded)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return AuthorityEnvelope{}, nil
	}
	var authority AuthorityEnvelope
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&authority); err != nil {
		return AuthorityEnvelope{}, fmt.Errorf("decode async job authority: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return AuthorityEnvelope{}, fmt.Errorf("decode async job authority: trailing JSON data")
		}
		return AuthorityEnvelope{}, fmt.Errorf("decode async job authority: trailing data: %w", err)
	}
	return authority, nil
}

func canonicalIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 512 && strings.IndexFunc(value, unicode.IsControl) < 0
}

// AuthorityRevalidator proves live authority after dequeue and before any
// product admission, state transition, or handler dispatch. Implementations
// must re-read current grants and credential lifecycle; queue transport
// credentials are not consulted as product authority.
type AuthorityRevalidator interface {
	Revalidate(context.Context, AuthorityEnvelope) error
}

type AuthorityRevalidatorFunc func(context.Context, AuthorityEnvelope) error

func (f AuthorityRevalidatorFunc) Revalidate(ctx context.Context, authority AuthorityEnvelope) error {
	return f(ctx, authority)
}
