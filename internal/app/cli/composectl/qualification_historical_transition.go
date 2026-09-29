package composectl

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/releasecontract"
)

const (
	qualificationHistoricalTransitionRequiredEnv  = "LEAPVIEW_HISTORICAL_TRANSITION_REQUIRED"
	qualificationHistoricalTransitionImageEnv     = "LEAPVIEW_HISTORICAL_TRANSITION_CANDIDATE_IMAGE"
	qualificationHistoricalTransitionRevisionEnv  = "LEAPVIEW_HISTORICAL_TRANSITION_CANDIDATE_REVISION"
	qualificationHistoricalTransitionEvidenceEnv  = "LEAPVIEW_HISTORICAL_TRANSITION_EVIDENCE"
	qualificationHistoricalTransitionFinalEnv     = "LEAPVIEW_HISTORICAL_TRANSITION_FINAL_ARTIFACT"
	qualificationHistoricalTransitionAdmissionEnv = "LEAPVIEW_HISTORICAL_TRANSITION_ADMISSION"
	qualificationHistoricalTransitionContract     = "compose-postgres-local/v1"
	qualificationHistoricalPredecessorImage       = "ghcr.io/flidai/leapview@sha256:b8d384dd8c137c4aa008fce0d2047f29ee915b82515d4d56c1d3f3f84735327f"
	qualificationHistoricalPredecessorRevision    = "28bfc7e7e8f8074847229c05c42f0bbc2dd79336"
	qualificationHistoricalPredecessorSchema      = 32
)

