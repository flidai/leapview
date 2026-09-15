package cli

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/platform/digest"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/flidai/leapview/internal/platform/safetext"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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
	ErrDeploymentOperationAmbiguous      = errors.New("deployment operation selection is ambiguous")
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

func normalizeDeploymentOperation(descriptor DeploymentOperationDescriptor) (DeploymentOperationDescriptor, error) {
	descriptor.Handle = strings.TrimSpace(descriptor.Handle)
	if err := validateDeploymentOperationHandle(descriptor.Handle); err != nil {
		return DeploymentOperationDescriptor{}, err
	}
	var err error
	descriptor.TargetOrigin, err = canonicalOperationOrigin(descriptor.TargetOrigin)
	if err != nil {
		return DeploymentOperationDescriptor{}, err
	}
	created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(descriptor.CreatedAt))
	if err != nil || created.IsZero() {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation createdAt must be RFC3339")
	}
	descriptor.CreatedAt = created.UTC().Format(time.RFC3339Nano)
	descriptor.ProjectID, descriptor.Environment = strings.TrimSpace(descriptor.ProjectID), strings.TrimSpace(descriptor.Environment)
	if descriptor.ProjectID == "" || descriptor.Environment == "" {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation requires project and environment")
	}
	descriptor.TargetID = strings.TrimSpace(descriptor.TargetID)
	descriptor.TargetSelector = strings.TrimSpace(descriptor.TargetSelector)
	descriptor.SourceSnapshotRef = strings.TrimSpace(descriptor.SourceSnapshotRef)
	descriptor.SourceSnapshotProjectID = strings.TrimSpace(descriptor.SourceSnapshotProjectID)
	descriptor.SourceSnapshotGraphDigest = strings.TrimSpace(descriptor.SourceSnapshotGraphDigest)
	descriptor.SourceRevision = strings.TrimSpace(descriptor.SourceRevision)
	descriptor.SourceRepository = strings.TrimSpace(descriptor.SourceRepository)
	descriptor.SourceRef = strings.TrimSpace(descriptor.SourceRef)
	descriptor.SourceChangeID = strings.TrimSpace(descriptor.SourceChangeID)
	descriptor.PlanID = strings.TrimSpace(descriptor.PlanID)
	descriptor.PlanDigest = strings.TrimSpace(descriptor.PlanDigest)
	descriptor.PlanStatus = strings.TrimSpace(descriptor.PlanStatus)
	descriptor.GovernanceDigest = strings.TrimSpace(descriptor.GovernanceDigest)
	descriptor.SourceDigest = strings.TrimSpace(descriptor.SourceDigest)
	descriptor.SourceAttestationDigest = strings.TrimSpace(descriptor.SourceAttestationDigest)
	descriptor.ProvenanceDigest = strings.TrimSpace(descriptor.ProvenanceDigest)
	descriptor.ExecutionDigest = strings.TrimSpace(descriptor.ExecutionDigest)
	descriptor.EvidenceDigest = strings.TrimSpace(descriptor.EvidenceDigest)
	descriptor.BuildID = strings.TrimSpace(descriptor.BuildID)
	descriptor.CandidateID = strings.TrimSpace(descriptor.CandidateID)
	descriptor.SealID = strings.TrimSpace(descriptor.SealID)
	descriptor.PublicationID = strings.TrimSpace(descriptor.PublicationID)
	descriptor.GenerationID = strings.TrimSpace(descriptor.GenerationID)
	descriptor.PublicationStatus = strings.TrimSpace(descriptor.PublicationStatus)
	descriptor.FailureCode = strings.TrimSpace(descriptor.FailureCode)
	descriptor.FailureDetail = strings.TrimSpace(descriptor.FailureDetail)
	descriptor.StatusURL = strings.TrimSpace(descriptor.StatusURL)
	if _, err := projectgraph.NewResourceID(descriptor.ProjectID); err != nil {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation project identity is invalid: %w", err)
	}
	if descriptor.TargetID != "" {
		if err := validateDeploymentOperationIdentity("targetId", descriptor.TargetID, 256); err != nil {
			return DeploymentOperationDescriptor{}, err
		}
	}
	if err := projectgraph.ValidateServingEnvironment(descriptor.Environment); err != nil {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation environment is invalid: %w", err)
	}
	for _, value := range []struct {
		name, value string
	}{
		{"targetId", descriptor.TargetID}, {"targetSelector", descriptor.TargetSelector}, {"projectId", descriptor.ProjectID}, {"environment", descriptor.Environment},
		{"sourceSnapshotRef", descriptor.SourceSnapshotRef}, {"sourceSnapshotProjectId", descriptor.SourceSnapshotProjectID}, {"sourceRevision", descriptor.SourceRevision}, {"sourceRepository", descriptor.SourceRepository}, {"sourceRef", descriptor.SourceRef}, {"sourceChangeId", descriptor.SourceChangeID},
		{"planId", descriptor.PlanID}, {"planStatus", descriptor.PlanStatus}, {"baseGenerationId", descriptor.BaseGenerationID}, {"buildId", descriptor.BuildID}, {"candidateId", descriptor.CandidateID}, {"sealId", descriptor.SealID}, {"publicationId", descriptor.PublicationID}, {"generationId", descriptor.GenerationID}, {"publicationStatus", descriptor.PublicationStatus}, {"failureCode", descriptor.FailureCode}, {"failureDetail", descriptor.FailureDetail},
	} {
		if err := validateDeploymentOperationText(value.name, value.value, maxDeploymentOperationTextBytes); err != nil {
			return DeploymentOperationDescriptor{}, err
		}
	}
	for name, value := range map[string]string{"targetSelector": descriptor.TargetSelector, "sourceRepository": descriptor.SourceRepository} {
		if strings.Contains(value, "://") {
			parsed, parseErr := url.Parse(value)
			if parseErr != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation %s must not contain credentials or query state", name)
			}
		}
	}
	for _, value := range []struct{ name, value string }{
		{"planIdempotencyKey", descriptor.PlanIdempotencyKey}, {"buildIdempotencyKey", descriptor.BuildIdempotencyKey}, {"publicationIdempotencyKey", descriptor.PublicationIdempotencyKey},
	} {
		if err := validateDeploymentOperationText(value.name, value.value, maxDeploymentOperationTextBytes); err != nil {
			return DeploymentOperationDescriptor{}, err
		}
	}
	if descriptor.SourceRoot != "" {
		descriptor.SourceRoot, err = filepath.Abs(descriptor.SourceRoot)
		if err != nil {
			return DeploymentOperationDescriptor{}, fmt.Errorf("resolve deployment source root: %w", err)
		}
		descriptor.SourceRoot = filepath.Clean(descriptor.SourceRoot)
		if err := validateDeploymentOperationText("sourceRoot", descriptor.SourceRoot, maxDeploymentOperationTextBytes); err != nil {
			return DeploymentOperationDescriptor{}, err
		}
	}
	if descriptor.SourceSnapshotProjectID != "" {
		if descriptor.SourceDigest == "" {
			return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation source snapshot requires a source digest")
		}
		if _, err := projectgraph.NewResourceID(descriptor.SourceSnapshotProjectID); err != nil {
			return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation source snapshot Project identity is invalid: %w", err)
		}
		sort.Slice(descriptor.SourceArtifacts, func(i, j int) bool { return descriptor.SourceArtifacts[i].Path < descriptor.SourceArtifacts[j].Path })
		if err := validatePortableSourceArtifacts(descriptor.SourceSnapshotProjectID, descriptor.SourceSnapshotGraphDigest, descriptor.SourceDigest, descriptor.SourceArtifacts); err != nil {
			return DeploymentOperationDescriptor{}, err
		}
	} else if len(descriptor.SourceArtifacts) != 0 {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation source artifacts require a snapshot Project identity")
	}
	descriptor.SourceRoot = strings.TrimSpace(descriptor.SourceRoot)
	for _, value := range []struct{ name, value string }{
		{"sourceDigest", descriptor.SourceDigest}, {"sourceAttestationDigest", descriptor.SourceAttestationDigest}, {"provenanceDigest", descriptor.ProvenanceDigest}, {"planDigest", descriptor.PlanDigest}, {"executionDigest", descriptor.ExecutionDigest}, {"evidenceDigest", descriptor.EvidenceDigest}, {"governanceDigest", descriptor.GovernanceDigest},
	} {
		if value.value != "" {
			if err := digest.ValidateSHA256Identity(value.value); err != nil {
				return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation %s is invalid: %w", value.name, err)
			}
		}
	}
	if descriptor.SourceSnapshotGraphDigest != "" {
		if err := digest.ValidateSHA256Identity(descriptor.SourceSnapshotGraphDigest); err != nil {
			return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation source graph digest is invalid: %w", err)
		}
	}
	if descriptor.StatusURL != "" {
		if err := validateDeploymentOperationStatusURL(descriptor.TargetOrigin, descriptor.StatusURL); err != nil {
			return DeploymentOperationDescriptor{}, err
		}
	}
	if descriptor.PlanEvidence.Digest != "" {
		if err := digest.ValidateSHA256Identity(descriptor.PlanEvidence.Digest); err != nil {
			return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation plan evidence digest is invalid: %w", err)
		}
	}
	if descriptor.PlanStatus != "" && descriptor.PlanStatus != "planned" && descriptor.PlanStatus != "expired" {
		return DeploymentOperationDescriptor{}, fmt.Errorf("unsupported deployment operation plan status %q", descriptor.PlanStatus)
	}
	if descriptor.PublicationStatus != "" {
		switch descriptor.PublicationStatus {
		case "pending", "committed", "rejected", "indeterminate":
		default:
			return DeploymentOperationDescriptor{}, fmt.Errorf("unsupported deployment operation publication status %q", descriptor.PublicationStatus)
		}
	}
	if descriptor.BuildRevision < 0 || descriptor.CandidateRevision < 0 || descriptor.BaseTargetRevision < 0 || descriptor.PublicationTargetRevision < 0 {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation revisions cannot be negative")
	}
	if err := validateDeploymentPlanEvidence(descriptor.PlanEvidence); err != nil {
		return DeploymentOperationDescriptor{}, err
	}
	if descriptor.PlanIdempotencyKey == "" || descriptor.BuildIdempotencyKey == "" || descriptor.PublicationIdempotencyKey == "" {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation requires stable mutation idempotency keys")
	}
	if descriptor.PlanIdempotencyKey != deploymentOperationIdempotencyKey(descriptor.Handle, "plan") || descriptor.BuildIdempotencyKey != deploymentOperationIdempotencyKey(descriptor.Handle, "build") || descriptor.PublicationIdempotencyKey != deploymentOperationIdempotencyKey(descriptor.Handle, "publish") {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation mutation idempotency keys are not stable for handle %q", descriptor.Handle)
	}
	if descriptor.Outcome == "" {
		descriptor.Outcome = DeploymentOperationUnknown
	}
	switch descriptor.Outcome {
	case DeploymentOperationUnknown, DeploymentOperationPendingApproval, DeploymentOperationActive, DeploymentOperationFailure, DeploymentOperationIndeterminate:
	default:
		return DeploymentOperationDescriptor{}, fmt.Errorf("unsupported deployment operation outcome %q", descriptor.Outcome)
	}
	return descriptor, nil
}

