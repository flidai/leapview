package connectionbinding

// This file owns the small local-development profile application checkpoint.
// It does not apply bindings, resolve credentials, or replace the binding
// repository. It retains only exact non-secret intent and protected binding
// evidence needed to close and reopen profile-dependent admission.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrInvalidProfileApplication     = errors.New("invalid profile application")
	ErrProfileApplicationNotFound    = errors.New("profile application not found")
	ErrProfileApplicationConflict    = errors.New("profile application optimistic conflict")
	ErrProfileApplicationReplacement = errors.New("profile application replacement required")
	ErrProfileApplicationNotAdmitted = errors.New("profile application admission denied")
)

// ProfileApplicationStatus is the durable lifecycle of one exact local
// profile intent. Only Applied opens profile-dependent runtime admission.
type ProfileApplicationStatus string

const (
	ProfileApplicationApplying   ProfileApplicationStatus = "applying"
	ProfileApplicationIncomplete ProfileApplicationStatus = "incomplete"
	ProfileApplicationApplied    ProfileApplicationStatus = "applied"
)

// ProfileApplicationID identifies one retained application attempt. It is
// opaque metadata and never contains profile or credential material.
type ProfileApplicationID string

// ProfileApplicationScope is the checkout/runtime and project/environment
// scope. TargetID is passed separately to the store because it is the target's
// durable lookup key, matching the existing binding repository convention.
type ProfileApplicationScope struct {
	CheckoutID  string                  `json:"checkoutId"`
	RuntimeID   string                  `json:"runtimeId"`
	ProjectID   projectgraph.ResourceID `json:"projectId"`
	Environment string                  `json:"environment"`
}

// ProfileApplicationConnection is exact non-secret binding intent plus
// protected credential-version evidence. Expected connections are immutable
// application identity; applied connections retain the reconciled revision.
type ProfileApplicationConnection struct {
	BindingID           BindingID               `json:"bindingId"`
	ConnectionID        projectgraph.ResourceID `json:"connectionId"`
	ConnectorKind       string                  `json:"connectorKind"`
	AuthenticationMode  AuthenticationMode      `json:"authenticationMode"`
	Endpoint            EndpointConfig          `json:"endpoint"`
	CredentialReference CredentialReference     `json:"credentialReference"`
	BindingRevision     int64                   `json:"bindingRevision"`
	ProviderVersion     string                  `json:"providerVersion"`
}

// ProfileApplicationRequiredConnection is the immutable graph portion of one
// eligible connection. Runtime evidence is kept separately on the record.
type ProfileApplicationRequiredConnection struct {
	ConnectionID  projectgraph.ResourceID `json:"connectionId"`
	ConnectorKind string                  `json:"connectorKind"`
}

// DevelopmentProfileDigestConnection is the non-secret, execution-relevant
// profile intent shared by local profile loading and runtime application. Human
// file paths and secret values are deliberately absent.
type DevelopmentProfileDigestConnection struct {
	ConnectionID       projectgraph.ResourceID `json:"connectionId"`
	ConnectorKind      string                  `json:"connectorKind"`
	Endpoint           EndpointConfig          `json:"endpoint"`
	CredentialVariable string                  `json:"credentialVariable,omitempty"`
	Unauthenticated    bool                    `json:"unauthenticated,omitempty"`
}

