package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
)

const deploymentOperationDocumentVersion = 1

const (
	maxDeploymentOperationTextBytes    = 128 << 10
	maxDeploymentOperationArtifacts    = 10_000
	maxDeploymentOperationArtifactSize = 8 << 20
	maxDeploymentOperationSourceBytes  = 64 << 20
	// Source bytes are base64-encoded in JSON. Leave bounded room for that
	// expansion plus descriptor/evidence metadata, but reject unbounded CI
	// artifacts before the JSON decoder allocates their contents.
	maxDeploymentOperationDocumentBytes = 96 << 20
)

var (
	ErrDeploymentOperationNotFound       = errors.New("deployment operation not found")
	ErrDeploymentOperationTargetMismatch = errors.New("deployment operation target identity mismatch")
)

// DeploymentOperationOutcome is the public, conservative outcome of a
// deployment operation. Only active means that target activation committed.
type DeploymentOperationOutcome string

const (
	DeploymentOperationUnknown         DeploymentOperationOutcome = "unknown"
	DeploymentOperationPendingApproval DeploymentOperationOutcome = "pending_approval"
	DeploymentOperationActive          DeploymentOperationOutcome = "active"
	DeploymentOperationFailure         DeploymentOperationOutcome = "failure"
	DeploymentOperationIndeterminate   DeploymentOperationOutcome = "indeterminate"
)

// DeploymentOperationDescriptor is a credential-free, versioned handoff for
// one exact deployment attempt. SourceRoot is informational and never used to
// reconstruct a resumed operation; SourceSnapshotRef and its digests identify
// the retained target-side source snapshot.
type DeploymentOperationDescriptor struct {
	Handle                    string                     `json:"handle"`
	CreatedAt                 string                     `json:"createdAt"`
	TargetOrigin              string                     `json:"targetOrigin"`
	TargetSelector            string                     `json:"targetSelector,omitempty"`
	TargetID                  string                     `json:"targetId,omitempty"`
	ProjectID                 string                     `json:"projectId"`
	Environment               string                     `json:"environment"`
	SourceRoot                string                     `json:"sourceRoot,omitempty"`
	SourceSnapshotRef         string                     `json:"sourceSnapshotRef,omitempty"`
	SourceRevision            string                     `json:"sourceRevision,omitempty"`
	SourceRepository          string                     `json:"sourceRepository,omitempty"`
	SourceRef                 string                     `json:"sourceRef,omitempty"`
	SourceChangeID            string                     `json:"sourceChangeId,omitempty"`
	SourceDigest              string                     `json:"sourceDigest,omitempty"`
	SourceAttestationDigest   string                     `json:"sourceAttestationDigest,omitempty"`
	ProvenanceDigest          string                     `json:"provenanceDigest,omitempty"`
	SourceSnapshotProjectID   string                     `json:"sourceSnapshotProjectId,omitempty"`
	SourceSnapshotGraphDigest string                     `json:"sourceSnapshotGraphDigest,omitempty"`
	SourceArtifacts           []DeploymentSourceArtifact `json:"sourceArtifacts,omitempty"`
	PlanID                    string                     `json:"planId,omitempty"`
	PlanDigest                string                     `json:"planDigest,omitempty"`
	PlanStatus                string                     `json:"planStatus,omitempty"`
	PlanExpiresAt             string                     `json:"planExpiresAt,omitempty"`
	PlanEvidence              DeliveryPlanEvidenceResult `json:"planEvidence,omitempty"`
	GovernanceDigest          string                     `json:"governanceDigest,omitempty"`
	BaseGenerationID          string                     `json:"baseGenerationId,omitempty"`
	BaseTargetRevision        int64                      `json:"baseTargetRevision,omitempty"`
	ExecutionDigest           string                     `json:"executionDigest,omitempty"`
	EvidenceDigest            string                     `json:"evidenceDigest,omitempty"`
	BuildID                   string                     `json:"buildId,omitempty"`
	BuildRevision             int64                      `json:"buildRevision,omitempty"`
	CandidateID               string                     `json:"candidateId,omitempty"`
	CandidateRevision         int64                      `json:"candidateRevision,omitempty"`
	SealID                    string                     `json:"sealId,omitempty"`
	PublicationID             string                     `json:"publicationId,omitempty"`
	GenerationID              string                     `json:"generationId,omitempty"`
	PublicationTargetRevision int64                      `json:"publicationTargetRevision,omitempty"`
	PublicationStatus         string                     `json:"publicationStatus,omitempty"`
	PlanIdempotencyKey        string                     `json:"planIdempotencyKey"`
	BuildIdempotencyKey       string                     `json:"buildIdempotencyKey"`
	PublicationIdempotencyKey string                     `json:"publicationIdempotencyKey"`
	Outcome                   DeploymentOperationOutcome `json:"outcome"`
	FailureCode               string                     `json:"failureCode,omitempty"`
	FailureDetail             string                     `json:"failureDetail,omitempty"`
	StatusURL                 string                     `json:"statusUrl,omitempty"`
}