func validatePortableSourceArtifacts(projectID, graphDigest, sourceDigest string, artifacts []DeploymentSourceArtifact) error {
	if strings.TrimSpace(projectID) == "" || len(artifacts) == 0 {
		return fmt.Errorf("deployment operation source snapshot requires project and artifacts")
	}
	if len(artifacts) > maxDeploymentOperationArtifacts {
		return fmt.Errorf("deployment operation source snapshot contains too many artifacts")
	}
	if graphDigest != "" {
		if err := digest.ValidateSHA256Identity(graphDigest); err != nil {
			return fmt.Errorf("deployment operation source graph digest is invalid: %w", err)
		}
	}
	seen := make(map[string]struct{}, len(artifacts))
	var totalBytes int64
	for _, artifact := range artifacts {
		artifact.Path = strings.TrimSpace(artifact.Path)
		if artifact.Path == "" || path.IsAbs(artifact.Path) || path.Clean(artifact.Path) != artifact.Path || artifact.Path == "." || strings.HasPrefix(artifact.Path, "../") || strings.Contains(artifact.Path, "\\") {
			return fmt.Errorf("deployment operation source artifact path %q is not canonical", artifact.Path)
		}
		if len(artifact.Path) > 4096 {
			return fmt.Errorf("deployment operation source artifact path is too long")
		}
		if _, exists := seen[artifact.Path]; exists {
			return fmt.Errorf("deployment operation source snapshot repeats path %q", artifact.Path)
		}
		seen[artifact.Path] = struct{}{}
		if artifact.SizeBytes != int64(len(artifact.Content)) {
			return fmt.Errorf("deployment operation source artifact %q size does not match content", artifact.Path)
		}
		if artifact.SizeBytes < 0 || artifact.SizeBytes > maxDeploymentOperationArtifactSize {
			return fmt.Errorf("deployment operation source artifact %q exceeds the per-file size limit", artifact.Path)
		}
		totalBytes += artifact.SizeBytes
		if totalBytes > maxDeploymentOperationSourceBytes {
			return fmt.Errorf("deployment operation source snapshot exceeds the total size limit")
		}
		if err := digest.ValidateSHA256Identity(artifact.Digest); err != nil {
			return fmt.Errorf("deployment operation source artifact %q digest is invalid: %w", artifact.Path, err)
		}
		hash := sha256.Sum256(artifact.Content)
		if artifact.Digest != "sha256:"+hex.EncodeToString(hash[:]) {
			return fmt.Errorf("deployment operation source artifact %q content does not match digest", artifact.Path)
		}
		if safetext.Credentials(string(artifact.Content)) != string(artifact.Content) {
			return fmt.Errorf("deployment operation source artifact %q appears to contain credential material", artifact.Path)
		}
	}
	if sourceDigest != "" {
		ordered := append([]DeploymentSourceArtifact(nil), artifacts...)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
		hash := sha256.New()
		for _, artifact := range ordered {
			_, _ = fmt.Fprintf(hash, "%d:%s:%d:%s:%d:", len(artifact.Path), artifact.Path, len(artifact.Digest), artifact.Digest, artifact.SizeBytes)
		}
		if actual := "sha256:" + hex.EncodeToString(hash.Sum(nil)); actual != sourceDigest {
			return fmt.Errorf("deployment operation source snapshot digest does not match artifacts")
		}
	}
	return nil
}