// DevelopmentProfileDigest returns the canonical identity of one selected
// profile. Both the host loader and local runtime recompute this value so a
// caller cannot pair a trusted digest with different binding intent.
func DevelopmentProfileDigest(profileName string, values []DevelopmentProfileDigestConnection) (string, error) {
	if !validApplicationToken(profileName) {
		return "", ErrInvalidProfileApplication
	}
	connections := append([]DevelopmentProfileDigestConnection(nil), values...)
	for index := range connections {
		connection := &connections[index]
		if connection.ConnectionID.Validate() != nil || !validApplicationToken(connection.ConnectorKind) || validateEndpoint(connection.Endpoint) != nil {
			return "", ErrInvalidProfileApplication
		}
		if len(connection.Endpoint.Options) == 0 {
			connection.Endpoint.Options = nil
		}
		if connection.Unauthenticated == (connection.CredentialVariable != "") {
			return "", ErrInvalidProfileApplication
		}
		if connection.CredentialVariable != "" && !validDevelopmentProfileCredentialVariable(connection.CredentialVariable) {
			return "", ErrInvalidProfileApplication
		}
	}
	sort.Slice(connections, func(i, j int) bool { return connections[i].ConnectionID < connections[j].ConnectionID })
	for index := 1; index < len(connections); index++ {
		if connections[index-1].ConnectionID == connections[index].ConnectionID {
			return "", ErrInvalidProfileApplication
		}
	}
	identity := struct {
		Version     int                                  `json:"version"`
		ProfileName string                               `json:"profileName"`
		Connections []DevelopmentProfileDigestConnection `json:"connections"`
	}{Version: 1, ProfileName: profileName, Connections: connections}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", ErrInvalidProfileApplication
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validDevelopmentProfileCredentialVariable(value string) bool {
	const prefix = "LEAPVIEW_DEV_CONNECTION_"
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return false
	}
	for _, char := range strings.TrimPrefix(value, prefix) {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}

// ProfileApplicationRecord is one durable optimistic application checkpoint.
// SourceDigest records the source snapshot observed when an explicit new or
// replacement application began; it is not profile identity and does not make
// ordinary source edits require replacement. Profile and graph digests are
// non-secret content identities. Raw credential bundle hashes are intentionally
// absent; protected provider-version evidence is retained on each eligible
// connection instead.
type ProfileApplicationRecord struct {
	ID                         ProfileApplicationID                   `json:"id"`
	CheckoutID                 string                                 `json:"checkoutId"`
	RuntimeID                  string                                 `json:"runtimeId"`
	TargetID                   TargetID                               `json:"targetId"`
	ProjectID                  projectgraph.ResourceID                `json:"projectId"`
	Environment                string                                 `json:"environment"`
	ProfileName                string                                 `json:"profileName"`
	SourceDigest               string                                 `json:"sourceDigest"`
	GraphDigest                string                                 `json:"graphDigest"`
	ProfileDigest              string                                 `json:"profileDigest"`
	LastCompletedApplicationID ProfileApplicationID                   `json:"lastCompletedApplicationId,omitempty"`
	LastCompletedAt            time.Time                              `json:"lastCompletedAt,omitempty"`
	RequiredConnections        []ProfileApplicationRequiredConnection `json:"requiredConnections"`
	ExpectedConnections        []ProfileApplicationConnection         `json:"expectedConnections"`
	RetiredConnections         []ProfileApplicationConnection         `json:"retiredConnections"`
	AppliedConnections         []ProfileApplicationConnection         `json:"appliedConnections"`
	Status                     ProfileApplicationStatus               `json:"status"`
	Revision                   int64                                  `json:"revision"`
	CreatedAt                  time.Time                              `json:"createdAt"`
	UpdatedAt                  time.Time                              `json:"updatedAt"`
}

// ProfileApplication is retained as a concise name for the canonical record.
type ProfileApplication = ProfileApplicationRecord

// ProfileApplicationAdmissionRequest describes the complete graph/runtime
// requirements that are about to admit new local profile-dependent work.
type ProfileApplicationAdmissionRequest struct {
	CheckoutID          string                         `json:"checkoutId"`
	RuntimeID           string                         `json:"runtimeId"`
	TargetID            TargetID                       `json:"targetId"`
	ProjectID           projectgraph.ResourceID        `json:"projectId"`
	Environment         string                         `json:"environment"`
	ProfileName         string                         `json:"profileName"`
	GraphDigest         string                         `json:"graphDigest"`
	ProfileDigest       string                         `json:"profileDigest"`
	EligibleConnections []ProfileApplicationConnection `json:"eligibleConnections"`
}

type AdmissionRequest = ProfileApplicationAdmissionRequest

// ProfileApplicationStore is the narrow durable authority for checkpoints.
// Save uses expectedRevision as a compare-and-swap token. Implementations
// must reject immutable intent drift unless an explicit replacement operation
// is added by a higher-level owner.
type ProfileApplicationStore interface {
	Application(context.Context, ProfileApplicationScope, TargetID) (ProfileApplicationRecord, error)
	Save(context.Context, ProfileApplicationRecord, int64) (ProfileApplicationRecord, error)
	Replace(context.Context, ProfileApplicationRecord, int64) (ProfileApplicationRecord, error)
}

// ProfileApplicationIdentity returns the immutable profile-application
// portion of a record, including pre-mutation expected binding and protected
// credential evidence. The observed source snapshot is deliberately excluded.
type ProfileApplicationIdentity struct {
	ID                  ProfileApplicationID
	CheckoutID          string
	RuntimeID           string
	TargetID            TargetID
	ProjectID           projectgraph.ResourceID
	Environment         string
	ProfileName         string
	GraphDigest         string
	ProfileDigest       string
	EligibleConnections []ProfileApplicationRequiredConnection
	ExpectedConnections []ProfileApplicationConnection
	RetiredConnections  []ProfileApplicationConnection
}

func (identity ProfileApplicationIdentity) String() string {
	return fmt.Sprintf("profile application %s status-independent scope=checkout=%s runtime=%s project=%s environment=%s target=%s", identity.ID, identity.CheckoutID, identity.RuntimeID, identity.ProjectID, identity.Environment, identity.TargetID)
}

func (identity ProfileApplicationIdentity) GoString() string {
	return "<profile-application-identity>"
}

func (id ProfileApplicationID) String() string   { return string(id) }
func (id ProfileApplicationID) GoString() string { return "<profile-application-id>" }

// String and GoString intentionally omit digests and provider versions.
func (scope ProfileApplicationScope) String() string {
	return fmt.Sprintf("checkout=%s runtime=%s project=%s environment=%s", scope.CheckoutID, scope.RuntimeID, scope.ProjectID, scope.Environment)
}
func (scope ProfileApplicationScope) GoString() string { return "<profile-application-scope>" }

func (record ProfileApplicationRecord) String() string {
	return fmt.Sprintf("profile application %s status=%s scope=%s target=%s", record.ID, record.Status, record.scope().String(), record.TargetID)
}
func (record ProfileApplicationRecord) GoString() string { return "<profile-application-record>" }

func (connection ProfileApplicationConnection) String() string {
	return fmt.Sprintf("connection=%s revision=%d", connection.ConnectionID, connection.BindingRevision)
}
func (connection ProfileApplicationConnection) GoString() string {
	return "<profile-application-connection>"
}

func (connection ProfileApplicationRequiredConnection) String() string {
	return fmt.Sprintf("connection=%s", connection.ConnectionID)
}

func (connection ProfileApplicationRequiredConnection) GoString() string {
	return "<profile-application-required-connection>"
}

func (request ProfileApplicationAdmissionRequest) String() string {
	return fmt.Sprintf("profile admission scope=%s target=%s profile=%s", ProfileApplicationScope{CheckoutID: request.CheckoutID, RuntimeID: request.RuntimeID, ProjectID: request.ProjectID, Environment: request.Environment}, request.TargetID, request.ProfileName)
}
func (request ProfileApplicationAdmissionRequest) GoString() string {
	return "<profile-application-admission-request>"
}

func NewProfileApplication(record ProfileApplicationRecord) (ProfileApplicationRecord, error) {
	return normalizeProfileApplication(record, true)
}

func (record ProfileApplicationRecord) Validate() error {
	_, err := normalizeProfileApplication(record, false)
	return err
}

func (record ProfileApplicationRecord) Identity() ProfileApplicationIdentity {
	normalized, err := normalizeProfileApplication(record, false)
	if err != nil {
		return ProfileApplicationIdentity{}
	}
	return ProfileApplicationIdentity{
		ID: normalized.ID, CheckoutID: normalized.CheckoutID, RuntimeID: normalized.RuntimeID,
		TargetID: normalized.TargetID, ProjectID: normalized.ProjectID, Environment: normalized.Environment,
		ProfileName: normalized.ProfileName, GraphDigest: normalized.GraphDigest, ProfileDigest: normalized.ProfileDigest,
		EligibleConnections: cloneRequiredConnections(normalized.RequiredConnections),
		ExpectedConnections: cloneConnections(normalized.ExpectedConnections),
		RetiredConnections:  cloneConnections(normalized.RetiredConnections),
	}
}

func (record ProfileApplicationRecord) scope() ProfileApplicationScope {
	return ProfileApplicationScope{CheckoutID: record.CheckoutID, RuntimeID: record.RuntimeID, ProjectID: record.ProjectID, Environment: record.Environment}
}

func normalizeProfileApplication(record ProfileApplicationRecord, allowInitialRevision bool) (ProfileApplicationRecord, error) {
	if !validApplicationToken(string(record.ID)) || !validApplicationToken(record.CheckoutID) || !validApplicationToken(record.RuntimeID) || !validApplicationToken(record.ProfileName) {
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	if _, err := ParseTargetID(record.TargetID.String()); err != nil || projectgraph.ValidateServingScope(record.ProjectID, record.Environment) != nil {
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	if !validDigest(record.SourceDigest) || !validDigest(record.GraphDigest) || !validDigest(record.ProfileDigest) {
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	if (record.LastCompletedApplicationID == "") != record.LastCompletedAt.IsZero() {
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	if record.LastCompletedApplicationID != "" && (!validApplicationToken(record.LastCompletedApplicationID.String()) || record.LastCompletedAt != record.LastCompletedAt.UTC()) {
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	var err error
	record.RequiredConnections, err = normalizeRequiredConnections(record.RequiredConnections)
	if err != nil {
		return ProfileApplicationRecord{}, err
	}
	record.ExpectedConnections, err = normalizeExpectedConnections(record.ExpectedConnections)
	if err != nil {
		return ProfileApplicationRecord{}, err
	}
	record.RetiredConnections, err = normalizeRetiredConnections(record.RetiredConnections)
	if err != nil {
		return ProfileApplicationRecord{}, err
	}
	record.AppliedConnections, err = normalizeAppliedConnections(record.AppliedConnections)
	if err != nil {
		return ProfileApplicationRecord{}, err
	}
	for _, connections := range [][]ProfileApplicationConnection{record.ExpectedConnections, record.RetiredConnections, record.AppliedConnections} {
		for _, connection := range connections {
			if validateProfileApplicationConnectionConfiguration(connection, record.ProjectID, record.Environment) != nil {
				return ProfileApplicationRecord{}, ErrInvalidProfileApplication
			}
		}
	}
	switch record.Status {
	case ProfileApplicationApplying, ProfileApplicationIncomplete, ProfileApplicationApplied:
	default:
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	if !applicationExpectedEvidenceIsComplete(record.RequiredConnections, record.ExpectedConnections) || !applicationEvidenceIsSubset(record.ExpectedConnections, record.AppliedConnections) {
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	if record.Status == ProfileApplicationApplied && !applicationEvidenceIsComplete(record.ExpectedConnections, record.AppliedConnections) {
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	if allowInitialRevision && record.Revision == 0 {
		record.Revision = 1
	}
	if record.Revision < 1 || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) || record.CreatedAt != record.CreatedAt.UTC() || record.UpdatedAt != record.UpdatedAt.UTC() {
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	return record, nil
}

func validApplicationToken(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) || unicode.IsSpace(char) {
			return false
		}
	}
	return true
}

func validDigest(value string) bool { return platformdigest.ValidateSHA256Identity(value) == nil }

func normalizeExpectedConnections(values []ProfileApplicationConnection) ([]ProfileApplicationConnection, error) {
	return normalizeConnections(values, true, false)
}

func normalizeAppliedConnections(values []ProfileApplicationConnection) ([]ProfileApplicationConnection, error) {
	return normalizeConnections(values, false, false)
}

func normalizeRetiredConnections(values []ProfileApplicationConnection) ([]ProfileApplicationConnection, error) {
	return normalizeConnections(values, false, true)
}

func normalizeConnections(values []ProfileApplicationConnection, allowZeroRevision, allowEmptyProviderVersion bool) ([]ProfileApplicationConnection, error) {
	result := append([]ProfileApplicationConnection(nil), values...)
	for index := range result {
		connection := &result[index]
		if len(connection.Endpoint.Options) == 0 {
			connection.Endpoint.Options = nil
		}
		if _, err := ParseBindingID(connection.BindingID.String()); err != nil || connection.ConnectionID.Validate() != nil || !validApplicationToken(connection.ConnectorKind) || connection.BindingRevision < 0 || (!allowZeroRevision && connection.BindingRevision < 1) || (!allowEmptyProviderVersion && !validApplicationToken(connection.ProviderVersion)) || (allowEmptyProviderVersion && connection.ProviderVersion != "" && !validApplicationToken(connection.ProviderVersion)) {
			return nil, ErrInvalidProfileApplication
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ConnectionID < result[j].ConnectionID })
	for index := 1; index < len(result); index++ {
		if result[index-1].ConnectionID == result[index].ConnectionID {
			return nil, ErrInvalidProfileApplication
		}
	}
	return result, nil
}

func normalizeRequiredConnections(values []ProfileApplicationRequiredConnection) ([]ProfileApplicationRequiredConnection, error) {
	result := append([]ProfileApplicationRequiredConnection(nil), values...)
	for index := range result {
		connection := &result[index]
		if connection.ConnectionID.Validate() != nil || !validApplicationToken(connection.ConnectorKind) {
			return nil, ErrInvalidProfileApplication
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ConnectionID < result[j].ConnectionID })
	for index := 1; index < len(result); index++ {
		if result[index-1].ConnectionID == result[index].ConnectionID {
			return nil, ErrInvalidProfileApplication
		}
	}
	return result, nil
}

func cloneConnections(values []ProfileApplicationConnection) []ProfileApplicationConnection {
	result := append([]ProfileApplicationConnection(nil), values...)
	for index := range result {
		result[index].Endpoint = cloneEndpoint(result[index].Endpoint)
	}
	return result
}

func validateProfileApplicationConnectionConfiguration(connection ProfileApplicationConnection, projectID projectgraph.ResourceID, environment string) error {
	if err := validateEndpoint(connection.Endpoint); err != nil {
		return err
	}
	switch connection.AuthenticationMode {
	case AuthenticationExternalBundle:
		if !connection.CredentialReference.valid() || connection.CredentialReference.ProjectID != projectID || connection.CredentialReference.Environment != environment {
			return ErrInvalidProfileApplication
		}
	case AuthenticationNone:
		if !connection.CredentialReference.empty() {
			return ErrInvalidProfileApplication
		}
	default:
		return ErrInvalidProfileApplication
	}
	return nil
}

func sameProfileApplicationConnection(left, right ProfileApplicationConnection, compareRevision bool) bool {
	return left.BindingID == right.BindingID && left.ConnectionID == right.ConnectionID && left.ConnectorKind == right.ConnectorKind && left.AuthenticationMode == right.AuthenticationMode && left.CredentialReference == right.CredentialReference && left.ProviderVersion == right.ProviderVersion && (!compareRevision || left.BindingRevision == right.BindingRevision) && reflect.DeepEqual(left.Endpoint, right.Endpoint)
}

func cloneRequiredConnections(values []ProfileApplicationRequiredConnection) []ProfileApplicationRequiredConnection {
	return append([]ProfileApplicationRequiredConnection(nil), values...)
}

func applicationExpectedEvidenceIsComplete(required []ProfileApplicationRequiredConnection, expected []ProfileApplicationConnection) bool {
	if len(required) != len(expected) {
		return false
	}
	byID := make(map[projectgraph.ResourceID]string, len(required))
	for _, connection := range required {
		byID[connection.ConnectionID] = connection.ConnectorKind
	}
	for _, connection := range expected {
		if byID[connection.ConnectionID] != connection.ConnectorKind {
			return false
		}
	}
	return true
}

func applicationEvidenceIsSubset(expected, applied []ProfileApplicationConnection) bool {
	if len(applied) > len(expected) {
		return false
	}
	byID := make(map[projectgraph.ResourceID]ProfileApplicationConnection, len(expected))
	for _, connection := range expected {
		byID[connection.ConnectionID] = connection
	}
	for _, connection := range applied {
		want, ok := byID[connection.ConnectionID]
		if !ok || !sameProfileApplicationConnection(want, connection, false) || connection.BindingRevision < max(1, want.BindingRevision) {
			return false
		}
	}
	return true
}

func applicationEvidenceIsComplete(expected, applied []ProfileApplicationConnection) bool {
	return len(expected) == len(applied) && applicationEvidenceIsSubset(expected, applied)
}

func sameApplicationIntent(left, right ProfileApplicationRecord) bool {
	left, lerr := normalizeProfileApplication(left, false)
	right, rerr := normalizeProfileApplication(right, false)
	if lerr != nil || rerr != nil {
		return false
	}
	if left.ID != right.ID || left.CheckoutID != right.CheckoutID || left.RuntimeID != right.RuntimeID || left.TargetID != right.TargetID || left.ProjectID != right.ProjectID || left.Environment != right.Environment || left.ProfileName != right.ProfileName || left.SourceDigest != right.SourceDigest || left.GraphDigest != right.GraphDigest || left.ProfileDigest != right.ProfileDigest || len(left.RequiredConnections) != len(right.RequiredConnections) || len(left.ExpectedConnections) != len(right.ExpectedConnections) || len(left.RetiredConnections) != len(right.RetiredConnections) {
		return false
	}
	for index := range left.ExpectedConnections {
		if !sameProfileApplicationConnection(left.ExpectedConnections[index], right.ExpectedConnections[index], true) {
			return false
		}
	}
	for index := range left.RetiredConnections {
		if !sameProfileApplicationConnection(left.RetiredConnections[index], right.RetiredConnections[index], true) {
			return false
		}
	}
	for index := range left.RequiredConnections {
		if left.RequiredConnections[index] != right.RequiredConnections[index] {
			return false
		}
	}
	return true
}

func sameApplicationEvidence(left, right ProfileApplicationRecord) bool {
	if len(left.AppliedConnections) != len(right.AppliedConnections) {
		return false
	}
	for index := range left.AppliedConnections {
		if !sameProfileApplicationConnection(left.AppliedConnections[index], right.AppliedConnections[index], true) {
			return false
		}
	}
	return true
}

func sameApplicationPayload(left, right ProfileApplicationRecord) bool {
	return sameApplicationIntent(left, right) && sameApplicationEvidence(left, right) && left.Status == right.Status
}

func (request ProfileApplicationAdmissionRequest) normalized(observedSourceDigest string) (ProfileApplicationRecord, error) {
	required := make([]ProfileApplicationRequiredConnection, len(request.EligibleConnections))
	for index, connection := range request.EligibleConnections {
		required[index] = ProfileApplicationRequiredConnection{ConnectionID: connection.ConnectionID, ConnectorKind: connection.ConnectorKind}
	}
	return NewProfileApplication(ProfileApplicationRecord{
		ID: "admission-request", CheckoutID: request.CheckoutID, RuntimeID: request.RuntimeID,
		TargetID: request.TargetID, ProjectID: request.ProjectID, Environment: request.Environment,
		ProfileName: request.ProfileName, SourceDigest: observedSourceDigest, GraphDigest: request.GraphDigest, ProfileDigest: request.ProfileDigest,
		RequiredConnections: required, ExpectedConnections: cloneConnections(request.EligibleConnections), AppliedConnections: cloneConnections(request.EligibleConnections), Status: ProfileApplicationApplied,
		Revision: 1, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
	})
}

// ProfileApplicationAdmissionChecker is the local runtime gate. It resolves
// one exact scoped record and admits only an applied record whose complete
// intent and eligible evidence match the request.
type ProfileApplicationAdmissionChecker struct{ store ProfileApplicationStore }

func NewProfileApplicationAdmissionChecker(store ProfileApplicationStore) (*ProfileApplicationAdmissionChecker, error) {
	if store == nil {
		return nil, ErrInvalidProfileApplication
	}
	return &ProfileApplicationAdmissionChecker{store: store}, nil
}

func (checker *ProfileApplicationAdmissionChecker) Check(ctx context.Context, request ProfileApplicationAdmissionRequest) error {
	_, err := checker.Record(ctx, request)
	return err
}

func (checker *ProfileApplicationAdmissionChecker) Admit(ctx context.Context, request ProfileApplicationAdmissionRequest) error {
	return checker.Check(ctx, request)
}

func (checker *ProfileApplicationAdmissionChecker) Record(ctx context.Context, request ProfileApplicationAdmissionRequest) (ProfileApplicationRecord, error) {
	if checker == nil || checker.store == nil || ctx == nil || ctx.Err() != nil {
		return ProfileApplicationRecord{}, ErrProfileApplicationNotAdmitted
	}
	actual, err := checker.store.Application(ctx, ProfileApplicationScope{CheckoutID: request.CheckoutID, RuntimeID: request.RuntimeID, ProjectID: request.ProjectID, Environment: request.Environment}, request.TargetID)
	if err != nil {
		return ProfileApplicationRecord{}, ErrProfileApplicationNotAdmitted
	}
	want, err := request.normalized(actual.SourceDigest)
	if err != nil {
		return ProfileApplicationRecord{}, ErrProfileApplicationNotAdmitted
	}
	if actual.Status != ProfileApplicationApplied || !sameAdmissionIntent(actual, want) || !sameAdmissionEvidence(actual.AppliedConnections, want.AppliedConnections) {
		return ProfileApplicationRecord{}, ErrProfileApplicationNotAdmitted
	}
	return actual, nil
}

func sameAdmissionEvidence(retained, current []ProfileApplicationConnection) bool {
	if len(retained) != len(current) {
		return false
	}
	for index := range retained {
		if !sameProfileApplicationConnection(retained[index], current[index], false) || current[index].BindingRevision < retained[index].BindingRevision {
			return false
		}
	}
	return true
}

func sameAdmissionIntent(actual, want ProfileApplicationRecord) bool {
	actual, actualErr := normalizeProfileApplication(actual, false)
	want, wantErr := normalizeProfileApplication(want, false)
	if actualErr != nil || wantErr != nil || actual.CheckoutID != want.CheckoutID || actual.RuntimeID != want.RuntimeID || actual.TargetID != want.TargetID || actual.ProjectID != want.ProjectID || actual.Environment != want.Environment || actual.ProfileName != want.ProfileName || actual.GraphDigest != want.GraphDigest || actual.ProfileDigest != want.ProfileDigest || len(actual.RequiredConnections) != len(want.RequiredConnections) {
		return false
	}
	for index := range actual.RequiredConnections {
		if actual.RequiredConnections[index] != want.RequiredConnections[index] {
			return false
		}
	}
	return true
}

func validateScope(scope ProfileApplicationScope) error {
	if !validApplicationToken(scope.CheckoutID) || !validApplicationToken(scope.RuntimeID) || projectgraph.ValidateServingScope(scope.ProjectID, scope.Environment) != nil {
		return ErrInvalidProfileApplication
	}
	return nil
}

func scopeKey(scope ProfileApplicationScope, targetID TargetID) string {
	return strings.Join([]string{scope.CheckoutID, scope.RuntimeID, targetID.String(), scope.ProjectID.String(), scope.Environment}, "\x00")
}

func cloneRecord(record ProfileApplicationRecord) ProfileApplicationRecord {
	record.RequiredConnections = cloneRequiredConnections(record.RequiredConnections)
	record.ExpectedConnections = cloneConnections(record.ExpectedConnections)
	record.RetiredConnections = cloneConnections(record.RetiredConnections)
	record.AppliedConnections = cloneConnections(record.AppliedConnections)
	return record
}
