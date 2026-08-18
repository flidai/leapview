// Package offline owns offline administrative use cases and the ports they
// require from application composition.
package offline

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/deployment"
)

const (
	CredentialRecoveryFileName = ".initial-credentials.json"
)

var ErrStateNotFound = errors.New("offline Admin state was not found")

// Config is the normalized immutable process configuration required by
// offline Admin use cases.
type Config struct {
	HomeDir            string
	DBPath             string
	Environment        string
	Production         bool
	BootstrapEmail     string
	DuckLakeCatalog    string
	DuckLakeData       string
	ArtifactDir        string
	RuntimeDir         string
	ManagedDataDir     string
	ManagedDataBackend string
}

type InitializeRequest struct {
	Format string
}

type StorageCleanupRequest struct {
	Apply bool
}

type MaintenanceRequest struct {
	Apply             bool
	AuditDays         int
	QueryDays         int
	ArchivedAgentDays int
	AuthStateDays     int
}

type BackupRequest struct {
	Out          string
	DatabaseOnly bool
}

type RestoreRequest struct {
	From          string
	CurrentBackup string
	Confirm       bool
	DatabaseOnly  bool
}

// PhysicalPoolBootstrapRequest is the offline operator input for the
// controlled pre-release pool admission path. It contains only non-secret
// pool identity and compatibility evidence; credentials remain target-owned
// references in PoolIdentity.
type PhysicalPoolBootstrapRequest struct {
	Pool     physicalpool.PoolIdentity
	Evidence physicalpool.Evidence
	Apply    bool
}

type PhysicalPoolBootstrapResult struct {
	PoolID              string
	CompatibilityDigest string
	EvidenceDigest      string
	ConformanceVersion  string
	Applied             bool
}

// DeliveryRepairRequest identifies one durable delivery root and one bounded
// control-plane repair action. Repair never accepts an arbitrary object key or
// physical deletion command; the application adapter verifies the exact
// SQLite root, immutable artifact bytes/digest, and DuckLake closure first.
type DeliveryRepairRequest struct {
	Root   deployment.DeliveryRoot
	Action string
	Apply  bool
}

// InitialCredentials are the one-time credentials returned by instance
// initialization.
type InitialCredentials struct {
	Email                   string `json:"email"`
	TemporaryPassword       string `json:"temporaryPassword"`
	PublisherToken          string `json:"publisherToken"`
	PublisherTokenExpiresAt string `json:"publisherTokenExpiresAt"`
}

type InitializationInput struct {
	Email       string
	Environment string
	Now         time.Time
}

// Initializer owns the audited Access mutation used to initialize an instance.
// prepare runs inside the same transaction, before commit, so recovery material
// is durable before credentials become active.
type Initializer interface {
	Initialize(context.Context, InitializationInput, func(InitialCredentials) error) (InitialCredentials, error)
}

type InstanceState interface {
	Environment(context.Context) (string, error)
	ExistingEnvironment(context.Context) (string, bool, error)
	BindEnvironment(context.Context, string) error
	Initialized(context.Context) (bool, error)
}

type CredentialRecovery interface {
	Read() ([]byte, error)
	Write([]byte) error
	Remove() error
}

type Lock interface {
	Release() error
}

type Locker interface {
	Acquire(context.Context) (Lock, error)
}

type RetentionPolicy struct {
	AuditEventsMaxAge             time.Duration
	QueryEventsMaxAge             time.Duration
	ArchivedAgentConversationsAge time.Duration
	AuthStateMaxAge               time.Duration
	DryRun                        bool
}

type RetentionResult struct {
	AuditEventsDeleted                  int64
	QueryEventsDeleted                  int64
	ArchivedAgentConversationsDeleted   int64
	ExpiredOAuthStatesDeleted           int64
	StaleSessionsDeleted                int64
	StaleAPITokensDeleted               int64
	StaleServicePrincipalSecretsDeleted int64
}

type Retention interface {
	Prune(context.Context, RetentionPolicy) (RetentionResult, error)
}

type StorageCleaner interface {
	Cleanup(context.Context, string, bool, io.Writer) error
}

type PhysicalPoolBootstrap interface {
	Bootstrap(context.Context, PhysicalPoolBootstrapRequest) (PhysicalPoolBootstrapResult, error)
}

type DeliveryRepair interface {
	RepairDeliveryRoot(context.Context, DeliveryRepairRequest, io.Writer) error
}

// PhysicalPoolEvidenceValidator is supplied by the DuckLake adapter so the
// offline command enforces the exact versioned checklist before either a
// dry-run result or a database mutation is acknowledged.
type PhysicalPoolEvidenceValidator interface {
	ValidateEvidence(physicalpool.Evidence) error
}

type BackupOptions struct {
	Path                 string
	Writer               io.Writer
	ExcludeRelativePaths []string
}

type RestoreOptions struct {
	Path                 string
	Reader               io.Reader
	CurrentBackup        string
	DiscardCurrentBackup bool
	ExpectedEnvironment  string
	ResetRelativePaths   []string
}

type Archive interface {
	BackupDatabase(context.Context, BackupOptions) error
	BackupInstance(context.Context, BackupOptions) error
	RestoreDatabase(context.Context, RestoreOptions) error
	RestoreInstance(context.Context, RestoreOptions) error
}

type Dependencies struct {
	Locker         Locker
	State          InstanceState
	Initializer    Initializer
	Recovery       CredentialRecovery
	Retention      Retention
	Storage        StorageCleaner
	PhysicalPool   PhysicalPoolBootstrap
	DeliveryRepair DeliveryRepair
	Archive        Archive
	Now            func() time.Time
}

type Service struct {
	config Config
	deps   Dependencies
}

func New(config Config, dependencies Dependencies) *Service {
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	return &Service{config: config, deps: dependencies}
}
