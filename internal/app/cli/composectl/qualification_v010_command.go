package composectl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/flidai/leapview/internal/platform/compatibility"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

const v010QualificationEvidenceName = "v0.1-preservation-qualification.json"

type QualificationV010Options struct {
	CandidateAdmission  string
	TransitionPolicy    string
	PolicySHA256        string
	PredecessorEvidence string
	EvidenceDir         string
}

type QualificationV010ArtifactReviewOptions struct {
	TransitionPolicy string
	PolicySHA256     string
	Evidence         string
}

// ReviewV010Artifact publishes the compatibility owner's existing Phase 1
// evidence contract for the exact authenticated v0.1 artifact. Release
// qualification supplies this document to the complete preservation journey.
func (c *Controller) ReviewV010Artifact(ctx context.Context, options QualificationV010ArtifactReviewOptions) error {
	if ctx == nil {
		return fmt.Errorf("v0.1 artifact review context is required")
	}
	if strings.TrimSpace(options.Evidence) == "" {
		return fmt.Errorf("v0.1 artifact review evidence destination is required")
	}
	destination, err := filepath.Abs(options.Evidence)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create v0.1 artifact review evidence directory: %w", err)
	}
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale v0.1 artifact review evidence: %w", err)
	}
	policyDocument, err := readV010QualificationPolicy(options.TransitionPolicy, options.PolicySHA256)
	if err != nil {
		return err
	}
	resolver := newDockerV010ArtifactResolver(c.root, c.dockerBin, c.qualificationExecutor)
	evidence, err := compatibility.VerifyReleasedV010Artifact(ctx, compatibility.V010ArtifactVerificationOptions{
		PolicyDocument: policyDocument,
		Resolver:       resolver,
		Now:            c.now,
	})
	if err != nil {
		return err
	}
	document, err := compatibility.MarshalV010ReleaseIdentityEvidence(evidence, policyDocument)
	if err != nil {
		return fmt.Errorf("owner-validate reviewed v0.1 artifact evidence: %w", err)
	}
	if err := securefs.WritePrivateFileAtomic(destination, document); err != nil {
		return fmt.Errorf("atomically publish reviewed v0.1 artifact evidence: %w", err)
	}
	published, err := os.ReadFile(destination)
	if err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("read published v0.1 artifact review evidence: %w", err)
	}
	validated, err := compatibility.ValidateV010ReleaseIdentityEvidence(published, policyDocument)
	if err != nil || !reflect.DeepEqual(validated, evidence) {
		_ = os.Remove(destination)
		if err != nil {
			return fmt.Errorf("validate published v0.1 artifact review evidence: %w", err)
		}
		return fmt.Errorf("published v0.1 artifact review evidence changed after owner validation")
	}
	_, err = fmt.Fprintf(c.stdout, "reviewed exact v0.1 artifact evidence: %s\n", destination)
	return err
}

// QualifyV010Preservation is the supported production entry point for the
// complete exact-v0.1 preservation and candidate fresh-install qualification.
func (c *Controller) QualifyV010Preservation(ctx context.Context, options QualificationV010Options) error {
	if ctx == nil {
		return fmt.Errorf("v0.1 qualification context is required")
	}
	for label, value := range map[string]string{
		"candidate admission":                options.CandidateAdmission,
		"candidate-bound transition policy":  options.TransitionPolicy,
		"expected transition policy SHA-256": options.PolicySHA256,
		"reviewed v0.1 predecessor evidence": options.PredecessorEvidence,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", label)
		}
	}
	evidenceDir := strings.TrimSpace(options.EvidenceDir)
	if evidenceDir == "" {
		evidenceDir = c.path("qualification-evidence")
	}
	evidenceDir, err := filepath.Abs(evidenceDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
		return fmt.Errorf("create v0.1 qualification evidence directory: %w", err)
	}
	destination := filepath.Join(evidenceDir, v010QualificationEvidenceName)
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale v0.1 qualification evidence: %w", err)
	}
	policyDocument, err := readV010QualificationPolicy(options.TransitionPolicy, options.PolicySHA256)
	if err != nil {
		return err
	}
	policy, err := compatibility.ParsePolicy(policyDocument)
	if err != nil {
		return fmt.Errorf("validate candidate-bound transition policy: %w", err)
	}
	candidateRelease, ok := policy.ReleaseByID(policy.CandidateRelease)
	if !ok {
		return fmt.Errorf("candidate-bound transition policy omits candidate release %q", policy.CandidateRelease)
	}
	candidate := candidateRelease.IdentityForPlatform(compatibility.ReleasedV010Platform)
	admissionDocument, err := os.ReadFile(options.CandidateAdmission)
	if err != nil {
		return fmt.Errorf("read candidate admission evidence: %w", err)
	}
	if _, err := compatibility.ValidateCandidateAdmissionEvidence(admissionDocument, candidate); err != nil {
		return err
	}
	predecessorDocument, err := os.ReadFile(options.PredecessorEvidence)
	if err != nil {
		return fmt.Errorf("read reviewed v0.1 predecessor evidence: %w", err)
	}
	reviewed, err := compatibility.ValidateV010ReleaseIdentityEvidence(predecessorDocument, policyDocument)
	if err != nil {
		return fmt.Errorf("validate reviewed v0.1 predecessor evidence: %w", err)
	}

	completed, err := c.qualifyV010ArtifactExecution(ctx, policyDocument)
	if err != nil {
		return err
	}
	if completed.Identity != reviewed.Identity || completed.Artifact != reviewed.Artifact ||
		completed.Provenance != reviewed.Provenance || completed.PolicyVersion != reviewed.PolicyVersion ||
		completed.PolicySHA256 != reviewed.PolicySHA256 {
		return fmt.Errorf("observed v0.1 artifact does not match reviewed predecessor evidence")
	}
	if completed.Execution == nil || completed.Execution.Preservation == nil || completed.Execution.FreshCandidate == nil {
		return fmt.Errorf("completed v0.1 qualification evidence is incomplete")
	}
	document, err := compatibility.MarshalV010ReleaseIdentityEvidence(completed, policyDocument)
	if err != nil {
		return fmt.Errorf("owner-validate completed v0.1 qualification evidence: %w", err)
	}
	if err := securefs.WritePrivateFileAtomic(destination, document); err != nil {
		return fmt.Errorf("atomically publish v0.1 qualification evidence: %w", err)
	}
	published, err := os.ReadFile(destination)
	if err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("read published v0.1 qualification evidence: %w", err)
	}
	validated, err := compatibility.ValidateV010ReleaseIdentityEvidence(published, policyDocument)
	if err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("validate published v0.1 qualification evidence: %w", err)
	}
	if !reflect.DeepEqual(validated, completed) {
		_ = os.Remove(destination)
		return fmt.Errorf("published v0.1 qualification evidence changed after owner validation")
	}
	_, err = fmt.Fprintf(c.stdout, "v0.1 preservation and fresh-install qualification passed; evidence: %s\n", destination)
	return err
}

func readV010QualificationPolicy(path, expectedSHA256 string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("candidate-bound transition policy is required")
	}
	if strings.TrimSpace(expectedSHA256) == "" {
		return nil, fmt.Errorf("expected transition policy SHA-256 is required")
	}
	document, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read candidate-bound transition policy: %w", err)
	}
	digest := sha256.Sum256(document)
	actual := hex.EncodeToString(digest[:])
	if strings.TrimSpace(expectedSHA256) != actual {
		return nil, fmt.Errorf("candidate-bound transition policy digest mismatch: got %s", actual)
	}
	return document, nil
}
