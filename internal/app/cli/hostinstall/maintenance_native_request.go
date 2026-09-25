package hostinstall

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
)

// NativeRequest is the private handoff from the existing qualified-image
// workflow to the single-host maintenance provider. The workflow authenticates to pinned SSH and
// must perform LIVE OCI admission before writing this file. This is not a
// substitute for a generic release-transition owner record, and is never
// published into those authorities. It binds the operator-selected installation profile.
type NativeRequest struct {
	Profile             MaintenanceProfile `json:"profile"`
	DeploymentRunID     string             `json:"deploymentRunId"`
	DeploymentAttempt   string             `json:"deploymentAttempt"`
	Version             int                `json:"version"`
	PredecessorImage    string             `json:"predecessorImage"`
	PredecessorRevision string             `json:"predecessorRevision"`
	CandidateImage      string             `json:"candidateImage"`
	CandidateRevision   string             `json:"candidateRevision"`
	Qualification       struct {
		Image      string `json:"image"`
		Revision   string `json:"revision"`
		RunID      string `json:"runId"`
		RunAttempt string `json:"runAttempt"`
		Qualified  bool   `json:"qualified"`
	} `json:"qualification"`
	Admission json.RawMessage `json:"admission"`
	Plan      struct {
		SourceBefore                 SourceCompatibility `json:"sourceBefore"`
		SourceAfter                  SourceCompatibility `json:"sourceAfter"`
		RolePolicyChanged            bool                `json:"rolePolicyChanged"`
		Mode                         string              `json:"mode"`
		CurrentSchema                int                 `json:"currentSchema"`
		CandidateSchema              int                 `json:"candidateSchema"`
		PendingMigrations            []string            `json:"pendingMigrations"`
		PendingMigrationDigests      map[string]string   `json:"pendingMigrationDigests"`
		CompatibilityChanges         []string            `json:"compatibilityChanges"`
		PredecessorRevision          string              `json:"predecessorRevision"`
		CandidateRevision            string              `json:"candidateRevision"`
		ImageOnlyEligible            bool                `json:"imageOnlyEligible"`
		MigrationExecutionAuthorized bool                `json:"migrationExecutionAuthorized"`
	} `json:"plan"`
}

var sourceRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var decimalPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

