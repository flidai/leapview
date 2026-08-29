package compatibility

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	ReleasedV010ID               = "v0.1.0"
	ReleasedV010Platform         = "linux/amd64"
	ReleasedV010Repository       = "ghcr.io/yacobolo/libredash"
	ReleasedV010SourceRepository = "https://github.com/Yacobolo/libredash"
	ReleasedV010PlatformManifest = "sha256:a8a60264b099c21eb46bf2ed8de1cca7f5867687d570ca2c6c3f9de4ad11018a"
	ReleasedV010ConfigDigest     = "sha256:fbeefbd98aebc862dee9204478e6c8dd30a54c760929bfe1d7e4236cfe63c8f9"

	v010ReleaseIdentityEvidenceKind   = "leapview-v0.1-release-identity"
	v010EvidenceResultVerified        = "verified"
	v010ArtifactEvidenceMaxBytes      = 64 * 1024
	v010ExpectedSemanticResultSHA256  = "21bc8a539aa05a0d97e07b294bfead69daaee6e6be3d15698efa2ed8d028c473"
	v010ExpectedDashboardResultSHA256 = "4e235add0e3c1f6c9a399981e981fdfd04f33c472a451b4516bb6a569a9a4ef9"
)

//go:embed v010_artifact_evidence.schema.json
var v010ArtifactEvidenceSchemaJSON []byte

var (
	v010ArtifactEvidenceSchemaOnce sync.Once
	v010ArtifactEvidenceSchema     *jsonschema.Schema
	v010ArtifactEvidenceSchemaErr  error
)

// V010ArtifactResolutionRequest is deliberately limited to the immutable
// policy-owned identity. It contains no tag, local image, or source-build
// fallback surface.
type V010ArtifactResolutionRequest struct {
	Image                 string
	Platform              string
	RequireAuthentication bool
}

// V010ResolvedArtifact is the registry resolver's observation of the exact
// policy-owned v0.1 artifact. The compatibility owner validates every field
// before it can become qualification evidence.
type V010ResolvedArtifact struct {
	Image                  string
	ResolvedDigest         string
	Platform               string
	PlatformManifestDigest string
	ConfigDigest           string
	Authenticated          bool
	SourceRepository       string
	SourceTag              string
	Version                string
	SourceRevision         string
}

// V010ArtifactResolver must resolve the requested immutable registry object.
// Implementations must not substitute a local image, mutable tag, rebuilt
// artifact, or alternate registry namespace.
type V010ArtifactResolver interface {
	ResolveExact(context.Context, V010ArtifactResolutionRequest) (V010ResolvedArtifact, error)
}

type V010ArtifactVerificationOptions struct {
	PolicyDocument []byte
	Resolver       V010ArtifactResolver
	Now            func() time.Time
}

// V010ReleaseIdentityEvidence is the persisted proof that the exact released
// v0.1 artifact named by the supplied policy was available and its immutable
// registry and OCI provenance matched the reviewed historical release.
type V010ReleaseIdentityEvidence struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Kind          string                  `json:"kind"`
	VerifiedAt    time.Time               `json:"verifiedAt"`
	PolicyVersion string                  `json:"policyVersion"`
	PolicySHA256  string                  `json:"policySha256"`
	Identity      ReleaseIdentity         `json:"identity"`
	Artifact      V010ArtifactEvidence    `json:"artifact"`
	Provenance    V010ProvenanceEvidence  `json:"provenance"`
	Execution     *V010ContainerExecution `json:"execution,omitempty"`
	Result        string                  `json:"result"`
}

type V010ArtifactEvidence struct {
	Repository             string `json:"repository"`
	Image                  string `json:"image"`
	ResolvedDigest         string `json:"resolvedDigest"`
	Platform               string `json:"platform"`
	PlatformManifestDigest string `json:"platformManifestDigest"`
	ConfigDigest           string `json:"configDigest"`
	Authenticated          bool   `json:"authenticated"`
}

type V010ProvenanceEvidence struct {
	SourceRepository string `json:"sourceRepository"`
	SourceTag        string `json:"sourceTag"`
	Version          string `json:"version"`
	SourceRevision   string `json:"sourceRevision"`
}