// DeploymentSourceArtifact is the portable, credential-free retained source
// capture used to retry source synchronization after an acknowledgement loss.
type DeploymentSourceArtifact struct {
	Path      string `json:"path"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"sizeBytes"`
	Content   []byte `json:"content"`
}

type deploymentOperationDocument struct {
	Version    int                                      `json:"version"`
	Operations map[string]DeploymentOperationDescriptor `json:"operations"`
}

// DeploymentOperationStore atomically persists operation descriptors. It is
// intentionally independent from credentials and target profiles so exported
// descriptors can safely be retained as CI artifacts.
type DeploymentOperationStore struct {
	path string
	mu   sync.Mutex
}

func NewDeploymentOperationStore(path string) *DeploymentOperationStore {
	return &DeploymentOperationStore{path: strings.TrimSpace(path)}
}

// NewDeploymentOperation creates a fresh descriptor and stable mutation keys.
// A caller may supply a handle for automation; an empty handle gets a unique,
// human-readable release handle.
func NewDeploymentOperation(handle, targetOrigin, targetSelector, projectID, environment, sourceRoot, sourceSnapshotRef string) (DeploymentOperationDescriptor, error) {
	if strings.TrimSpace(handle) == "" {
		var entropy [6]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return DeploymentOperationDescriptor{}, fmt.Errorf("generate deployment operation handle: %w", err)
		}
		handle = fmt.Sprintf("release-%d-%s", time.Now().UTC().Unix(), hex.EncodeToString(entropy[:]))
	}
	handle = strings.TrimSpace(handle)
	if err := validateDeploymentOperationHandle(handle); err != nil {
		return DeploymentOperationDescriptor{}, err
	}
	origin, err := canonicalOperationOrigin(targetOrigin)
	if err != nil {
		return DeploymentOperationDescriptor{}, err
	}
	projectID, environment = strings.TrimSpace(projectID), strings.TrimSpace(environment)
	if projectID == "" || environment == "" {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation requires project and environment")
	}
	root := strings.TrimSpace(sourceRoot)
	if root != "" {
		root, err = filepath.Abs(root)
		if err != nil {
			return DeploymentOperationDescriptor{}, fmt.Errorf("resolve deployment source root: %w", err)
		}
		root = filepath.Clean(root)
	}
	created := time.Now().UTC().Format(time.RFC3339Nano)
	d := DeploymentOperationDescriptor{
		Handle: handle, CreatedAt: created, TargetOrigin: origin,
		TargetSelector: strings.TrimSpace(targetSelector), ProjectID: projectID,
		Environment: environment, SourceRoot: root, SourceSnapshotRef: strings.TrimSpace(sourceSnapshotRef),
		PlanIdempotencyKey:        deploymentOperationIdempotencyKey(handle, "plan"),
		BuildIdempotencyKey:       deploymentOperationIdempotencyKey(handle, "build"),
		PublicationIdempotencyKey: deploymentOperationIdempotencyKey(handle, "publish"),
		Outcome:                   DeploymentOperationUnknown,
	}
	return d, nil
}