var (
	qualificationHistoricalRevisionPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	qualificationHistoricalSHA256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	qualificationHistoricalOCIImagePattern   = regexp.MustCompile(`^ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}$`)
	qualificationHistoricalLocalImagePattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// qualificationHistoricalTransitionOptions are bound to an explicit immutable
// predecessor. Candidate identity is supplied by the calling lane so pre-merge
// runs may use a local image while final-artifact runs require its OCI digest.
type qualificationHistoricalTransitionOptions struct {
	CandidateImage          string
	CandidateRevision       string
	AdmissionSourceRevision string
	EvidencePath            string
	AdmissionPath           string
	FinalArtifact           bool
}

type qualificationHistoricalTransitionIdentity struct {
	Image             string `json:"image"`
	Revision          string `json:"revision"`
	Schema            int    `json:"schema"`
	PermissionProfile string `json:"permissionProfile"`
}

type qualificationHistoricalTransitionChecks struct {
	LegacyPublication       bool `json:"legacyPublication"`
	LegacyViewer            bool `json:"legacyViewer"`
	TypedPolicyCaptured     bool `json:"typedPolicyCaptured"`
	IndependentApproval     bool `json:"independentApproval"`
	PublisherNoSelfApproval bool `json:"publisherNoSelfApproval"`
	ViewerLeastPrivilege    bool `json:"viewerLeastPrivilege"`
	RealPublicationAdapter  bool `json:"realPublicationAdapter"`
	SubsequentDeploy        bool `json:"subsequentDeploy"`
}

type qualificationHistoricalTransitionReceipt struct {
	Version           int                                       `json:"version"`
	Status            string                                    `json:"status"`
	Predecessor       qualificationHistoricalTransitionIdentity `json:"predecessor"`
	Candidate         qualificationHistoricalTransitionIdentity `json:"candidate"`
	ValidatorRevision string                                    `json:"validatorRevision"`
	Contract          string                                    `json:"contract"`
	Checks            qualificationHistoricalTransitionChecks   `json:"checks"`
}

func qualificationHistoricalTransitionRequired(environ map[string]string) bool {
	return strings.TrimSpace(environ[qualificationHistoricalTransitionRequiredEnv]) == "1"
}

func readQualificationHistoricalTransitionOptions(environ map[string]string) (qualificationHistoricalTransitionOptions, error) {
	if !qualificationHistoricalTransitionRequired(environ) {
		return qualificationHistoricalTransitionOptions{}, errors.New("historical transition qualification is not required")
	}
	options := qualificationHistoricalTransitionOptions{
		CandidateImage:          strings.TrimSpace(environ[qualificationHistoricalTransitionImageEnv]),
		CandidateRevision:       strings.TrimSpace(environ[qualificationHistoricalTransitionRevisionEnv]),
		AdmissionSourceRevision: strings.TrimSpace(environ["GITHUB_SHA"]),
		EvidencePath:            strings.TrimSpace(environ[qualificationHistoricalTransitionEvidenceEnv]),
		AdmissionPath:           strings.TrimSpace(environ[qualificationHistoricalTransitionAdmissionEnv]),
	}
	finalMarker := strings.TrimSpace(environ[qualificationHistoricalTransitionFinalEnv])
	if finalMarker != "" && finalMarker != "0" && finalMarker != "1" {
		return qualificationHistoricalTransitionOptions{}, errors.New("historical transition final-artifact marker must be 0 or 1")
	}
	options.FinalArtifact = finalMarker == "1"
	if options.CandidateImage == "" || (!qualificationHistoricalLocalImagePattern.MatchString(options.CandidateImage) && !qualificationHistoricalOCIImagePattern.MatchString(options.CandidateImage)) {
		return qualificationHistoricalTransitionOptions{}, errors.New("historical transition qualification requires a valid candidate image")
	}
	if !qualificationHistoricalRevisionPattern.MatchString(options.CandidateRevision) {
		return qualificationHistoricalTransitionOptions{}, errors.New("historical transition qualification requires a full candidate source revision")
	}
	if options.FinalArtifact && !qualificationHistoricalOCIImagePattern.MatchString(options.CandidateImage) {
		return qualificationHistoricalTransitionOptions{}, errors.New("final historical transition qualification requires an immutable candidate OCI digest")
	}
	if options.FinalArtifact && !filepath.IsAbs(options.AdmissionPath) {
		return qualificationHistoricalTransitionOptions{}, errors.New("final historical transition qualification requires absolute OCI admission evidence")
	}
	if options.FinalArtifact && !qualificationHistoricalRevisionPattern.MatchString(options.AdmissionSourceRevision) {
		return qualificationHistoricalTransitionOptions{}, errors.New("final historical transition qualification requires the protected workflow attestation source revision")
	}
	if !filepath.IsAbs(options.EvidencePath) {
		return qualificationHistoricalTransitionOptions{}, errors.New("historical transition qualification requires an absolute evidence path")
	}
	evidencePath := filepath.Clean(options.EvidencePath)
	var err error
	if err := os.MkdirAll(filepath.Dir(evidencePath), 0o700); err != nil {
		return qualificationHistoricalTransitionOptions{}, fmt.Errorf("create private historical transition evidence directory: %w", err)
	}
	directory, err := os.Lstat(filepath.Dir(evidencePath))
	if err != nil || directory.Mode()&os.ModeSymlink != 0 || !directory.IsDir() || directory.Mode().Perm()&0o077 != 0 {
		return qualificationHistoricalTransitionOptions{}, errors.New("historical transition evidence directory must be a private, non-symlink directory")
	}
	if info, err := os.Lstat(evidencePath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return qualificationHistoricalTransitionOptions{}, errors.New("historical transition evidence path must be a new regular file")
		}
		return qualificationHistoricalTransitionOptions{}, errors.New("historical transition evidence path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return qualificationHistoricalTransitionOptions{}, fmt.Errorf("inspect historical transition evidence path: %w", err)
	}
	options.EvidencePath = evidencePath
	return options, nil
}

func writeQualificationHistoricalTransitionReceipt(path, validatorRevision string, candidateImage, candidateRevision string, checks qualificationHistoricalTransitionChecks) error {
	if strings.TrimSpace(path) == "" || !qualificationHistoricalRevisionPattern.MatchString(validatorRevision) ||
		!qualificationHistoricalRevisionPattern.MatchString(candidateRevision) ||
		validatorRevision != candidateRevision ||
		(!qualificationHistoricalLocalImagePattern.MatchString(candidateImage) && !qualificationHistoricalOCIImagePattern.MatchString(candidateImage)) {
		return errors.New("historical transition receipt must bind the clean candidate validator revision")
	}
	if !qualificationHistoricalTransitionChecksPassed(checks) {
		return errors.New("historical transition receipt requires every supported-transition check to pass")
	}
	receipt := qualificationHistoricalTransitionReceipt{
		Version: 1, Status: "passed",
		Predecessor: qualificationHistoricalTransitionIdentity{
			Image: qualificationHistoricalPredecessorImage, Revision: qualificationHistoricalPredecessorRevision,
			Schema: qualificationHistoricalPredecessorSchema, PermissionProfile: releasecontract.LegacyPermissions,
		},
		Candidate: qualificationHistoricalTransitionIdentity{
			Image: candidateImage, Revision: candidateRevision,
			Schema: int(migrations.CurrentRevision), PermissionProfile: releasecontract.TypedPermissions,
		},
		ValidatorRevision: validatorRevision,
		Contract:          qualificationHistoricalTransitionContract,
		Checks:            checks,
	}
	encoded, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("encode historical transition receipt: %w", err)
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create historical transition receipt: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write historical transition receipt: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("sync historical transition receipt: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close historical transition receipt: %w", err)
	}
	return nil
}

func qualificationHistoricalTransitionChecksPassed(checks qualificationHistoricalTransitionChecks) bool {
	return checks.LegacyPublication && checks.LegacyViewer && checks.TypedPolicyCaptured &&
		checks.IndependentApproval && checks.PublisherNoSelfApproval && checks.ViewerLeastPrivilege &&
		checks.RealPublicationAdapter && checks.SubsequentDeploy
}