// V010ContainerExecution records the isolated container identity and clean
// lifecycle for the same exact artifact. Host paths and credentials are
// deliberately excluded from persisted evidence.
type V010ContainerExecution struct {
	RunID           string                      `json:"runId"`
	ContainerID     string                      `json:"containerId"`
	ContainerName   string                      `json:"containerName"`
	Image           string                      `json:"image"`
	ImageID         string                      `json:"imageId"`
	Platform        string                      `json:"platform"`
	NetworkName     string                      `json:"networkName"`
	StateVolumeName string                      `json:"stateVolumeName"`
	StartedAt       time.Time                   `json:"startedAt"`
	StoppedAt       time.Time                   `json:"stoppedAt"`
	StartedState    string                      `json:"startedState"`
	StoppedState    string                      `json:"stoppedState"`
	CleanShutdown   bool                        `json:"cleanShutdown"`
	CleanupVerified bool                        `json:"cleanupVerified"`
	Journey         *V010ApplicationJourney     `json:"journey,omitempty"`
	Preservation    *V010PreservationEvidence   `json:"preservation,omitempty"`
	FreshCandidate  *V010FreshCandidateEvidence `json:"freshCandidate,omitempty"`
}

// V010ApplicationJourney records the assertions produced by the authentic
// v0.1 CLI and API journey. It deliberately retains stable identities and
// normalized result checksums, not bootstrap credentials or raw responses.
type V010ApplicationJourney struct {
	StartedAt              time.Time `json:"startedAt"`
	ReadyAt                time.Time `json:"readyAt"`
	CompletedAt            time.Time `json:"completedAt"`
	AdminEmail             string    `json:"adminEmail"`
	AdminPrincipalID       string    `json:"adminPrincipalId"`
	UserEmail              string    `json:"userEmail"`
	UserPrincipalID        string    `json:"userPrincipalId"`
	ProjectID              string    `json:"projectId"`
	Environment            string    `json:"environment"`
	PublishID              string    `json:"publishId"`
	ActivatedDigest        string    `json:"activatedDigest"`
	ProjectDigest          string    `json:"projectDigest"`
	ManagedDataRows        int       `json:"managedDataRows"`
	SemanticResultSHA256   string    `json:"semanticResultSha256"`
	DashboardResultSHA256  string    `json:"dashboardResultSha256"`
	AuthenticationVerified bool      `json:"authenticationVerified"`
	ProjectActivated       bool      `json:"projectActivated"`
	ManagedDataVerified    bool      `json:"managedDataVerified"`
	WorkloadVerified       bool      `json:"workloadVerified"`
}

// V010PreservationEvidence binds two owner-normalized observations around a
// clean stop and restart of the same exact container and state volume.
type V010PreservationEvidence struct {
	ObservedBeforeShutdownAt time.Time          `json:"observedBeforeShutdownAt"`
	ShutdownAt               time.Time          `json:"shutdownAt"`
	RestartedAt              time.Time          `json:"restartedAt"`
	ObservedAfterRestartAt   time.Time          `json:"observedAfterRestartAt"`
	Inventory                V010StateInventory `json:"inventory"`
	BeforeSHA256             string             `json:"beforeSha256"`
	AfterSHA256              string             `json:"afterSha256"`
	RestartIdentityVerified  bool               `json:"restartIdentityVerified"`
	StatePreserved           bool               `json:"statePreserved"`
}

type V010StateInventory struct {
	Application           V010InventoryApplication `json:"application"`
	Principals            []V010InventoryPrincipal `json:"principals"`
	Project               V010InventoryProject     `json:"project"`
	Publish               V010InventoryPublish     `json:"publish"`
	Assets                []V010InventoryAsset     `json:"assets"`
	ManagedDataRows       int                      `json:"managedDataRows"`
	SemanticResultSHA256  string                   `json:"semanticResultSha256"`
	DashboardResultSHA256 string                   `json:"dashboardResultSha256"`
}

type V010InventoryApplication struct {
	Image          string `json:"image"`
	ImageID        string `json:"imageId"`
	ContainerID    string `json:"containerId"`
	Platform       string `json:"platform"`
	SourceRevision string `json:"sourceRevision"`
}

type V010InventoryPrincipal struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

type V010InventoryProject struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Environment          string `json:"environment"`
	ActiveServingStateID string `json:"activeServingStateId"`
}

type V010InventoryPublish struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId"`
	Environment string `json:"environment"`
	Status      string `json:"status"`
	Digest      string `json:"digest"`
}

type V010InventoryAsset struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Key            string `json:"key"`
	ServingStateID string `json:"servingStateId"`
	ContentHash    string `json:"contentHash"`
}