func deploymentOperationIdempotencyKey(handle, phase string) string {
	return "deployment-operation-" + strings.TrimSpace(handle) + "-" + strings.TrimSpace(phase)
}

func (store *DeploymentOperationStore) Create(descriptor DeploymentOperationDescriptor) error {
	if store == nil {
		return fmt.Errorf("deployment operation store is required")
	}
	normalized, err := normalizeDeploymentOperation(descriptor)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	lock, err := store.acquireMutationLock()
	if err != nil {
		return err
	}
	defer lock.Release()
	document, err := store.load()
	if err != nil {
		return err
	}
	if _, exists := document.Operations[normalized.Handle]; exists {
		return fmt.Errorf("deployment operation handle %q already exists", normalized.Handle)
	}
	document.Operations[normalized.Handle] = normalized
	return store.save(document)
}

// Save merges a descriptor under the cross-process lock. Immutable identity
// cannot be changed, terminal outcomes cannot be downgraded, and fields known
// by a racing writer are retained rather than overwritten with stale blanks.
func (store *DeploymentOperationStore) Save(descriptor DeploymentOperationDescriptor) error {
	if store == nil {
		return fmt.Errorf("deployment operation store is required")
	}
	normalized, err := normalizeDeploymentOperation(descriptor)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	lock, err := store.acquireMutationLock()
	if err != nil {
		return err
	}
	defer lock.Release()
	document, err := store.load()
	if err != nil {
		return err
	}
	current, exists := document.Operations[normalized.Handle]
	if !exists {
		document.Operations[normalized.Handle] = normalized
		return store.save(document)
	}
	if err := sameDeploymentOperationIdentity(current, normalized); err != nil {
		return err
	}
	merged, err := mergeDeploymentOperation(current, normalized)
	if err != nil {
		return err
	}
	document.Operations[normalized.Handle] = merged
	return store.save(document)
}

func (store *DeploymentOperationStore) Load(handle string) (DeploymentOperationDescriptor, error) {
	if store == nil {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation store is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	document, err := store.load()
	if err != nil {
		return DeploymentOperationDescriptor{}, err
	}
	descriptor, ok := document.Operations[strings.TrimSpace(handle)]
	if !ok {
		return DeploymentOperationDescriptor{}, ErrDeploymentOperationNotFound
	}
	normalized, err := normalizeDeploymentOperation(descriptor)
	if err != nil {
		return DeploymentOperationDescriptor{}, fmt.Errorf("stored deployment operation is invalid: %w", err)
	}
	return normalized, nil
}

// List returns deterministic handle order. It never chooses one operation.
// Empty filters are wildcards, except targetOrigin when supplied, which is
// canonicalized to prevent aliases from selecting another target.
func (store *DeploymentOperationStore) List(targetOrigin, projectID, environment string) ([]DeploymentOperationDescriptor, error) {
	if store == nil {
		return nil, fmt.Errorf("deployment operation store is required")
	}
	origin := strings.TrimSpace(targetOrigin)
	var err error
	if origin != "" {
		origin, err = canonicalOperationOrigin(origin)
		if err != nil {
			return nil, err
		}
	}
	projectID, environment = strings.TrimSpace(projectID), strings.TrimSpace(environment)
	store.mu.Lock()
	defer store.mu.Unlock()
	document, err := store.load()
	if err != nil {
		return nil, err
	}
	items := make([]DeploymentOperationDescriptor, 0)
	for _, descriptor := range document.Operations {
		normalized, normalizeErr := normalizeDeploymentOperation(descriptor)
		if normalizeErr != nil {
			return nil, fmt.Errorf("stored deployment operation %q is invalid: %w", descriptor.Handle, normalizeErr)
		}
		if origin != "" && normalized.TargetOrigin != origin || projectID != "" && normalized.ProjectID != projectID || environment != "" && normalized.Environment != environment {
			continue
		}
		items = append(items, normalized)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Handle < items[j].Handle })
	return items, nil
}

