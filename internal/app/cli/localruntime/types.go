// Package localruntime owns the released host-side lifecycle for one
// checkout-scoped local analytics runtime.
package localruntime

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/flidai/leapview/internal/platform/buildinfo"
)

const (
	stateSchemaVersion           = 3
	manifestSchemaVersion        = 1
	persistentStateSchemaVersion = 1
	postgresMajor                = 18
	postgresImage                = "docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8"
)

type Endpoint interface {
	Host() string
	ServerID() string
	Fingerprint() string
	Verify(context.Context) error
	DockerArguments(...string) []string
	Environment([]string) []string
}

type Runner interface {
	Run(context.Context, []string, ...string) ([]byte, error)
}

type Options struct {
	CheckoutRoot            string
	RuntimePackage          string
	StateRoot               string
	DockerBin               string
	Endpoint                Endpoint
	ResolveProjectAuthority func() (ProjectAuthority, error)
	BuildIdentity           buildinfo.Identity
	Runner                  Runner
	HTTPClient              *http.Client
	EstablishSessions       func(context.Context, SessionRequest) (SessionResult, error)
	ResetSessions           func(context.Context, SessionRequest) error
	Stdout                  io.Writer
	Sleep                   func(context.Context, time.Duration) error
	Now                     func() time.Time
	// DevelopmentCredentials is the exact profile-selected set of connector
	// bundle environment variables. No ambient process variables are copied.
	DevelopmentCredentials map[string]string
	// DevelopmentProfile is the exact non-secret profile/graph identity that
	// every attachment to this runtime must share. Credential values remain in
	// the separate private environment file.
	DevelopmentProfile          DevelopmentProfileIdentity
	AttachmentHeartbeatInterval time.Duration
	AttachmentStaleAfter        time.Duration
}

type DevelopmentProfileIdentity struct {
	Name          string
	GraphDigest   string
	ProfileDigest string
}

type State struct {
	SchemaVersion             int         `json:"schemaVersion"`
	AttachmentRegistryVersion int         `json:"attachmentRegistryVersion"`
	Status                    string      `json:"status"`
	Phase                     string      `json:"phase"`
	OperationID               string      `json:"operationId"`
	Checkout                  checkout    `json:"checkout"`
	Runtime                   runtimeID   `json:"runtime"`
	Endpoint                  endpointID  `json:"endpoint"`
	Network                   networkID   `json:"network"`
	Authority                 authorityID `json:"authority"`
	Session                   sessionID   `json:"session"`
	Reset                     *resetState `json:"reset,omitempty"`
	LastError                 *failure    `json:"lastError,omitempty"`
}

type resetState struct {
	Stage     string          `json:"stage"`
	Resources []OwnedResource `json:"resources"`
}

type checkout struct {
	CanonicalRoot string `json:"canonicalRoot"`
	ID            string `json:"checkoutId"`
}

type runtimeID struct {
	OwnerID        string `json:"ownerId"`
	ComposeProject string `json:"composeProject"`
	ManifestDigest string `json:"manifestDigest"`
	Version        string `json:"version"`
	Revision       string `json:"revision"`
}

type endpointID struct {
	Host        string `json:"host"`
	ServerID    string `json:"serverId"`
	Fingerprint string `json:"fingerprint"`
}

type networkID struct {
	AppPort int    `json:"appPort"`
	URL     string `json:"url"`
}

type authorityID struct {
	InstanceID          string `json:"instanceId,omitempty"`
	Environment         string `json:"environment"`
	IssuerID            string `json:"issuerId"`
	ProjectUID          string `json:"projectUid"`
	PoolID              string `json:"poolId,omitempty"`
	CompatibilityDigest string `json:"compatibilityDigest,omitempty"`
	EvidenceDigest      string `json:"evidenceDigest,omitempty"`
	ConformanceVersion  string `json:"conformanceVersion,omitempty"`
}

type sessionID struct {
	TargetName string `json:"targetName,omitempty"`
	SessionID  string `json:"sessionId,omitempty"`
}

type SessionRequest struct {
	TargetName  string
	Origin      string
	InstanceID  string
	Environment string
	ProjectID   string
}

type SessionResult struct {
	TargetName string
	SessionID  string
}

type AttachmentStatus struct {
	ID          string    `json:"id"`
	PID         int       `json:"pid"`
	AttachedAt  time.Time `json:"attachedAt"`
	HeartbeatAt time.Time `json:"heartbeatAt"`
}

type LifecycleStatus struct {
	Exists             bool                      `json:"exists"`
	RuntimeStatus      string                    `json:"runtimeStatus,omitempty"`
	Phase              string                    `json:"phase,omitempty"`
	CheckoutRoot       string                    `json:"checkoutRoot"`
	CheckoutID         string                    `json:"checkoutId"`
	StateRoot          string                    `json:"stateRoot,omitempty"`
	ComposeProject     string                    `json:"composeProject,omitempty"`
	OwnerID            string                    `json:"ownerId,omitempty"`
	URL                string                    `json:"url,omitempty"`
	TargetName         string                    `json:"targetName,omitempty"`
	TargetID           string                    `json:"targetId,omitempty"`
	ProjectID          string                    `json:"projectId,omitempty"`
	Services           map[string]string         `json:"services,omitempty"`
	Attachments        []AttachmentStatus        `json:"attachments"`
	DevelopmentProfile *DevelopmentProfileStatus `json:"developmentProfile,omitempty"`
}

// DevelopmentProfileStatus is the deliberately redacted portion of the
// target-owned profile-application checkpoint shown by `dev status`.
type DevelopmentProfileStatus struct {
	ApplicationID              string   `json:"applicationId"`
	Status                     string   `json:"status"`
	ProfileName                string   `json:"profileName"`
	LastCompletedApplicationID string   `json:"lastCompletedApplicationId,omitempty"`
	LastCompletedAt            string   `json:"lastCompletedAt,omitempty"`
	RequiredConnections        int32    `json:"requiredConnections"`
	AppliedConnections         int32    `json:"appliedConnections"`
	IncompleteConnections      []string `json:"incompleteConnections"`
	UpdatedAt                  string   `json:"updatedAt"`
}

type OwnedResource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type ResetPlan struct {
	CheckoutRoot string          `json:"checkoutRoot"`
	CheckoutID   string          `json:"checkoutId"`
	StateRoot    string          `json:"stateRoot"`
	Resources    []OwnedResource `json:"resources"`
	Confirmation string          `json:"confirmation"`
}

type DetachResult struct {
	RemainingAttachments int  `json:"remainingAttachments"`
	ServicesStopped      bool `json:"servicesStopped"`
}

type ProjectAuthority struct {
	IssuerID   string
	ProjectUID string
}

type failure struct {
	Phase string `json:"phase"`
	Code  string `json:"code"`
}

type runtimeManifest struct {
	SchemaVersion                int              `json:"schemaVersion"`
	PersistentStateSchemaVersion int              `json:"persistentStateSchemaVersion"`
	ComposeMinimumVersion        string           `json:"composeMinimumVersion"`
	LeapView                     manifestLeapView `json:"leapview"`
	Postgres                     manifestPostgres `json:"postgres"`
}

type manifestLeapView struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Image    string `json:"image"`
}

type manifestPostgres struct {
	Major int    `json:"major"`
	Image string `json:"image"`
}