// V010FreshCandidateEvidence proves that the policy candidate was initialized
// in state isolated from the preserved predecessor and that every attempted
// legacy-state adoption was denied without changing either state set.
type V010FreshCandidateEvidence struct {
	RunID                          string                      `json:"runId"`
	ContainerID                    string                      `json:"containerId"`
	ContainerName                  string                      `json:"containerName"`
	StateVolumeName                string                      `json:"stateVolumeName"`
	NetworkName                    string                      `json:"networkName"`
	Predecessor                    ReleaseIdentity             `json:"predecessor"`
	Candidate                      ReleaseIdentity             `json:"candidate"`
	CandidateImageID               string                      `json:"candidateImageId"`
	PolicyVersion                  string                      `json:"policyVersion"`
	PolicySHA256                   string                      `json:"policySha256"`
	FreshInstallDecision           Decision                    `json:"freshInstallDecision"`
	FreshStateBeforeSHA256         string                      `json:"freshStateBeforeSha256"`
	FreshStateAfterSHA256          string                      `json:"freshStateAfterSha256"`
	CandidateBeforeDenialsSHA256   string                      `json:"candidateBeforeDenialsSha256"`
	CandidateAfterDenialsSHA256    string                      `json:"candidateAfterDenialsSha256"`
	PredecessorBeforeDenialsSHA256 string                      `json:"predecessorBeforeDenialsSha256"`
	PredecessorAfterDenialsSHA256  string                      `json:"predecessorAfterDenialsSha256"`
	CandidateInventory             V010FreshCandidateInventory `json:"candidateInventory"`
	Denials                        []V010LegacyDenialEvidence  `json:"denials"`
	StartedAt                      time.Time                   `json:"startedAt"`
	CompletedAt                    time.Time                   `json:"completedAt"`
	CleanStateVerified             bool                        `json:"cleanStateVerified"`
	LegacyStateUnavailable         bool                        `json:"legacyStateUnavailable"`
	MutationFree                   bool                        `json:"mutationFree"`
	CleanupVerified                bool                        `json:"cleanupVerified"`
}

type V010FreshCandidateInventory struct {
	AdminEmail               string `json:"adminEmail"`
	AdminPrincipalID         string `json:"adminPrincipalId"`
	LegacyPrincipalCount     int    `json:"legacyPrincipalCount"`
	DashboardCount           int    `json:"dashboardCount"`
	SemanticModelCount       int    `json:"semanticModelCount"`
	LegacyProjectVisible     bool   `json:"legacyProjectVisible"`
	LegacyManagedDataVisible bool   `json:"legacyManagedDataVisible"`
}

type V010LegacyDenialEvidence struct {
	Scenario             string    `json:"scenario"`
	Operation            Operation `json:"operation"`
	ReasonCode           string    `json:"reasonCode"`
	BeforeSHA256         string    `json:"beforeSha256"`
	AfterSHA256          string    `json:"afterSha256"`
	DeniedBeforeMutation bool      `json:"deniedBeforeMutation"`
}

// V010StateInventorySHA256 hashes the ordered, normalized inventory contract.
// The inventory contains no timestamps, credentials, or raw API payloads.
func V010StateInventorySHA256(inventory V010StateInventory) (string, error) {
	document, err := json.Marshal(inventory)
	if err != nil {
		return "", fmt.Errorf("encode v0.1 state inventory: %w", err)
	}
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:]), nil
}

func EmbeddedV010ArtifactEvidenceSchema() []byte {
	return append([]byte(nil), v010ArtifactEvidenceSchemaJSON...)
}

