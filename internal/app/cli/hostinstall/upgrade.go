package hostinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/flidai/leapview/internal/platform/ociref"
	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/flidai/leapview/internal/release/transitionrunner"
)

// UpgradeControl binds the installed Compose lifecycle without giving host
// upgrade access to unrelated controller operations.
type UpgradeControl struct {
	ConfiguredImage func() (string, error)
	UpdateImage     func(string) error
	Start           func(context.Context) error
	RunningImage    func(context.Context) (string, error)
}

// UpgradeOperationStore exposes only the existing operation and target fence.
// The transition runner, not the host command, records phase results.
type UpgradeOperationStore interface {
	Get(context.Context, string) (transitionoperation.Operation, error)
	Validate(context.Context, transitionoperation.Fence) error
}

type UpgradeRequest struct {
	OperationID    string
	CandidateImage string
	TargetID       string
	Phase          transitionrunner.Phase
}

type UpgradeOptions struct {
	Paths      Paths
	Operations UpgradeOperationStore
	Preflight  transitionrunner.PreflightAuthority
	Control    UpgradeControl
	Payload    func(context.Context, string) (map[string][]byte, error)
	Activate   func(Paths, string) error
}

type Upgrader struct{ options UpgradeOptions }

func NewUpgrader(options UpgradeOptions) (*Upgrader, error) {
	if options.Paths.Root == "" || options.Operations == nil || options.Preflight == nil || options.Control.ConfiguredImage == nil || options.Control.UpdateImage == nil || options.Control.Start == nil || options.Control.RunningImage == nil || options.Payload == nil {
		return nil, fmt.Errorf("host upgrade requires installation paths, operation authority, preflight authority, Compose control, and candidate payload source")
	}
	if options.Activate == nil {
		options.Activate = activateGeneration
	}
	return &Upgrader{options: options}, nil
}

