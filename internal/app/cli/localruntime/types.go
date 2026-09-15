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
	stateSchemaVersion           = 1
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
	Stdout                  io.Writer
	Sleep                   func(context.Context, time.Duration) error
}

type State struct {
	SchemaVersion int         `json:"schemaVersion"`
	Status        string      `json:"status"`
	Phase         string      `json:"phase"`
	OperationID   string      `json:"operationId"`
	Checkout      checkout    `json:"checkout"`
	Runtime       runtimeID   `json:"runtime"`
	Endpoint      endpointID  `json:"endpoint"`
	Network       networkID   `json:"network"`
	Authority     authorityID `json:"authority"`
	Session       sessionID   `json:"session"`
	LastError     *failure    `json:"lastError,omitempty"`
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