func validateDeploymentOperationText(name, value string, maxBytes int) error {
	if len(value) > maxBytes {
		return fmt.Errorf("deployment operation %s is too large", name)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("deployment operation %s contains control characters", name)
		}
	}
	if safetext.Credentials(value) != value {
		return fmt.Errorf("deployment operation %s appears to contain credential material", name)
	}
	return nil
}

func validateDeploymentOperationIdentity(name, value string, maxBytes int) error {
	if value == "" {
		return nil
	}
	if len(value) > maxBytes {
		return fmt.Errorf("deployment operation %s is too large", name)
	}
	for index, character := range value {
		if index == 0 && !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9')) {
			return fmt.Errorf("deployment operation %s is not canonical", name)
		}
		if index > 0 && !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || strings.ContainsRune("_.:@/-", character)) {
			return fmt.Errorf("deployment operation %s is not canonical", name)
		}
	}
	return nil
}

func validateDeploymentOperationStatusURL(origin, statusURL string) error {
	base, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("deployment operation target origin is invalid: %w", err)
	}
	parsed, err := url.Parse(strings.TrimSpace(statusURL))
	if err != nil || parsed.Scheme != base.Scheme || parsed.Host != base.Host || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/candidates/") {
		return fmt.Errorf("deployment operation status URL must be a credential-free candidate URL under the target origin")
	}
	return nil
}