func (store *DeploymentOperationStore) Select(handle, targetOrigin, projectID, environment string) (DeploymentOperationDescriptor, error) {
	return store.SelectExact(handle, targetOrigin, "", projectID, environment)
}

// SelectExact validates every known canonical destination component before a
// resume. An empty targetID preserves compatibility with callers that can only
// resolve origin/project/environment during their preflight.
func (store *DeploymentOperationStore) SelectExact(handle, targetOrigin, targetID, projectID, environment string) (DeploymentOperationDescriptor, error) {
	descriptor, err := store.Load(handle)
	if err != nil {
		return DeploymentOperationDescriptor{}, err
	}
	origin, err := canonicalOperationOrigin(targetOrigin)
	if err != nil {
		return DeploymentOperationDescriptor{}, err
	}
	if descriptor.TargetOrigin != origin || (strings.TrimSpace(targetID) != "" && descriptor.TargetID != strings.TrimSpace(targetID)) || (strings.TrimSpace(projectID) != "" && descriptor.ProjectID != strings.TrimSpace(projectID)) || (strings.TrimSpace(environment) != "" && descriptor.Environment != strings.TrimSpace(environment)) {
		return DeploymentOperationDescriptor{}, fmt.Errorf("%w: handle %q is bound to %s/%s/%s", ErrDeploymentOperationTargetMismatch, descriptor.Handle, descriptor.TargetOrigin, descriptor.ProjectID, descriptor.Environment)
	}
	return descriptor, nil
}

// Export writes exactly one descriptor and therefore never exports credentials.
func (store *DeploymentOperationStore) Export(handle string, out io.Writer) error {
	descriptor, err := store.Load(handle)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		Version    int                                      `json:"version"`
		Operations map[string]DeploymentOperationDescriptor `json:"operations"`
	}{deploymentOperationDocumentVersion, map[string]DeploymentOperationDescriptor{descriptor.Handle: descriptor}})
}

// Import verifies a descriptor's schema and exact target association before
// persisting it. Existing handles may only be imported when their identity is
// identical, making artifact retries safe.
func (store *DeploymentOperationStore) Import(in io.Reader, targetOrigin, projectID, environment string) (DeploymentOperationDescriptor, error) {
	if store == nil {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation store is required")
	}
	document, err := decodeDeploymentOperationDocument(in)
	if err != nil {
		return DeploymentOperationDescriptor{}, fmt.Errorf("decode deployment operation artifact: %w", err)
	}
	if document.Version != deploymentOperationDocumentVersion || len(document.Operations) != 1 {
		return DeploymentOperationDescriptor{}, fmt.Errorf("unsupported deployment operation artifact")
	}
	for handle, descriptor := range document.Operations {
		if strings.TrimSpace(handle) != descriptor.Handle {
			return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation artifact handle %q does not match descriptor handle %q", handle, descriptor.Handle)
		}
		if err := store.ensureNoExistingOrCreate(descriptor, targetOrigin, projectID, environment); err != nil {
			return DeploymentOperationDescriptor{}, err
		}
		normalized, _ := normalizeDeploymentOperation(descriptor)
		return normalized, nil
	}
	return DeploymentOperationDescriptor{}, ErrDeploymentOperationNotFound
}

