package connectionbinding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/analytics/connectors"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrInvalidBinding                 = apigenfailure.New("invalid", "invalid connection binding")
	ErrBindingNotFound                = apigenfailure.New("not_found", "connection binding not found")
	ErrIncompatibleBinding            = apigenfailure.New("conflict", "incompatible connection binding")
	ErrDisabledBinding                = apigenfailure.New("disabled", "connection binding disabled")
	ErrUnauthorizedBinding            = apigenfailure.New("unauthorized", "connection binding unauthorized")
	ErrCredentialSerialization        = apigenfailure.New("credential_serialization", "credential snapshot cannot be serialized")
	ErrCredentialDenied               = apigenfailure.New("credential_invalid", "credential access denied")
	ErrCredentialNotFound             = apigenfailure.New("credential_invalid", "credential not found")
	ErrCredentialRateLimited          = apigenfailure.New("provider_unavailable", "credential provider rate limited")
	ErrProviderUnavailable            = apigenfailure.New("provider_unavailable", "credential provider unavailable")
	ErrInvalidCredentialBundle        = apigenfailure.New("credential_invalid", "invalid credential bundle")
	ErrRotationAuditUnavailable       = apigenfailure.New("audit_unavailable", "credential rotation audit unavailable")
	ErrAdministrationAuditUnavailable = apigenfailure.New("audit_unavailable", "connection administration audit unavailable")

	identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$`)
	optionKeyPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
)

// ConnectionID is the explicit project-graph identity of a connection. The
// symbolic connection name is resolved by the active project graph before a
// binding is created and is never persisted as an execution key.
func ParseConnectionID(value string) (projectgraph.ResourceID, error) {
	id, err := projectgraph.NewResourceID(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("%w: connection id is invalid", ErrInvalidBinding)
	}
	return id, nil
}

type AuthenticationMode string

const (
	AuthenticationNone           AuthenticationMode = "none"
	AuthenticationExternalBundle AuthenticationMode = "external_bundle"
	AuthenticationWorkload       AuthenticationMode = "workload_identity"
)

type BindingHealth string

const (
	HealthPending  BindingHealth = "pending"
	HealthHealthy  BindingHealth = "healthy"
	HealthDegraded BindingHealth = "degraded"
	HealthDisabled BindingHealth = "disabled"
)

type BindingScope struct {
	ProjectID   projectgraph.ResourceID `json:"projectId"`
	Environment string                  `json:"environment"`
}

type EndpointConfig struct {
	Host           string            `json:"host,omitempty"`
	Port           int               `json:"port,omitempty"`
	Database       string            `json:"database,omitempty"`
	ObjectScope    string            `json:"objectScope,omitempty"`
	SourceIdentity string            `json:"sourceIdentity,omitempty"`
	TLSMode        string            `json:"tlsMode,omitempty"`
	Options        map[string]string `json:"options,omitempty"`
}

type CredentialReference struct {
	ProjectID   projectgraph.ResourceID `json:"projectId"`
	Environment string                  `json:"environment"`
	SecretPath  string                  `json:"secretPath"`
	SecretKey   string                  `json:"secretKey"`
}

func (reference CredentialReference) empty() bool {
	return reference == (CredentialReference{})
}

func (reference CredentialReference) valid() bool {
	return reference.ProjectID.Valid() &&
		strings.TrimSpace(reference.Environment) != "" &&
		strings.HasPrefix(strings.TrimSpace(reference.SecretPath), "/") &&
		strings.TrimSpace(reference.SecretKey) != ""
}

type TargetBinding struct {
	ID                  BindingID
	TargetID            string
	ConnectionID        projectgraph.ResourceID
	ConnectorKind       string
	AuthenticationMode  AuthenticationMode
	Scope               BindingScope
	Endpoint            EndpointConfig
	CredentialReference CredentialReference
	Enabled             bool
	ValidatedVersion    string
	Health              BindingHealth
	HealthReason        string
	LastValidatedAt     time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Revision            int64
}

type TargetBindingInput struct {
	ID                  BindingID
	TargetID            string
	ConnectionID        projectgraph.ResourceID
	ConnectorKind       string
	AuthenticationMode  AuthenticationMode
	Scope               BindingScope
	Endpoint            EndpointConfig
	CredentialReference CredentialReference
	Enabled             bool
	Now                 time.Time
}

type TargetBindingConfiguration struct {
	ConnectorKind       string
	AuthenticationMode  AuthenticationMode
	Endpoint            EndpointConfig
	CredentialReference CredentialReference
}

func NewTargetBinding(input TargetBindingInput) (TargetBinding, error) {
	if input.ID.String() != strings.TrimSpace(input.ID.String()) ||
		input.ConnectionID.String() != strings.TrimSpace(input.ConnectionID.String()) ||
		input.Scope.ProjectID.String() != strings.TrimSpace(input.Scope.ProjectID.String()) {
		return TargetBinding{}, fmt.Errorf("%w: binding graph identities must be canonical", ErrInvalidBinding)
	}
	connectionID, err := ParseConnectionID(input.ConnectionID.String())
	if err != nil {
		return TargetBinding{}, err
	}
	input.ID = BindingID(input.ID.String())
	input.TargetID = strings.TrimSpace(input.TargetID)
	input.ConnectorKind = strings.TrimSpace(input.ConnectorKind)
	input.Scope.ProjectID = projectgraph.ResourceID(input.Scope.ProjectID.String())
	input.Scope.Environment = strings.TrimSpace(input.Scope.Environment)
	input.Now = input.Now.UTC()
	if _, err := ParseBindingID(input.ID.String()); err != nil || !identifierPattern.MatchString(input.TargetID) ||
		!identifierPattern.MatchString(input.ConnectorKind) || !input.Scope.ProjectID.Valid() ||
		!identifierPattern.MatchString(input.Scope.Environment) || input.Now.IsZero() {
		return TargetBinding{}, fmt.Errorf("%w: binding identity, target, connector, scope, environment, and creation time are required", ErrInvalidBinding)
	}
	if _, ok := connectors.LookupConnection(input.ConnectorKind); !ok {
		return TargetBinding{}, fmt.Errorf("%w: unsupported connector kind %q", ErrInvalidBinding, input.ConnectorKind)
	}
	if err := validateEndpoint(input.Endpoint); err != nil {
		return TargetBinding{}, err
	}
	switch input.AuthenticationMode {
	case AuthenticationExternalBundle:
		if !input.CredentialReference.valid() {
			return TargetBinding{}, fmt.Errorf("%w: external bundle authentication requires a complete credential reference", ErrInvalidBinding)
		}
	case AuthenticationNone, AuthenticationWorkload:
		if !input.CredentialReference.empty() {
			return TargetBinding{}, fmt.Errorf("%w: %s authentication cannot persist a credential reference", ErrInvalidBinding, input.AuthenticationMode)
		}
	default:
		return TargetBinding{}, fmt.Errorf("%w: unsupported authentication mode %q", ErrInvalidBinding, input.AuthenticationMode)
	}
	health := HealthPending
	if !input.Enabled {
		health = HealthDisabled
	}
	return TargetBinding{
		ID: input.ID, TargetID: input.TargetID, ConnectionID: connectionID,
		ConnectorKind: input.ConnectorKind, AuthenticationMode: input.AuthenticationMode,
		Scope: input.Scope, Endpoint: cloneEndpoint(input.Endpoint),
		CredentialReference: canonicalReference(input.CredentialReference),
		Enabled:             input.Enabled, Health: health, CreatedAt: input.Now, UpdatedAt: input.Now, Revision: 1,
	}, nil
}

func (binding TargetBinding) Validate() error {
	connectionID, err := ParseConnectionID(binding.ConnectionID.String())
	if err != nil || connectionID != binding.ConnectionID {
		return fmt.Errorf("%w: connection identity is invalid", ErrInvalidBinding)
	}
	if _, err := ParseBindingID(binding.ID.String()); err != nil || !identifierPattern.MatchString(binding.TargetID) ||
		!identifierPattern.MatchString(binding.ConnectorKind) || !binding.Scope.ProjectID.Valid() ||
		!identifierPattern.MatchString(binding.Scope.Environment) {
		return fmt.Errorf("%w: binding identity, target, connector, scope, and environment are required", ErrInvalidBinding)
	}
	if _, ok := connectors.LookupConnection(binding.ConnectorKind); !ok {
		return fmt.Errorf("%w: unsupported connector kind %q", ErrInvalidBinding, binding.ConnectorKind)
	}
	if err := validateEndpoint(binding.Endpoint); err != nil {
		return err
	}
	switch binding.AuthenticationMode {
	case AuthenticationExternalBundle:
		if !binding.CredentialReference.valid() ||
			binding.CredentialReference != canonicalReference(binding.CredentialReference) {
			return fmt.Errorf("%w: external bundle authentication requires a canonical credential reference", ErrInvalidBinding)
		}
	case AuthenticationNone, AuthenticationWorkload:
		if !binding.CredentialReference.empty() {
			return fmt.Errorf("%w: %s authentication cannot persist a credential reference", ErrInvalidBinding, binding.AuthenticationMode)
		}
	default:
		return fmt.Errorf("%w: unsupported authentication mode %q", ErrInvalidBinding, binding.AuthenticationMode)
	}
	switch binding.Health {
	case HealthPending, HealthHealthy, HealthDegraded, HealthDisabled:
	default:
		return fmt.Errorf("%w: unsupported binding health %q", ErrInvalidBinding, binding.Health)
	}
	if binding.Enabled == (binding.Health == HealthDisabled) {
		return fmt.Errorf("%w: enabled state and health are inconsistent", ErrInvalidBinding)
	}
	if binding.Health == HealthHealthy && (strings.TrimSpace(binding.ValidatedVersion) == "" || binding.LastValidatedAt.IsZero()) {
		return fmt.Errorf("%w: healthy binding requires validation evidence", ErrInvalidBinding)
	}
	if binding.Health == HealthDegraded {
		if !safeDiagnosticCode(binding.HealthReason) {
			return fmt.Errorf("%w: degraded binding requires a bounded diagnostic code", ErrInvalidBinding)
		}
	} else if binding.HealthReason != "" {
		return fmt.Errorf("%w: only degraded bindings may retain a health reason", ErrInvalidBinding)
	}
	if binding.Revision <= 0 || binding.CreatedAt.IsZero() || binding.UpdatedAt.Before(binding.CreatedAt) ||
		!binding.LastValidatedAt.IsZero() && (binding.LastValidatedAt.Before(binding.CreatedAt) || binding.LastValidatedAt.After(binding.UpdatedAt)) {
		return fmt.Errorf("%w: binding revision and timestamps are invalid", ErrInvalidBinding)
	}
	return nil
}

type Requirement struct {
	ConnectionID     projectgraph.ResourceID
	ConnectorKind    string
	BindingRevision  int64
	ValidatedVersion string
}

type BindingEvidence struct {
	BindingID          BindingID                    `json:"bindingId"`
	TargetID           string                       `json:"targetId"`
	ConnectionID       projectgraph.ResourceID      `json:"connectionId"`
	Identity           projectgraph.ServingIdentity `json:"identity"`
	ConnectorKind      string                       `json:"connectorKind"`
	Scope              BindingScope                 `json:"scope"`
	BindingRevision    int64                        `json:"bindingRevision"`
	ValidatedVersion   string                       `json:"validatedVersion,omitempty"`
	EndpointConfigHash string                       `json:"endpointConfigHash"`
	Health             BindingHealth                `json:"health"`
}

func (binding TargetBinding) Evidence() BindingEvidence {
	return BindingEvidence{
		BindingID: binding.ID, TargetID: binding.TargetID, ConnectionID: binding.ConnectionID,
		ConnectorKind: binding.ConnectorKind, Scope: binding.Scope, BindingRevision: binding.Revision,
		ValidatedVersion: binding.ValidatedVersion, EndpointConfigHash: endpointDigest(binding.Endpoint), Health: binding.Health,
	}
}

func (binding TargetBinding) CompatibleEvidence(requirement Requirement, authorized bool) (BindingEvidence, error) {
	if !authorized {
		return BindingEvidence{}, ErrUnauthorizedBinding
	}
	if !binding.Enabled {
		return BindingEvidence{}, ErrDisabledBinding
	}
	if requirement.ConnectionID != binding.ConnectionID ||
		strings.TrimSpace(requirement.ConnectorKind) != binding.ConnectorKind ||
		requirement.BindingRevision > 0 && requirement.BindingRevision != binding.Revision ||
		requirement.ValidatedVersion != "" && requirement.ValidatedVersion != binding.ValidatedVersion {
		return BindingEvidence{}, ErrIncompatibleBinding
	}
	return binding.Evidence(), nil
}

func (binding TargetBinding) MarkValidated(providerVersion string, now time.Time) (TargetBinding, error) {
	providerVersion = strings.TrimSpace(providerVersion)
	now = now.UTC()
	if !binding.Enabled {
		return TargetBinding{}, ErrDisabledBinding
	}
	if providerVersion == "" || now.IsZero() || now.Before(binding.UpdatedAt) {
		return TargetBinding{}, fmt.Errorf("%w: provider version and monotonic validation time are required", ErrInvalidBinding)
	}
	if binding.Health == HealthHealthy && binding.ValidatedVersion == providerVersion && binding.LastValidatedAt.Equal(now) {
		return binding, nil
	}
	binding.ValidatedVersion = providerVersion
	binding.Health = HealthHealthy
	binding.HealthReason = ""
	binding.LastValidatedAt = now
	return binding.advance(now), nil
}

func (binding TargetBinding) Configuration() TargetBindingConfiguration {
	return TargetBindingConfiguration{
		ConnectorKind: binding.ConnectorKind, AuthenticationMode: binding.AuthenticationMode,
		Endpoint: cloneEndpoint(binding.Endpoint), CredentialReference: binding.CredentialReference,
	}
}

func (binding TargetBinding) UpdateConfiguration(configuration TargetBindingConfiguration, now time.Time) (TargetBinding, error) {
	now = now.UTC()
	if !binding.Enabled {
		return TargetBinding{}, ErrDisabledBinding
	}
	if now.IsZero() || now.Before(binding.UpdatedAt) {
		return TargetBinding{}, fmt.Errorf("%w: monotonic update time is required", ErrInvalidBinding)
	}
	configuration.ConnectorKind = strings.TrimSpace(configuration.ConnectorKind)
	configuration.CredentialReference = canonicalReference(configuration.CredentialReference)
	candidate := binding
	candidate.ConnectorKind = configuration.ConnectorKind
	candidate.AuthenticationMode = configuration.AuthenticationMode
	candidate.Endpoint = cloneEndpoint(configuration.Endpoint)
	candidate.CredentialReference = configuration.CredentialReference
	if reflect.DeepEqual(candidate.Configuration(), binding.Configuration()) {
		return binding, nil
	}
	candidate.ValidatedVersion = ""
	candidate.LastValidatedAt = time.Time{}
	candidate.Health = HealthPending
	candidate.HealthReason = ""
	candidate = candidate.advance(now)
	if err := candidate.Validate(); err != nil {
		return TargetBinding{}, err
	}
	return candidate, nil
}

func (binding TargetBinding) MarkDegraded(reason string, now time.Time) (TargetBinding, error) {
	reason = strings.TrimSpace(reason)
	now = now.UTC()
	if !binding.Enabled {
		return TargetBinding{}, ErrDisabledBinding
	}
	if !safeDiagnosticCode(reason) || now.IsZero() || now.Before(binding.UpdatedAt) {
		return TargetBinding{}, fmt.Errorf("%w: bounded diagnostic code and monotonic time are required", ErrInvalidBinding)
	}
	if binding.Health == HealthDegraded && binding.HealthReason == reason {
		return binding, nil
	}
	binding.Health = HealthDegraded
	binding.HealthReason = reason
	return binding.advance(now), nil
}

func (binding TargetBinding) Disable(now time.Time) (TargetBinding, error) {
	now = now.UTC()
	if !binding.Enabled {
		return binding, nil
	}
	if now.IsZero() || now.Before(binding.UpdatedAt) {
		return TargetBinding{}, fmt.Errorf("%w: monotonic disable time is required", ErrInvalidBinding)
	}
	binding.Enabled = false
	binding.Health = HealthDisabled
	binding.HealthReason = ""
	return binding.advance(now), nil
}

func (binding TargetBinding) Enable(now time.Time) (TargetBinding, error) {
	now = now.UTC()
	if binding.Enabled {
		return binding, nil
	}
	if now.IsZero() || now.Before(binding.UpdatedAt) {
		return TargetBinding{}, fmt.Errorf("%w: monotonic enable time is required", ErrInvalidBinding)
	}
	binding.Enabled = true
	binding.Health = HealthPending
	binding.HealthReason = ""
	binding.ValidatedVersion = ""
	binding.LastValidatedAt = time.Time{}
	return binding.advance(now), nil
}

func (binding TargetBinding) advance(now time.Time) TargetBinding {
	binding.UpdatedAt = now.UTC()
	binding.Revision++
	return binding
}

func safeDiagnosticCode(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}

func validateEndpoint(endpoint EndpointConfig) error {
	if endpoint.Port < 0 || endpoint.Port > 65535 {
		return fmt.Errorf("%w: endpoint port is outside the valid range", ErrInvalidBinding)
	}
	for name, value := range map[string]string{
		"host": endpoint.Host, "database": endpoint.Database, "object scope": endpoint.ObjectScope,
		"source identity": endpoint.SourceIdentity, "TLS mode": endpoint.TLSMode,
	} {
		if value != strings.TrimSpace(value) {
			return fmt.Errorf("%w: endpoint %s must be canonical", ErrInvalidBinding, name)
		}
	}
	for key := range endpoint.Options {
		normalized := strings.ToLower(key)
		if !optionKeyPattern.MatchString(key) ||
			strings.Contains(normalized, "password") || strings.Contains(normalized, "secret") ||
			strings.Contains(normalized, "token") || strings.Contains(normalized, "credential") ||
			strings.Contains(normalized, "private_key") || strings.Contains(normalized, "access_key") {
			return fmt.Errorf("%w: endpoint option %q is not non-secret configuration", ErrInvalidBinding, key)
		}
	}
	return nil
}

func cloneEndpoint(endpoint EndpointConfig) EndpointConfig {
	result := endpoint
	if endpoint.Options != nil {
		result.Options = make(map[string]string, len(endpoint.Options))
		for key, value := range endpoint.Options {
			result.Options[key] = value
		}
	}
	return result
}

func canonicalReference(reference CredentialReference) CredentialReference {
	return CredentialReference{
		ProjectID: strings.TrimSpace(reference.ProjectID), Environment: strings.TrimSpace(reference.Environment),
		SecretPath: strings.TrimSpace(reference.SecretPath), SecretKey: strings.TrimSpace(reference.SecretKey),
	}
}

func endpointDigest(endpoint EndpointConfig) string {
	encoded, _ := json.Marshal(endpoint)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type CredentialSnapshot struct {
	values          map[string]string
	providerVersion string
	retrievedAt     time.Time
	expiresAt       time.Time
}

func NewCredentialSnapshot(values map[string]string, providerVersion string, retrievedAt, expiresAt time.Time) (CredentialSnapshot, error) {
	providerVersion = strings.TrimSpace(providerVersion)
	retrievedAt = retrievedAt.UTC()
	expiresAt = expiresAt.UTC()
	if len(values) == 0 || providerVersion == "" || retrievedAt.IsZero() ||
		!expiresAt.IsZero() && !expiresAt.After(retrievedAt) {
		return CredentialSnapshot{}, fmt.Errorf("%w: credential fields, provider version, and retrieval time are required", ErrInvalidBinding)
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		if !optionKeyPattern.MatchString(key) || value == "" {
			return CredentialSnapshot{}, fmt.Errorf("%w: credential bundle contains an invalid field", ErrInvalidBinding)
		}
		cloned[key] = value
	}
	return CredentialSnapshot{values: cloned, providerVersion: providerVersion, retrievedAt: retrievedAt, expiresAt: expiresAt}, nil
}

func (snapshot CredentialSnapshot) ProviderVersion() string { return snapshot.providerVersion }
func (snapshot CredentialSnapshot) RetrievedAt() time.Time  { return snapshot.retrievedAt }
func (snapshot CredentialSnapshot) ExpiresAt() time.Time    { return snapshot.expiresAt }

func (snapshot CredentialSnapshot) Use(consumer func(map[string]string) error) error {
	if consumer == nil || len(snapshot.values) == 0 {
		return fmt.Errorf("%w: credential snapshot consumer is required", ErrInvalidBinding)
	}
	values := make(map[string]string, len(snapshot.values))
	for key, value := range snapshot.values {
		values[key] = value
	}
	defer clear(values)
	return consumer(values)
}

func (snapshot *CredentialSnapshot) Destroy() {
	if snapshot == nil {
		return
	}
	clear(snapshot.values)
	snapshot.values = nil
	snapshot.providerVersion = ""
	snapshot.retrievedAt = time.Time{}
	snapshot.expiresAt = time.Time{}
}

func (CredentialSnapshot) MarshalJSON() ([]byte, error) {
	return nil, ErrCredentialSerialization
}

func (CredentialSnapshot) MarshalYAML() (any, error) {
	return nil, ErrCredentialSerialization
}

func (CredentialSnapshot) String() string { return "<credential-snapshot:redacted>" }
func (CredentialSnapshot) GoString() string {
	return "connectionbinding.CredentialSnapshot{<redacted>}"
}

func (snapshot CredentialSnapshot) LogValue() slog.Value {
	attributes := []slog.Attr{
		slog.String("provider_version", snapshot.providerVersion),
		slog.Time("retrieved_at", snapshot.retrievedAt),
	}
	if !snapshot.expiresAt.IsZero() {
		attributes = append(attributes, slog.Time("expires_at", snapshot.expiresAt))
	}
	return slog.GroupValue(attributes...)
}
