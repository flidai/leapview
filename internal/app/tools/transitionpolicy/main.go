package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
)

func main() {
	bindRelease := flag.String("bind-release", "", "release-identity JSON containing the admitted candidate image")
	candidateAdmission := flag.String("candidate-admission", "", "OCI admission record for the candidate image")
	bindOutput := flag.String("bind-output", "", "write the candidate-bound release-transition policy")
	predecessorEvidenceOutput := flag.String("predecessor-evidence-output", "", "write exact predecessor registry verification evidence")
	flag.Parse()
	if err := bind(*bindRelease, *candidateAdmission, *bindOutput, *predecessorEvidenceOutput); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type predecessorVerification struct {
	SchemaVersion   int                           `json:"schemaVersion"`
	PolicyVersion   string                        `json:"policyVersion"`
	TemplateVersion string                        `json:"templateVersion"`
	Predecessors    []predecessorVerificationItem `json:"predecessors"`
}

type predecessorVerificationItem struct {
	ReleaseID      string `json:"releaseId"`
	Version        string `json:"version"`
	SourceRevision string `json:"sourceRevision"`
	Distribution   string `json:"distribution"`
	Platform       string `json:"platform"`
	Image          string `json:"image"`
	ManifestSHA256 string `json:"manifestSha256"`
}

type predecessorResolver func(string) (string, error)

func bind(releasePath, admissionPath, outputPath, predecessorEvidencePath string) error {
	return bindWithResolver(releasePath, admissionPath, outputPath, predecessorEvidencePath, resolvePredecessorManifest)
}

func bindWithResolver(releasePath, admissionPath, outputPath, predecessorEvidencePath string, resolve predecessorResolver) error {
	if strings.TrimSpace(releasePath) == "" || strings.TrimSpace(admissionPath) == "" ||
		strings.TrimSpace(outputPath) == "" || strings.TrimSpace(predecessorEvidencePath) == "" {
		return fmt.Errorf("--bind-release, --candidate-admission, --bind-output, and --predecessor-evidence-output are required together")
	}
	var release struct {
		Version     string `json:"version"`
		Revision    string `json:"revision"`
		BuildTime   string `json:"buildTime"`
		Dirty       bool   `json:"dirty"`
		Development bool   `json:"development"`
		Image       string `json:"image"`
	}
	contents, err := os.ReadFile(releasePath)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&release); err != nil {
		return fmt.Errorf("decode admitted release identity: %w", err)
	}
	if release.Dirty || release.Development {
		return fmt.Errorf("candidate-bound policy requires a clean released identity")
	}
	if err := validateCandidateAdmission(admissionPath, release.Image, release.Revision); err != nil {
		return err
	}
	base, err := compatibility.EmbeddedPolicy()
	if err != nil {
		return err
	}
	template, err := compatibility.EmbeddedCandidateTransitionTemplate()
	if err != nil {
		return err
	}
	predecessorEvidence, err := verifyPredecessors(base, template, resolve)
	if err != nil {
		return err
	}
	bound, err := base.BindCandidate(compatibility.ReleaseIdentity{
		Version: release.Version, SourceRevision: release.Revision,
		Image: release.Image, Distribution: "public",
	}, []string{"linux/amd64", "linux/arm64"})
	if err != nil {
		return err
	}
	encoded, err := compatibility.MarshalPolicy(bound)
	if err != nil {
		return err
	}
	if err := writeJSON(predecessorEvidencePath, predecessorEvidence); err != nil {
		return err
	}
	return os.WriteFile(outputPath, encoded, 0o600)
}

func validateCandidateAdmission(path, image, revision string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read candidate admission: %w", err)
	}
	_, err = compatibility.ValidateCandidateAdmissionEvidence(contents, compatibility.ReleaseIdentity{
		Image: image, SourceRevision: revision,
	})
	return err
}

func verifyPredecessors(policy *compatibility.Policy, template compatibility.CandidateTransitionTemplate, resolve predecessorResolver) (predecessorVerification, error) {
	if resolve == nil {
		return predecessorVerification{}, fmt.Errorf("predecessor resolver is required")
	}
	predecessor, ok := policy.ReleaseByID(template.PredecessorRelease)
	if !ok {
		return predecessorVerification{}, fmt.Errorf("candidate transition predecessor %q is absent from the base policy", template.PredecessorRelease)
	}
	evidence := predecessorVerification{
		SchemaVersion: 1, PolicyVersion: policy.PolicyVersion, TemplateVersion: template.TemplateVersion,
		Predecessors: []predecessorVerificationItem{},
	}
	manifests := make(map[string]string)
	for _, platform := range template.Platforms {
		identity := predecessor.IdentityForPlatform(platform)
		if strings.TrimSpace(identity.Image) == "" {
			return predecessorVerification{}, fmt.Errorf("predecessor %q has no image for %s", predecessor.ID, platform)
		}
		manifestSHA, ok := manifests[identity.Image]
		if !ok {
			resolvedDigest, err := resolve(identity.Image)
			if err != nil {
				return predecessorVerification{}, fmt.Errorf("resolve predecessor %s for %s: %w", identity.Image, platform, err)
			}
			manifestSHA = strings.TrimPrefix(strings.TrimSpace(resolvedDigest), "sha256:")
			want := strings.TrimPrefix(identity.Image[strings.LastIndex(identity.Image, "@"):], "@sha256:")
			if len(manifestSHA) != 64 || manifestSHA != want {
				return predecessorVerification{}, fmt.Errorf("predecessor %s registry manifest digest %s does not match immutable identity", identity.Image, manifestSHA)
			}
			manifests[identity.Image] = manifestSHA
		}
		evidence.Predecessors = append(evidence.Predecessors, predecessorVerificationItem{
			ReleaseID: predecessor.ID, Version: predecessor.Version, SourceRevision: predecessor.SourceRevision,
			Distribution: predecessor.Distribution, Platform: platform, Image: identity.Image, ManifestSHA256: manifestSHA,
		})
	}
	if len(evidence.Predecessors) != len(template.Platforms) {
		return predecessorVerification{}, fmt.Errorf("not every predecessor platform was verified")
	}
	return evidence, nil
}

func resolvePredecessorManifest(image string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "buildx", "imagetools", "inspect", "--raw", image)
	manifest, err := command.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(manifest)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func writeJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(bytes.TrimSpace(encoded), '\n')
	return os.WriteFile(path, encoded, 0o600)
}