func (store *DeploymentOperationStore) ensureNoExistingOrCreate(descriptor DeploymentOperationDescriptor, targetOrigin, projectID, environment string) error {
	normalized, err := normalizeDeploymentOperation(descriptor)
	if err != nil {
		return err
	}
	origin, err := canonicalOperationOrigin(targetOrigin)
	if err != nil {
		return err
	}
	if normalized.TargetOrigin != origin || normalized.ProjectID != strings.TrimSpace(projectID) || normalized.Environment != strings.TrimSpace(environment) {
		return ErrDeploymentOperationTargetMismatch
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	lock, err := store.acquireMutationLock()
	if err != nil {
		return err
	}
	defer lock.Release()
	document, err := store.load()
	if err != nil {
		return err
	}
	if existing, ok := document.Operations[normalized.Handle]; ok {
		if err := sameDeploymentOperationIdentity(existing, normalized); err != nil {
			return err
		}
		return nil
	}
	document.Operations[normalized.Handle] = normalized
	return store.save(document)
}

func (store *DeploymentOperationStore) load() (deploymentOperationDocument, error) {
	document := deploymentOperationDocument{Version: deploymentOperationDocumentVersion, Operations: map[string]DeploymentOperationDescriptor{}}
	if strings.TrimSpace(store.path) == "" {
		return deploymentOperationDocument{}, fmt.Errorf("deployment operation path is required")
	}
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return document, nil
	}
	if err != nil {
		return deploymentOperationDocument{}, fmt.Errorf("read deployment operations: %w", err)
	}
	defer file.Close()
	loaded, err := decodeDeploymentOperationDocument(file)
	if err != nil {
		return deploymentOperationDocument{}, fmt.Errorf("decode deployment operations: %w", err)
	}
	return loaded, nil
}

func decodeDeploymentOperationDocument(in io.Reader) (deploymentOperationDocument, error) {
	return decodeDeploymentOperationDocumentWithLimit(in, maxDeploymentOperationDocumentBytes)
}

func decodeDeploymentOperationDocumentWithLimit(in io.Reader, limit int64) (deploymentOperationDocument, error) {
	content, err := io.ReadAll(io.LimitReader(in, limit+1))
	if err != nil {
		return deploymentOperationDocument{}, err
	}
	if int64(len(content)) > limit {
		return deploymentOperationDocument{}, fmt.Errorf("deployment operation document exceeds %d bytes", limit)
	}
	var document deploymentOperationDocument
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return deploymentOperationDocument{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return deploymentOperationDocument{}, fmt.Errorf("trailing JSON content")
	}
	if document.Version != deploymentOperationDocumentVersion {
		return deploymentOperationDocument{}, fmt.Errorf("unsupported deployment operation version %d", document.Version)
	}
	if document.Operations == nil {
		document.Operations = map[string]DeploymentOperationDescriptor{}
	}
	return document, nil
}

func (store *DeploymentOperationStore) save(document deploymentOperationDocument) error {
	content, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode deployment operations: %w", err)
	}
	if err := securefs.WritePrivateFileAtomic(store.path, content); err != nil {
		return fmt.Errorf("write deployment operations: %w", err)
	}
	return nil
}

func (store *DeploymentOperationStore) acquireMutationLock() (*instancelock.Lock, error) {
	if strings.TrimSpace(store.path) == "" {
		return nil, fmt.Errorf("deployment operation path is required")
	}
	return instancelock.AcquireNamed(filepath.Dir(store.path), "."+filepath.Base(store.path)+".lock")
}