// VerifyReleasedV010Artifact resolves only the exact v0.1 artifact from the
// supplied checked-in or candidate-bound policy and fails closed on every
// resolver or identity error. It never attempts an alternate reference.
func VerifyReleasedV010Artifact(ctx context.Context, options V010ArtifactVerificationOptions) (V010ReleaseIdentityEvidence, error) {
	if ctx == nil {
		return V010ReleaseIdentityEvidence{}, fmt.Errorf("v0.1 artifact verification context is required")
	}
	if options.Resolver == nil {
		return V010ReleaseIdentityEvidence{}, fmt.Errorf("v0.1 exact artifact resolver is required")
	}
	release, policy, policyDigest, err := releasedV010FromPolicy(options.PolicyDocument)
	if err != nil {
		return V010ReleaseIdentityEvidence{}, err
	}
	identity := release.IdentityForPlatform(ReleasedV010Platform)
	request := V010ArtifactResolutionRequest{
		Image:                 identity.Image,
		Platform:              ReleasedV010Platform,
		RequireAuthentication: true,
	}
	resolved, err := options.Resolver.ResolveExact(ctx, request)
	if err != nil {
		return V010ReleaseIdentityEvidence{}, fmt.Errorf("resolve exact policy-declared v0.1 artifact %s: %w", identity.Image, err)
	}
	if err := validateResolvedV010Artifact(resolved, identity); err != nil {
		return V010ReleaseIdentityEvidence{}, err
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	verifiedAt := now().UTC()
	if verifiedAt.IsZero() {
		return V010ReleaseIdentityEvidence{}, fmt.Errorf("v0.1 artifact verification time is required")
	}
	evidence := V010ReleaseIdentityEvidence{
		SchemaVersion: CurrentSchemaVersion,
		Kind:          v010ReleaseIdentityEvidenceKind,
		VerifiedAt:    verifiedAt,
		PolicyVersion: policy.PolicyVersion,
		PolicySHA256:  policyDigest,
		Identity:      identity,
		Artifact: V010ArtifactEvidence{
			Repository:             ReleasedV010Repository,
			Image:                  resolved.Image,
			ResolvedDigest:         resolved.ResolvedDigest,
			Platform:               resolved.Platform,
			PlatformManifestDigest: resolved.PlatformManifestDigest,
			ConfigDigest:           resolved.ConfigDigest,
			Authenticated:          resolved.Authenticated,
		},
		Provenance: V010ProvenanceEvidence{
			SourceRepository: resolved.SourceRepository,
			SourceTag:        resolved.SourceTag,
			Version:          resolved.Version,
			SourceRevision:   resolved.SourceRevision,
		},
		Result: v010EvidenceResultVerified,
	}
	if err := validateV010ReleaseIdentityEvidence(evidence, options.PolicyDocument); err != nil {
		return V010ReleaseIdentityEvidence{}, fmt.Errorf("validate produced v0.1 release identity evidence: %w", err)
	}
	return evidence, nil
}

// ValidateV010ReleaseIdentityEvidence is the compatibility owner's canonical
// validator for a persisted release-identity.json document. Callers must
// supply the exact policy document used by the qualification run.
func ValidateV010ReleaseIdentityEvidence(document, policyDocument []byte) (V010ReleaseIdentityEvidence, error) {
	if len(document) == 0 || len(document) > v010ArtifactEvidenceMaxBytes {
		return V010ReleaseIdentityEvidence{}, fmt.Errorf("v0.1 release identity evidence size must be between 1 and %d bytes", v010ArtifactEvidenceMaxBytes)
	}
	schema, err := compiledV010ArtifactEvidenceSchema()
	if err != nil {
		return V010ReleaseIdentityEvidence{}, err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return V010ReleaseIdentityEvidence{}, fmt.Errorf("decode v0.1 release identity evidence JSON: %w", err)
	}
	if err := schema.Validate(value); err != nil {
		return V010ReleaseIdentityEvidence{}, fmt.Errorf("validate v0.1 release identity evidence schema: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var evidence V010ReleaseIdentityEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return V010ReleaseIdentityEvidence{}, fmt.Errorf("decode v0.1 release identity evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return V010ReleaseIdentityEvidence{}, fmt.Errorf("v0.1 release identity evidence contains trailing data")
	}
	if err := validateV010ReleaseIdentityEvidence(evidence, policyDocument); err != nil {
		return V010ReleaseIdentityEvidence{}, err
	}
	return evidence, nil
}

// MarshalV010ReleaseIdentityEvidence produces the canonical persisted
// release-identity.json document only after applying the owner validator.
func MarshalV010ReleaseIdentityEvidence(evidence V010ReleaseIdentityEvidence, policyDocument []byte) ([]byte, error) {
	if err := validateV010ReleaseIdentityEvidence(evidence, policyDocument); err != nil {
		return nil, err
	}
	document, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode v0.1 release identity evidence: %w", err)
	}
	document = append(document, '\n')
	if _, err := ValidateV010ReleaseIdentityEvidence(document, policyDocument); err != nil {
		return nil, err
	}
	return document, nil
}

func validateV010ReleaseIdentityEvidence(evidence V010ReleaseIdentityEvidence, policyDocument []byte) error {
	release, policy, policyDigest, err := releasedV010FromPolicy(policyDocument)
	if err != nil {
		return err
	}
	identity := release.IdentityForPlatform(ReleasedV010Platform)
	if evidence.SchemaVersion != CurrentSchemaVersion || evidence.Kind != v010ReleaseIdentityEvidenceKind ||
		evidence.Result != v010EvidenceResultVerified || evidence.VerifiedAt.IsZero() {
		return fmt.Errorf("v0.1 release identity evidence result is incomplete")
	}
	if evidence.PolicyVersion != policy.PolicyVersion || evidence.PolicySHA256 != policyDigest {
		return fmt.Errorf("v0.1 release identity evidence does not bind the supplied policy")
	}
	if evidence.Identity != identity {
		return fmt.Errorf("v0.1 release identity evidence does not match the exact policy-declared identity")
	}
	resolved := V010ResolvedArtifact{
		Image: evidence.Artifact.Image, ResolvedDigest: evidence.Artifact.ResolvedDigest,
		Platform: evidence.Artifact.Platform, PlatformManifestDigest: evidence.Artifact.PlatformManifestDigest,
		ConfigDigest: evidence.Artifact.ConfigDigest, Authenticated: evidence.Artifact.Authenticated,
		SourceRepository: evidence.Provenance.SourceRepository, SourceTag: evidence.Provenance.SourceTag,
		Version: evidence.Provenance.Version, SourceRevision: evidence.Provenance.SourceRevision,
	}
	if evidence.Artifact.Repository != ReleasedV010Repository {
		return fmt.Errorf("v0.1 release identity evidence registry repository is not the reviewed repository")
	}
	if err := validateResolvedV010Artifact(resolved, identity); err != nil {
		return err
	}
	if evidence.Execution != nil {
		if err := validateV010ContainerExecution(*evidence.Execution, evidence, policyDocument); err != nil {
			return err
		}
	}
	return nil
}

func validateV010ContainerExecution(execution V010ContainerExecution, evidence V010ReleaseIdentityEvidence, policyDocument []byte) error {
	if len(execution.RunID) != 32 || !isLowerHex(execution.RunID) ||
		len(execution.ContainerID) != 64 || !isLowerHex(execution.ContainerID) {
		return fmt.Errorf("v0.1 execution run and container identities are invalid")
	}
	prefix := "leapview-v010-" + execution.RunID
	if execution.ContainerName != prefix || execution.NetworkName != prefix+"-network" ||
		execution.StateVolumeName != prefix+"-state" {
		return fmt.Errorf("v0.1 execution resource identities do not match the isolated run")
	}
	if execution.Image != evidence.Identity.Image || execution.ImageID != evidence.Artifact.ConfigDigest ||
		execution.Platform != ReleasedV010Platform {
		return fmt.Errorf("v0.1 execution container does not match the verified artifact identity")
	}
	if execution.StartedAt.IsZero() || execution.StoppedAt.Before(execution.StartedAt) ||
		execution.StartedState != "running" || execution.StoppedState != "exited" ||
		!execution.CleanShutdown || !execution.CleanupVerified {
		return fmt.Errorf("v0.1 execution did not complete a clean isolated lifecycle")
	}
	if execution.Journey == nil {
		return fmt.Errorf("v0.1 execution omitted the authentic application journey")
	}
	if err := validateV010ApplicationJourney(*execution.Journey, execution); err != nil {
		return err
	}
	if execution.Preservation == nil {
		return fmt.Errorf("v0.1 execution omitted stopped-state preservation evidence")
	}
	if err := validateV010PreservationEvidence(*execution.Preservation, *execution.Journey, execution, evidence); err != nil {
		return err
	}
	if execution.FreshCandidate == nil {
		return fmt.Errorf("v0.1 execution omitted fresh candidate denial evidence")
	}
	if err := validateV010FreshCandidateEvidence(*execution.FreshCandidate, execution, evidence, policyDocument); err != nil {
		return err
	}
	return nil
}

func validateV010ApplicationJourney(journey V010ApplicationJourney, execution V010ContainerExecution) error {
	if journey.StartedAt.Before(execution.StartedAt) || journey.ReadyAt.Before(journey.StartedAt) ||
		journey.CompletedAt.Before(journey.ReadyAt) || execution.StoppedAt.Before(journey.CompletedAt) {
		return fmt.Errorf("v0.1 application journey chronology is invalid")
	}
	if journey.AdminEmail != "fai-517-admin@qualification.invalid" ||
		journey.UserEmail != "fai-517-user@qualification.invalid" || journey.ProjectID != "compatibility" ||
		journey.Environment != "fai-517" || journey.AdminPrincipalID == "" || journey.UserPrincipalID == "" ||
		journey.PublishID == "" || journey.ActivatedDigest == "" || journey.ProjectDigest == "" {
		return fmt.Errorf("v0.1 application journey identities are incomplete")
	}
	if journey.ManagedDataRows != 3 || journey.SemanticResultSHA256 != v010ExpectedSemanticResultSHA256 ||
		journey.DashboardResultSHA256 != v010ExpectedDashboardResultSHA256 ||
		!journey.AuthenticationVerified || !journey.ProjectActivated || !journey.ManagedDataVerified || !journey.WorkloadVerified {
		return fmt.Errorf("v0.1 application journey did not prove the deterministic workload")
	}
	return nil
}

func validateV010PreservationEvidence(
	preservation V010PreservationEvidence,
	journey V010ApplicationJourney,
	execution V010ContainerExecution,
	evidence V010ReleaseIdentityEvidence,
) error {
	if preservation.ObservedBeforeShutdownAt.Before(journey.CompletedAt) ||
		preservation.ShutdownAt.Before(preservation.ObservedBeforeShutdownAt) ||
		preservation.RestartedAt.Before(preservation.ShutdownAt) ||
		preservation.ObservedAfterRestartAt.Before(preservation.RestartedAt) ||
		execution.StoppedAt.Before(preservation.ObservedAfterRestartAt) {
		return fmt.Errorf("v0.1 stopped-state preservation chronology is invalid")
	}
	if !preservation.RestartIdentityVerified || !preservation.StatePreserved {
		return fmt.Errorf("v0.1 stopped-state preservation was not verified")
	}
	hash, err := V010StateInventorySHA256(preservation.Inventory)
	if err != nil {
		return err
	}
	if preservation.BeforeSHA256 != hash || preservation.AfterSHA256 != hash {
		return fmt.Errorf("v0.1 before and after inventory checksums do not match the normalized inventory")
	}
	inventory := preservation.Inventory
	if inventory.Application != (V010InventoryApplication{
		Image: evidence.Identity.Image, ImageID: evidence.Artifact.ConfigDigest,
		ContainerID: execution.ContainerID, Platform: ReleasedV010Platform,
		SourceRevision: evidence.Provenance.SourceRevision,
	}) {
		return fmt.Errorf("v0.1 inventory application identity does not match the exact released artifact")
	}
	if len(inventory.Principals) != 2 ||
		inventory.Principals[0].ID != journey.AdminPrincipalID ||
		inventory.Principals[0].Email != journey.AdminEmail ||
		inventory.Principals[1].ID != journey.UserPrincipalID ||
		inventory.Principals[1].Email != journey.UserEmail {
		return fmt.Errorf("v0.1 inventory principals do not match the authenticated journey")
	}
	if inventory.Project.ID != journey.ProjectID || inventory.Project.Title != "FAI-517 Compatibility" ||
		inventory.Project.Environment != journey.Environment || inventory.Project.ActiveServingStateID == "" {
		return fmt.Errorf("v0.1 inventory project or environment identity is incomplete")
	}
	if inventory.Publish.ID != journey.PublishID || inventory.Publish.ProjectID != journey.ProjectID ||
		inventory.Publish.Environment != journey.Environment || inventory.Publish.Status != "active" ||
		inventory.Publish.Digest != journey.ActivatedDigest {
		return fmt.Errorf("v0.1 inventory published workload identity does not match the activated journey")
	}
	if inventory.ManagedDataRows != journey.ManagedDataRows ||
		inventory.SemanticResultSHA256 != journey.SemanticResultSHA256 ||
		inventory.DashboardResultSHA256 != journey.DashboardResultSHA256 {
		return fmt.Errorf("v0.1 inventory query-visible managed data does not match the journey")
	}
	if err := validateV010InventoryAssets(inventory.Assets, inventory.Project.ActiveServingStateID); err != nil {
		return err
	}
	return nil
}

func validateV010InventoryAssets(assets []V010InventoryAsset, servingStateID string) error {
	required := map[string]bool{
		"source:qualification.orders":                false,
		"model_table:compatibility.orders":           false,
		"semantic_model:compatibility.compatibility": false,
		"dashboard:compatibility.preservation":       false,
	}
	previous := ""
	for _, asset := range assets {
		key := asset.Type + ":" + asset.Key
		if asset.ID == "" || asset.ContentHash == "" || asset.ServingStateID != servingStateID || key <= previous {
			return fmt.Errorf("v0.1 inventory asset metadata is incomplete or not canonically ordered")
		}
		previous = key
		if _, ok := required[key]; ok {
			required[key] = true
		}
	}
	for key, found := range required {
		if !found {
			return fmt.Errorf("v0.1 inventory omits required published asset %s", key)
		}
	}
	return nil
}

func validateV010FreshCandidateEvidence(
	fresh V010FreshCandidateEvidence,
	execution V010ContainerExecution,
	evidence V010ReleaseIdentityEvidence,
	policyDocument []byte,
) error {
	if len(fresh.RunID) != 32 || !isLowerHex(fresh.RunID) || len(fresh.ContainerID) != 64 || !isLowerHex(fresh.ContainerID) {
		return fmt.Errorf("fresh candidate run and container identities are invalid")
	}
	prefix := "leapview-v010-candidate-" + fresh.RunID
	if fresh.ContainerName != prefix || fresh.NetworkName != prefix+"-network" || fresh.StateVolumeName != prefix+"-state" {
		return fmt.Errorf("fresh candidate resources are not bound to the isolated run")
	}
	_, policy, policyDigest, err := releasedV010FromPolicy(policyDocument)
	if err != nil {
		return err
	}
	if fresh.PolicyVersion != evidence.PolicyVersion || fresh.PolicySHA256 != evidence.PolicySHA256 ||
		fresh.PolicyVersion != policy.PolicyVersion || fresh.PolicySHA256 != policyDigest {
		return fmt.Errorf("fresh candidate evidence is not bound to the exact transition policy")
	}
	candidateRelease, ok := policy.ReleaseByID(policy.CandidateRelease)
	if !ok {
		return fmt.Errorf("transition policy candidate release is unavailable")
	}
	wantCandidate := candidateRelease.IdentityForPlatform(ReleasedV010Platform)
	if fresh.Predecessor != evidence.Identity || fresh.Candidate != wantCandidate || !validV010OCIDigest(fresh.CandidateImageID) {
		return fmt.Errorf("fresh candidate evidence does not bind the exact predecessor and candidate artifacts")
	}
	wantFresh := policy.Evaluate(Request{Operation: OperationFreshInstall, Next: wantCandidate})
	if !reflect.DeepEqual(fresh.FreshInstallDecision, wantFresh) || !fresh.FreshInstallDecision.Allowed ||
		fresh.FreshInstallDecision.ReasonCode != ReasonAllowedFreshInstall {
		return fmt.Errorf("fresh candidate evidence contains an invalid fresh-install decision")
	}
	for label, value := range map[string]string{
		"fresh state before":         fresh.FreshStateBeforeSHA256,
		"fresh state after":          fresh.FreshStateAfterSHA256,
		"candidate before denials":   fresh.CandidateBeforeDenialsSHA256,
		"candidate after denials":    fresh.CandidateAfterDenialsSHA256,
		"predecessor before denials": fresh.PredecessorBeforeDenialsSHA256,
		"predecessor after denials":  fresh.PredecessorAfterDenialsSHA256,
	} {
		if len(value) != 64 || !isLowerHex(value) {
			return fmt.Errorf("fresh candidate %s checksum is invalid", label)
		}
	}
	if fresh.FreshStateBeforeSHA256 == fresh.FreshStateAfterSHA256 ||
		fresh.CandidateBeforeDenialsSHA256 != fresh.FreshStateAfterSHA256 ||
		fresh.CandidateAfterDenialsSHA256 != fresh.CandidateBeforeDenialsSHA256 ||
		fresh.PredecessorAfterDenialsSHA256 != fresh.PredecessorBeforeDenialsSHA256 {
		return fmt.Errorf("fresh candidate or predecessor state changed during denied legacy adoption")
	}
	if fresh.StartedAt.Before(execution.StoppedAt) || fresh.CompletedAt.Before(fresh.StartedAt) ||
		!fresh.CleanStateVerified || !fresh.LegacyStateUnavailable || !fresh.MutationFree || !fresh.CleanupVerified {
		return fmt.Errorf("fresh candidate lifecycle did not prove isolated mutation-free qualification")
	}
	inventory := fresh.CandidateInventory
	if inventory.AdminEmail != "fai-517-candidate@qualification.invalid" || strings.TrimSpace(inventory.AdminPrincipalID) == "" ||
		inventory.LegacyPrincipalCount != 0 || inventory.DashboardCount != 0 || inventory.SemanticModelCount != 0 ||
		inventory.LegacyProjectVisible || inventory.LegacyManagedDataVisible {
		return fmt.Errorf("fresh candidate inventory contains preserved v0.1 state")
	}
	if len(fresh.Denials) != 3 {
		return fmt.Errorf("fresh candidate evidence must contain all legacy-state denial scenarios")
	}
	wantUpgrade := policy.Evaluate(Request{Operation: OperationUpgrade, Current: evidence.Identity, Next: wantCandidate})
	wantAdoption := policy.Evaluate(Request{Operation: OperationFreshInstall, Next: evidence.Identity})
	wants := []struct {
		scenario string
		decision Decision
		before   string
	}{
		{scenario: "preserved-state-mount", decision: wantUpgrade, before: fresh.PredecessorBeforeDenialsSHA256},
		{scenario: "legacy-state-reference", decision: wantUpgrade, before: fresh.PredecessorBeforeDenialsSHA256},
		{scenario: "direct-legacy-artifact-adoption", decision: wantAdoption, before: fresh.CandidateBeforeDenialsSHA256},
	}
	for index, want := range wants {
		denial := fresh.Denials[index]
		if denial.Scenario != want.scenario || denial.Operation != want.decision.Operation ||
			denial.ReasonCode != want.decision.ReasonCode || want.decision.Allowed ||
			denial.BeforeSHA256 != want.before || denial.AfterSHA256 != denial.BeforeSHA256 || !denial.DeniedBeforeMutation {
			return fmt.Errorf("fresh candidate denial evidence for %s is invalid", want.scenario)
		}
	}
	return nil
}

func validateResolvedV010Artifact(resolved V010ResolvedArtifact, identity ReleaseIdentity) error {
	wantDigest := identity.Image[strings.LastIndex(identity.Image, "@")+1:]
	for label, value := range map[string]string{
		"resolved manifest": resolved.ResolvedDigest,
		"platform manifest": resolved.PlatformManifestDigest,
		"config":            resolved.ConfigDigest,
	} {
		if !validV010OCIDigest(value) {
			return fmt.Errorf("v0.1 %s digest is invalid", label)
		}
	}
	if resolved.Image != identity.Image || resolved.ResolvedDigest != wantDigest {
		return fmt.Errorf("v0.1 registry manifest does not match the exact policy-declared immutable image")
	}
	if resolved.PlatformManifestDigest != ReleasedV010PlatformManifest || resolved.ConfigDigest != ReleasedV010ConfigDigest {
		return fmt.Errorf("v0.1 platform manifest and config do not match the reviewed immutable OCI graph")
	}
	if resolved.Platform != ReleasedV010Platform || identity.Platform != ReleasedV010Platform {
		return fmt.Errorf("v0.1 artifact platform must be %s", ReleasedV010Platform)
	}
	if !resolved.Authenticated {
		return fmt.Errorf("v0.1 authentication-required artifact was not resolved with registry credentials")
	}
	if resolved.SourceRepository != ReleasedV010SourceRepository || resolved.SourceTag != ReleasedV010ID ||
		resolved.Version != identity.Version || resolved.SourceRevision != identity.SourceRevision {
		return fmt.Errorf("v0.1 OCI provenance does not match the reviewed source tag, repository, version, and revision")
	}
	return nil
}

func releasedV010FromPolicy(document []byte) (Release, *Policy, string, error) {
	if len(document) == 0 {
		return Release{}, nil, "", fmt.Errorf("v0.1 qualification requires the exact release-transition policy document")
	}
	policy, err := ParsePolicy(document)
	if err != nil {
		return Release{}, nil, "", err
	}
	release, ok := policy.ReleaseByID(ReleasedV010ID)
	if !ok {
		return Release{}, nil, "", fmt.Errorf("release-transition policy omits %s", ReleasedV010ID)
	}
	embedded, err := EmbeddedPolicy()
	if err != nil {
		return Release{}, nil, "", err
	}
	reviewed, ok := embedded.ReleaseByID(ReleasedV010ID)
	if !ok {
		return Release{}, nil, "", fmt.Errorf("checked-in release-transition policy omits %s", ReleasedV010ID)
	}
	if !reflect.DeepEqual(release, reviewed) {
		return Release{}, nil, "", fmt.Errorf("release-transition policy v0.1 identity does not match the checked-in reviewed release")
	}
	if release.Distribution != "authentication-required" || len(release.Artifacts) != 1 ||
		release.Artifacts[0].Platform != ReleasedV010Platform || release.Artifacts[0].Image != ReleasedV010Image {
		return Release{}, nil, "", fmt.Errorf("checked-in v0.1 release identity is not the supported authentication-required linux/amd64 artifact")
	}
	digest := sha256.Sum256(document)
	return release, policy, hex.EncodeToString(digest[:]), nil
}

func validV010OCIDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	encoded := strings.TrimPrefix(value, "sha256:")
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == sha256.Size && encoded == strings.ToLower(encoded)
}

func compiledV010ArtifactEvidenceSchema() (*jsonschema.Schema, error) {
	v010ArtifactEvidenceSchemaOnce.Do(func() {
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(v010ArtifactEvidenceSchemaJSON))
		if err != nil {
			v010ArtifactEvidenceSchemaErr = fmt.Errorf("decode v0.1 artifact evidence schema: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		const schemaURL = "https://leapview.dev/schemas/v0.1-artifact-evidence.schema.json"
		if err := compiler.AddResource(schemaURL, document); err != nil {
			v010ArtifactEvidenceSchemaErr = fmt.Errorf("register v0.1 artifact evidence schema: %w", err)
			return
		}
		v010ArtifactEvidenceSchema, v010ArtifactEvidenceSchemaErr = compiler.Compile(schemaURL)
	})
	return v010ArtifactEvidenceSchema, v010ArtifactEvidenceSchemaErr
}