func ReadNativeRequest(path string) (NativeRequest, error) {
	raw, err := securefs.ReadPrivateFile(path)
	if err != nil {
		return NativeRequest{}, err
	}
	if len(raw) > 1<<20 {
		return NativeRequest{}, errors.New("upgrade request exceeds size bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request NativeRequest
	if err = decoder.Decode(&request); err != nil {
		return request, err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return request, errors.New("trailing upgrade request data")
	}
	_, err = request.Identity()
	return request, err
}
func (r NativeRequest) Identity() (Identity, error) {
	if !decimalPattern.MatchString(r.DeploymentRunID) || !decimalPattern.MatchString(r.DeploymentAttempt) || r.Version != 1 || !sourceRevisionPattern.MatchString(r.PredecessorRevision) || !sourceRevisionPattern.MatchString(r.CandidateRevision) {
		return Identity{}, ErrIdentity
	}
	q := r.Qualification
	if !q.Qualified || q.Image != r.CandidateImage || q.Revision != r.CandidateRevision || !decimalPattern.MatchString(q.RunID) || !decimalPattern.MatchString(q.RunAttempt) {
		return Identity{}, errors.New("exact image qualification receipt required")
	}
	p := r.Plan
	if err := r.Profile.Validate(); err != nil {
		return Identity{}, err
	}
	mode, pending, err := classifySources(p.SourceBefore, p.SourceAfter)
	if err != nil {
		return Identity{}, err
	}
	if mode != p.Mode || mode == "review-required" || !slices.Equal(pending, p.PendingMigrations) || p.CurrentSchema != p.SourceBefore.Schema || p.CandidateSchema != p.SourceAfter.Schema {
		return Identity{}, errors.New("source compatibility decision mismatch")
	}
	if (p.Mode != "database-upgrade-required" && p.Mode != "image-only") || p.CurrentSchema < 1 || p.CandidateSchema != int(migrations.CurrentRevision) || p.CurrentSchema > p.CandidateSchema || p.PredecessorRevision != r.PredecessorRevision || p.CandidateRevision != r.CandidateRevision || p.ImageOnlyEligible != (mode == "image-only") || p.MigrationExecutionAuthorized {
		return Identity{}, errors.New("unsupported host upgrade source boundary")
	}
	if len(p.CompatibilityChanges) > 0 {
		return Identity{}, fmt.Errorf("unsupported engine/storage compatibility changes: %v", p.CompatibilityChanges)
	}
	if err := validateCandidateMigrations(p.CurrentSchema, p.CandidateSchema, p.PendingMigrations, p.PendingMigrationDigests); err != nil {
		return Identity{}, err
	}
	var admission struct {
		SchemaVersion  int    `json:"schemaVersion"`
		Image          string `json:"image"`
		Digest         string `json:"digest"`
		RegistryDigest string `json:"registryDigest"`
		Attestation    struct {
			Verified       bool   `json:"verified"`
			Repository     string `json:"repository"`
			Workflow       string `json:"workflow"`
			SourceRevision string `json:"sourceRevision"`
		} `json:"attestation"`
		SBOM struct {
			Discoverable  bool   `json:"discoverable"`
			PredicateType string `json:"predicateType"`
		} `json:"sbom"`
		Vulnerability struct {
			Passed   bool   `json:"passed"`
			SHA256   string `json:"sha256"`
			Scanner  string `json:"scanner"`
			Platform string `json:"platform"`
		} `json:"vulnerabilityPolicy"`
	}
	if err := json.Unmarshal(r.Admission, &admission); err != nil {
		return Identity{}, err
	}
	imageDigest := strings.TrimPrefix(r.CandidateImage, "ghcr.io/flidai/leapview@")
	if admission.SchemaVersion != 1 || admission.Image != r.CandidateImage || admission.Digest != imageDigest || admission.RegistryDigest != imageDigest || !admission.Attestation.Verified || admission.Attestation.Repository != "flidai/leapview" || admission.Attestation.Workflow != "flidai/leapview/.github/workflows/artifacts.yml" || admission.Attestation.SourceRevision != r.CandidateRevision || !admission.SBOM.Discoverable || admission.SBOM.PredicateType != "https://spdx.dev/Document/v2.3" || !admission.Vulnerability.Passed || admission.Vulnerability.Scanner != "trivy" || (admission.Vulnerability.Platform != "" && admission.Vulnerability.Platform != "linux/amd64") {
		return Identity{}, errors.New("OCI admission does not bind the qualified candidate")
	}
	if len(admission.Vulnerability.SHA256) != 64 {
		return Identity{}, errors.New("missing vulnerability policy identity")
	}
	if _, err := hex.DecodeString(admission.Vulnerability.SHA256); err != nil {
		return Identity{}, err
	}
	// Native provider identity binds BOTH workflow receipts. It is deliberately
	// domain-separated from the generic release authority's admission digest.
	canonical, err := json.Marshal(r)
	if err != nil {
		return Identity{}, err
	}
	digest := sha256.Sum256(append([]byte("leapview/host-maintenance-request/v1\n"), canonical...))
	id := Identity{Target: r.Profile.ID, Predecessor: r.PredecessorImage, Candidate: r.CandidateImage, ArtifactAdmissionDigest: "sha256:" + hex.EncodeToString(digest[:])}
	if !strings.HasPrefix(id.Predecessor, "ghcr.io/flidai/leapview@") || !strings.HasPrefix(id.Candidate, "ghcr.io/flidai/leapview@") {
		return Identity{}, ErrIdentity
	}
	return id, id.validate()
}

func validateCandidateMigrations(current, target int, names []string, digests map[string]string) error {
	if len(names) != target-current || len(digests) != len(names) {
		return errors.New("incomplete pending migration manifest")
	}
	for i, name := range names {
		prefix, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || version != current+i+1 || strings.ContainsAny(name, "/\\") {
			return errors.New("invalid pending migration chain")
		}
		raw, err := fs.ReadFile(migrations.MigrationFS(), name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != digests[name] {
			return errors.New("candidate embedded SQL differs from admitted source")
		}
		if strings.Contains(string(raw), "-- +goose NO TRANSACTION") {
			return errors.New("nontransactional migration requires separate qualification")
		}
	}
	return nil
}