// DeploymentOperationResult converts the retained descriptor into the public
// result envelope used by text and JSON CLI output.
func (descriptor DeploymentOperationDescriptor) DeploymentOperationResult() DeploymentOperationResult {
	return DeploymentOperationResult{
		SchemaVersion: 1, Handle: descriptor.Handle, CreatedAt: descriptor.CreatedAt,
		TargetOrigin: descriptor.TargetOrigin, TargetID: descriptor.TargetID,
		ProjectID: descriptor.ProjectID, Environment: descriptor.Environment,
		SourceRevision: descriptor.SourceRevision, SourceDigest: descriptor.SourceDigest,
		SourceRepository: descriptor.SourceRepository, SourceRef: descriptor.SourceRef, SourceChangeID: descriptor.SourceChangeID,
		SourceAttestationDigest: descriptor.SourceAttestationDigest, ProvenanceDigest: descriptor.ProvenanceDigest, PlanID: descriptor.PlanID,
		PlanDigest: descriptor.PlanDigest, PlanStatus: descriptor.PlanStatus, PlanExpiresAt: descriptor.PlanExpiresAt, PlanEvidence: descriptor.PlanEvidence,
		GovernanceDigest: descriptor.GovernanceDigest,
		BaseGenerationID: descriptor.BaseGenerationID, BaseTargetRevision: descriptor.BaseTargetRevision, BuildID: descriptor.BuildID,
		ExecutionDigest: descriptor.ExecutionDigest, EvidenceDigest: descriptor.EvidenceDigest,
		BuildRevision: descriptor.BuildRevision, CandidateID: descriptor.CandidateID, CandidateRevision: descriptor.CandidateRevision, SealID: descriptor.SealID, PublicationID: descriptor.PublicationID,
		GenerationID: descriptor.GenerationID, Outcome: descriptor.Outcome,
		PublicationTargetRevision: descriptor.PublicationTargetRevision,
		PublicationStatus:         descriptor.PublicationStatus, FailureCode: descriptor.FailureCode,
		FailureDetail: descriptor.FailureDetail, StatusURL: descriptor.StatusURL,
	}
}

func WriteDeploymentOperationResult(out io.Writer, format string, result DeploymentOperationResult) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	fmt.Fprintf(out, "operation %s target %s environment %s outcome %s\n", result.Handle, result.TargetID, result.Environment, result.Outcome)
	if result.ProjectID != "" {
		fmt.Fprintf(out, "project %s\n", result.ProjectID)
	}
	if result.SourceDigest != "" {
		fmt.Fprintf(out, "source %s\n", result.SourceDigest)
	}
	if result.PlanID != "" {
		fmt.Fprintf(out, "plan %s digest %s\n", result.PlanID, result.PlanDigest)
	}
	if result.BuildID != "" {
		fmt.Fprintf(out, "build %s revision %d candidate %s candidate-revision %d seal %s\n", result.BuildID, result.BuildRevision, result.CandidateID, result.CandidateRevision, result.SealID)
	}
	if result.PublicationID != "" {
		fmt.Fprintf(out, "publication %s generation %s status %s\n", result.PublicationID, result.GenerationID, result.PublicationStatus)
	}
	if result.FailureDetail != "" {
		fmt.Fprintf(out, "failure %s: %s\n", result.FailureCode, result.FailureDetail)
	}
	if result.NextAction != "" {
		fmt.Fprintf(out, "next-action %s\n", result.NextAction)
	}
	return nil
}

func validateDeploymentOperationHandle(handle string) error {
	if handle == "" || len(handle) > 128 || strings.ContainsAny(handle, "/\\\r\n\t") {
		return fmt.Errorf("deployment operation handle must be 1-128 characters without path separators")
	}
	return nil
}