// Upgrade executes exactly one host effect under the runner's live target
// fence. The returned EffectResult carries bound evidence for that runner to
// validate and persist before it advances to the next phase.
func (u *Upgrader) Upgrade(ctx context.Context, request UpgradeRequest) (transitionrunner.EffectResult, error) {
	if strings.TrimSpace(request.OperationID) == "" || strings.TrimSpace(request.TargetID) == "" {
		return transitionrunner.EffectResult{}, fmt.Errorf("operation ID and target ID are required")
	}
	if request.Phase != transitionrunner.PhaseStage && request.Phase != transitionrunner.PhaseActivate && request.Phase != transitionrunner.PhaseRestart {
		return transitionrunner.EffectResult{}, fmt.Errorf("unsupported host upgrade phase %q", request.Phase)
	}
	candidateRef, err := ociref.ParseImmutable(request.CandidateImage)
	if err != nil {
		return transitionrunner.EffectResult{}, fmt.Errorf("candidate image: %w", err)
	}
	paths := u.options.Paths
	lock, err := instancelock.AcquireNamed(paths.Root, installLockName)
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	defer lock.Release()
	installed, _, err := readAndValidateConfig(filepath.Join(paths.Root, installMarkerName))
	if err != nil {
		return transitionrunner.EffectResult{}, fmt.Errorf("existing host installation is required: %w", err)
	}
	op, err := u.options.Operations.Get(ctx, request.OperationID)
	if err != nil {
		return transitionrunner.EffectResult{}, fmt.Errorf("read transition operation: %w", err)
	}
	if op.Status != transitionoperation.StatusRunning || op.CurrentPhase != request.Phase {
		return transitionrunner.EffectResult{}, fmt.Errorf("transition operation is not at %s", request.Phase)
	}
	evidence, err := transitionpreflight.ParseEvidence(op.PreflightEvidence)
	if err != nil {
		return transitionrunner.EffectResult{}, fmt.Errorf("stored transition preflight: %w", err)
	}
	digest, err := evidence.Digest()
	if err != nil || digest != op.PreflightEvidenceDigest || evidence.TargetIdentityDigest != op.TargetIdentityDigest {
		return transitionrunner.EffectResult{}, fmt.Errorf("stored transition preflight identity is invalid")
	}
	predecessorDigest, err := evidence.Predecessor.Digest()
	if err != nil || predecessorDigest != op.PredecessorArtifactDigest {
		return transitionrunner.EffectResult{}, fmt.Errorf("stored predecessor admission identity is invalid")
	}
	candidateDigest, err := evidence.Candidate.Digest()
	if err != nil || candidateDigest != op.CandidateArtifactDigest || evidence.Candidate.Release.Image != request.CandidateImage {
		return transitionrunner.EffectResult{}, fmt.Errorf("candidate does not match admitted transition artifact")
	}
	if evidence.Decision != transitionpreflight.DecisionBinaryRollbackCompatible || evidence.RecoveryFrontier == nil {
		return transitionrunner.EffectResult{}, fmt.Errorf("candidate is unsupported by transition preflight")
	}
	fresh, err := u.options.Preflight.ResolveAndEvaluate(ctx, transitionpreflight.ResolutionRequest{
		PredecessorRef: evidence.Predecessor.Release.Image, CandidateRef: request.CandidateImage,
		TargetRef: request.TargetID, RecoveryFrontier: *evidence.RecoveryFrontier,
	})
	if err != nil {
		return transitionrunner.EffectResult{}, fmt.Errorf("authoritative preflight failed: %w", err)
	}
	if fresh.EvidenceDigest != digest {
		return transitionrunner.EffectResult{}, fmt.Errorf("stale or mismatched authoritative preflight")
	}
	if err := u.options.Operations.Validate(ctx, op.Fence); err != nil {
		return transitionrunner.EffectResult{}, fmt.Errorf("transition target fence is unavailable: %w", err)
	}
	configured, err := u.options.Control.ConfiguredImage()
	if err != nil {
		return transitionrunner.EffectResult{}, fmt.Errorf("read configured host image: %w", err)
	}
	active, err := activeGeneration(paths)
	if err != nil {
		return transitionrunner.EffectResult{}, fmt.Errorf("read active host generation: %w", err)
	}
	installedRef, err := ociref.ParseImmutable(installed.Image)
	if err != nil {
		return transitionrunner.EffectResult{}, fmt.Errorf("installation marker image is not immutable: %w", err)
	}
	resumingActivation := request.Phase == transitionrunner.PhaseActivate && installed.Image == evidence.Predecessor.Release.Image && active == candidateRef.Generation
	if active != installedRef.Generation && !resumingActivation {
		return transitionrunner.EffectResult{}, fmt.Errorf("active host generation does not match installation marker")
	}
	if configured != installed.Image && !(resumingActivation && configured == request.CandidateImage) {
		return transitionrunner.EffectResult{}, fmt.Errorf("configured host image does not match installation marker")
	}
	if installed.Image != evidence.Predecessor.Release.Image && installed.Image != request.CandidateImage {
		return transitionrunner.EffectResult{}, fmt.Errorf("installed image does not match admitted transition artifacts")
	}
	phaseEvidence := upgradePhaseEvidence{
		PredecessorImage:     evidence.Predecessor.Release.Image,
		CandidateImage:       request.CandidateImage,
		TargetIdentityDigest: evidence.TargetIdentityDigest,
		PreflightDigest:      digest,
		Generation:           candidateRef.Generation,
	}
	switch request.Phase {
	case transitionrunner.PhaseStage:
		if installed.Image != evidence.Predecessor.Release.Image {
			return transitionrunner.EffectResult{}, fmt.Errorf("candidate is already active before staging phase")
		}
		payload, err := u.options.Payload(ctx, request.CandidateImage)
		if err != nil {
			return transitionrunner.EffectResult{}, fmt.Errorf("extract admitted candidate payload: %w", err)
		}
		generation, err := stageGeneration(paths, request.CandidateImage, payload)
		if err != nil {
			return transitionrunner.EffectResult{}, fmt.Errorf("stage candidate generation: %w", err)
		}
		phaseEvidence.Generation = generation
	case transitionrunner.PhaseActivate:
		if installed.Image == evidence.Predecessor.Release.Image {
			payload, err := u.options.Payload(ctx, request.CandidateImage)
			if err != nil {
				return transitionrunner.EffectResult{}, fmt.Errorf("verify admitted candidate payload: %w", err)
			}
			if err := validateGeneration(filepath.Join(paths.Root, "releases", candidateRef.Generation), payload); err != nil {
				return transitionrunner.EffectResult{}, fmt.Errorf("staged candidate generation is invalid: %w", err)
			}
			if err := ensurePayloadLinks(paths); err != nil {
				return transitionrunner.EffectResult{}, fmt.Errorf("installed deployment links are invalid: %w", err)
			}
			if err := u.options.Operations.Validate(ctx, op.Fence); err != nil {
				return transitionrunner.EffectResult{}, fmt.Errorf("transition target fence was lost before activation: %w", err)
			}
			if active != candidateRef.Generation {
				if err := u.options.Activate(paths, candidateRef.Generation); err != nil {
					return transitionrunner.EffectResult{}, fmt.Errorf("activate candidate generation: %w", err)
				}
			}
			if configured != request.CandidateImage {
				if err := u.options.Control.UpdateImage(request.CandidateImage); err != nil {
					return transitionrunner.EffectResult{}, fmt.Errorf("candidate generation activated but Compose selection is uncertain: %w", err)
				}
			}
			installed.Image = request.CandidateImage
			marker, err := json.MarshalIndent(installed, "", "  ")
			if err != nil {
				return transitionrunner.EffectResult{}, err
			}
			if err := securefs.WritePrivateFileAtomic(filepath.Join(paths.Root, installMarkerName), append(marker, '\n')); err != nil {
				return transitionrunner.EffectResult{}, fmt.Errorf("candidate generation activated but installation marker is uncertain: %w", err)
			}
		} else if active != candidateRef.Generation {
			return transitionrunner.EffectResult{}, fmt.Errorf("candidate marker does not match active generation")
		}
	case transitionrunner.PhaseRestart:
		if installed.Image != request.CandidateImage || active != candidateRef.Generation {
			return transitionrunner.EffectResult{}, fmt.Errorf("candidate is not active for restart")
		}
		if err := u.options.Control.Start(ctx); err != nil {
			return transitionrunner.EffectResult{}, fmt.Errorf("restart candidate: %w", err)
		}
		running, err := u.options.Control.RunningImage(ctx)
		if err != nil {
			return transitionrunner.EffectResult{}, fmt.Errorf("verify running candidate image: %w", err)
		}
		if running != request.CandidateImage {
			return transitionrunner.EffectResult{}, fmt.Errorf("running image %q does not match admitted candidate %q", running, request.CandidateImage)
		}
		phaseEvidence.RuntimeImage = running
	}
	if err := u.options.Operations.Validate(ctx, op.Fence); err != nil {
		return transitionrunner.EffectResult{}, fmt.Errorf("transition target fence was lost after %s: %w", request.Phase, err)
	}
	result, err := json.Marshal(phaseEvidence)
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	return transitionrunner.EffectResult{TargetIdentityDigest: evidence.TargetIdentityDigest, CandidateArtifactDigest: candidateDigest, Payload: result}, nil
}

type upgradePhaseEvidence struct {
	PredecessorImage     string `json:"predecessorImage"`
	CandidateImage       string `json:"candidateImage"`
	TargetIdentityDigest string `json:"targetIdentityDigest"`
	PreflightDigest      string `json:"preflightDigest"`
	Generation           string `json:"generation"`
	RuntimeImage         string `json:"runtimeImage,omitempty"`
}