func validateDeploymentPlanEvidence(evidence DeliveryPlanEvidenceResult) error {
	if evidence.Digest != "" {
		if err := digest.ValidateSHA256Identity(evidence.Digest); err != nil {
			return fmt.Errorf("deployment operation plan evidence digest is invalid: %w", err)
		}
	}
	if len(evidence.PlannedInputs) > maxDeploymentOperationArtifacts || len(evidence.QualificationSteps) > maxDeploymentOperationArtifacts || len(evidence.ReuseDecisions) > maxDeploymentOperationArtifacts {
		return fmt.Errorf("deployment operation plan evidence contains too many entries")
	}
	if evidence.AddedCount < 0 || evidence.RemovedCount < 0 || evidence.DirectlyModifiedCount < 0 || evidence.IndirectlyAffectedCount < 0 || evidence.ReuseCount < 0 || evidence.QualificationStepCount < 0 {
		return fmt.Errorf("deployment operation plan evidence counts cannot be negative")
	}
	texts := []struct {
		name, value string
	}{
		{"plan impact statement", evidence.ImpactStatement}, {"plan physical-work statement", evidence.PhysicalWorkStatement}, {"plan reuse statement", evidence.ReuseStatement}, {"plan rollback class", evidence.RollbackClass}, {"plan qualification policy", evidence.QualificationPolicy}, {"stale policy description", evidence.StalePolicy.Description}, {"stale policy mode", evidence.StalePolicy.Mode},
	}
	for _, text := range texts {
		if err := validateDeploymentOperationText(text.name, text.value, maxDeploymentOperationTextBytes); err != nil {
			return err
		}
	}
	for index, input := range evidence.PlannedInputs {
		for _, text := range []struct{ name, value string }{{"plan input id", input.ID}, {"plan input mode", input.Mode}, {"plan input revision", input.Revision}, {"plan input bound", input.Bound}} {
			if err := validateDeploymentOperationText(fmt.Sprintf("plan input %d %s", index, text.name), text.value, maxDeploymentOperationTextBytes); err != nil {
				return err
			}
		}
	}
	for index, step := range evidence.QualificationSteps {
		for _, text := range []struct{ name, value string }{{"qualification step id", step.ID}, {"qualification step kind", step.Kind}, {"qualification step description", step.Description}} {
			if err := validateDeploymentOperationText(fmt.Sprintf("qualification step %d %s", index, text.name), text.value, maxDeploymentOperationTextBytes); err != nil {
				return err
			}
		}
	}
	for index, decision := range evidence.ReuseDecisions {
		if err := validateDeploymentOperationText(fmt.Sprintf("reuse decision %d resource id", index), decision.ResourceID, maxDeploymentOperationTextBytes); err != nil {
			return err
		}
		if err := validateDeploymentOperationText(fmt.Sprintf("reuse decision %d reason", index), decision.Reason, maxDeploymentOperationTextBytes); err != nil {
			return err
		}
		if decision.ReuseKeyDigest != "" {
			if err := digest.ValidateSHA256Identity(decision.ReuseKeyDigest); err != nil {
				return fmt.Errorf("deployment operation reuse key digest is invalid: %w", err)
			}
		}
	}
	return nil
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
		PlanDigest: descriptor.PlanDigest, PlanStatus: descriptor.PlanStatus, PlanEvidence: descriptor.PlanEvidence,
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
		{"planId", a.PlanID, b.PlanID}, {"planDigest", a.PlanDigest, b.PlanDigest}, {"governanceDigest", a.GovernanceDigest, b.GovernanceDigest},
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