func canonicalOperationOrigin(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && operationLoopbackHost(parsed.Hostname()))) {
		return "", fmt.Errorf("deployment operation target origin must be an absolute URL without credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("deployment operation target origin must not contain a path, query, or fragment")
	}
	parsed.Path = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func operationLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func sameDeploymentOperationIdentity(a, b DeploymentOperationDescriptor) error {
	for _, identity := range []struct{ name, left, right string }{
		{"createdAt", a.CreatedAt, b.CreatedAt}, {"targetOrigin", a.TargetOrigin, b.TargetOrigin},
		{"targetSelector", a.TargetSelector, b.TargetSelector}, {"targetId", a.TargetID, b.TargetID},
		{"projectId", a.ProjectID, b.ProjectID}, {"environment", a.Environment, b.Environment},
		{"sourceSnapshotRef", a.SourceSnapshotRef, b.SourceSnapshotRef}, {"sourceRevision", a.SourceRevision, b.SourceRevision}, {"sourceRepository", a.SourceRepository, b.SourceRepository}, {"sourceRef", a.SourceRef, b.SourceRef}, {"sourceChangeId", a.SourceChangeID, b.SourceChangeID}, {"sourceDigest", a.SourceDigest, b.SourceDigest},
		{"sourceSnapshotProjectId", a.SourceSnapshotProjectID, b.SourceSnapshotProjectID}, {"sourceSnapshotGraphDigest", a.SourceSnapshotGraphDigest, b.SourceSnapshotGraphDigest},
		{"sourceAttestationDigest", a.SourceAttestationDigest, b.SourceAttestationDigest}, {"provenanceDigest", a.ProvenanceDigest, b.ProvenanceDigest},
		{"planId", a.PlanID, b.PlanID}, {"planDigest", a.PlanDigest, b.PlanDigest}, {"planExpiresAt", a.PlanExpiresAt, b.PlanExpiresAt}, {"governanceDigest", a.GovernanceDigest, b.GovernanceDigest},
		{"executionDigest", a.ExecutionDigest, b.ExecutionDigest}, {"evidenceDigest", a.EvidenceDigest, b.EvidenceDigest},
		{"buildId", a.BuildID, b.BuildID}, {"candidateId", a.CandidateID, b.CandidateID}, {"sealId", a.SealID, b.SealID},
		{"publicationId", a.PublicationID, b.PublicationID}, {"generationId", a.GenerationID, b.GenerationID},
		{"planIdempotencyKey", a.PlanIdempotencyKey, b.PlanIdempotencyKey}, {"buildIdempotencyKey", a.BuildIdempotencyKey, b.BuildIdempotencyKey},
		{"publicationIdempotencyKey", a.PublicationIdempotencyKey, b.PublicationIdempotencyKey},
	} {
		if identity.left != "" && identity.right != "" && identity.left != identity.right {
			return fmt.Errorf("deployment operation %q identity changed: %s", a.Handle, identity.name)
		}
	}
	return nil
}

func mergeDeploymentOperation(current, next DeploymentOperationDescriptor) (DeploymentOperationDescriptor, error) {
	merged := current
	fill := func(dst *string, value string) {
		if strings.TrimSpace(value) != "" {
			*dst = strings.TrimSpace(value)
		}
	}
	fill(&merged.TargetID, next.TargetID)
	fill(&merged.SourceRoot, next.SourceRoot)
	fill(&merged.SourceSnapshotRef, next.SourceSnapshotRef)
	fill(&merged.SourceRevision, next.SourceRevision)
	fill(&merged.SourceRepository, next.SourceRepository)
	fill(&merged.SourceRef, next.SourceRef)
	fill(&merged.SourceChangeID, next.SourceChangeID)
	fill(&merged.SourceDigest, next.SourceDigest)
	fill(&merged.SourceAttestationDigest, next.SourceAttestationDigest)
	fill(&merged.ProvenanceDigest, next.ProvenanceDigest)
	fill(&merged.SourceSnapshotProjectID, next.SourceSnapshotProjectID)
	fill(&merged.SourceSnapshotGraphDigest, next.SourceSnapshotGraphDigest)
	if len(next.SourceArtifacts) != 0 {
		if len(merged.SourceArtifacts) != 0 {
			if err := validatePortableSourceArtifacts(merged.SourceSnapshotProjectID, merged.SourceSnapshotGraphDigest, merged.SourceDigest, next.SourceArtifacts); err != nil {
				return DeploymentOperationDescriptor{}, err
			}
			if len(merged.SourceArtifacts) != len(next.SourceArtifacts) {
				return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation source snapshot changed")
			}
			for i := range merged.SourceArtifacts {
				if merged.SourceArtifacts[i].Path != next.SourceArtifacts[i].Path || merged.SourceArtifacts[i].Digest != next.SourceArtifacts[i].Digest || !bytes.Equal(merged.SourceArtifacts[i].Content, next.SourceArtifacts[i].Content) {
					return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation source snapshot changed")
				}
			}
		} else {
			merged.SourceArtifacts = append([]DeploymentSourceArtifact(nil), next.SourceArtifacts...)
		}
	}
	fill(&merged.PlanID, next.PlanID)
	fill(&merged.PlanDigest, next.PlanDigest)
	fill(&merged.PlanStatus, next.PlanStatus)
	fill(&merged.PlanExpiresAt, next.PlanExpiresAt)
	fill(&merged.GovernanceDigest, next.GovernanceDigest)
	if hasDeploymentPlanEvidence(next.PlanEvidence) {
		merged.PlanEvidence = next.PlanEvidence
	}
	fill(&merged.BaseGenerationID, next.BaseGenerationID)
	if next.BaseTargetRevision != 0 {
		merged.BaseTargetRevision = next.BaseTargetRevision
	}
	fill(&merged.ExecutionDigest, next.ExecutionDigest)
	fill(&merged.EvidenceDigest, next.EvidenceDigest)
	fill(&merged.BuildID, next.BuildID)
	fill(&merged.CandidateID, next.CandidateID)
	fill(&merged.SealID, next.SealID)
	fill(&merged.PublicationID, next.PublicationID)
	fill(&merged.GenerationID, next.GenerationID)
	fill(&merged.PublicationStatus, next.PublicationStatus)
	fill(&merged.FailureCode, next.FailureCode)
	fill(&merged.FailureDetail, next.FailureDetail)
	fill(&merged.StatusURL, next.StatusURL)
	if next.BuildRevision != 0 {
		merged.BuildRevision = next.BuildRevision
	}
	if next.CandidateRevision != 0 {
		merged.CandidateRevision = next.CandidateRevision
	}
	if next.PublicationTargetRevision != 0 {
		merged.PublicationTargetRevision = next.PublicationTargetRevision
	}
	// A stale writer may only leave an already terminal result alone. Evidence
	// reconciliation is allowed to advance indeterminate/pending to a newer
	// target outcome, including committed activation.
	switch {
	case current.Outcome == DeploymentOperationActive:
		merged.Outcome = current.Outcome
	case current.Outcome == DeploymentOperationFailure && next.Outcome != DeploymentOperationActive:
		merged.Outcome = current.Outcome
	case next.Outcome == DeploymentOperationUnknown && current.Outcome != DeploymentOperationUnknown:
		merged.Outcome = current.Outcome
	default:
		merged.Outcome = next.Outcome
	}
	return normalizeDeploymentOperation(merged)
}

func hasDeploymentPlanEvidence(evidence DeliveryPlanEvidenceResult) bool {
	return evidence.Digest != "" || evidence.CompatibilityBreaking || evidence.AddedCount != 0 || evidence.RemovedCount != 0 || evidence.DirectlyModifiedCount != 0 || evidence.IndirectlyAffectedCount != 0 || evidence.ReuseCount != 0 || evidence.QualificationStepCount != 0 || evidence.ImpactStatement != "" || evidence.PhysicalWorkStatement != "" || evidence.ReuseStatement != "" || evidence.RollbackClass != "" || evidence.QualificationPolicy != "" || evidence.StalePolicy.Mode != "" || evidence.StalePolicy.AllowRetainedBase || evidence.StalePolicy.Description != "" || len(evidence.PlannedInputs) != 0 || len(evidence.QualificationSteps) != 0 || len(evidence.ReuseDecisions) != 0
}

func outcomeRank(outcome DeploymentOperationOutcome) int {
	switch outcome {
	case DeploymentOperationUnknown:
		return 0
	case DeploymentOperationPendingApproval:
		return 1
	case DeploymentOperationIndeterminate:
		return 2
	case DeploymentOperationFailure:
		return 3
	case DeploymentOperationActive:
		return 4
	default:
		return -1
	}
}
