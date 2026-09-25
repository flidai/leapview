package demoupgrade

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

// NativeRequest is the private handoff from the existing qualified-image
// workflow to the demo provider. The workflow authenticates to pinned SSH and
// must perform LIVE OCI admission before writing this file. This is not a
// substitute for a generic release-transition owner record, and is never
// published into those authorities. It authorizes only the bounded demo profile.
type NativeRequest struct {
	DeploymentRunID     string `json:"deploymentRunId"`
	DeploymentAttempt   string `json:"deploymentAttempt"`
	Version             int    `json:"version"`
	PredecessorImage    string `json:"predecessorImage"`
	PredecessorRevision string `json:"predecessorRevision"`
	CandidateImage      string `json:"candidateImage"`
	CandidateRevision   string `json:"candidateRevision"`
	Qualification       struct {
		Image      string `json:"image"`
		Revision   string `json:"revision"`
		RunID      string `json:"runId"`
		RunAttempt string `json:"runAttempt"`
		Qualified  bool   `json:"qualified"`
	} `json:"qualification"`
	Admission json.RawMessage `json:"admission"`
	Plan      struct {
		Mode                         string            `json:"mode"`
		CurrentSchema                int               `json:"currentSchema"`
		CandidateSchema              int               `json:"candidateSchema"`
		PendingMigrations            []string          `json:"pendingMigrations"`
		PendingMigrationDigests      map[string]string `json:"pendingMigrationDigests"`
		ChangedCompatibilityPaths    []string          `json:"changedCompatibilityPaths"`
		PredecessorRevision          string            `json:"predecessorRevision"`
		CandidateRevision            string            `json:"candidateRevision"`
		ImageOnlyEligible            bool              `json:"imageOnlyEligible"`
		MigrationExecutionAuthorized bool              `json:"migrationExecutionAuthorized"`
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
	if p.Mode != "database-upgrade-required" || p.CurrentSchema != 28 || p.CandidateSchema != 30 || p.PredecessorRevision != r.PredecessorRevision || p.CandidateRevision != r.CandidateRevision || p.ImageOnlyEligible || p.MigrationExecutionAuthorized {
		return Identity{}, errors.New("unsupported demo upgrade source boundary")
	}
	if strings.Join(p.PendingMigrations, ",") != "029_agent_configuration.sql,030_browser_session_client_label.sql" {
		return Identity{}, errors.New("unexpected pending migrations")
	}
	if len(p.PendingMigrationDigests) != 2 ||
		p.PendingMigrationDigests["029_agent_configuration.sql"] != "55d04d342de0391ff743915867f2265e2309d6837b36d9833e6396fcec7d1a47" ||
		p.PendingMigrationDigests["030_browser_session_client_label.sql"] != "cd721999bae6b681f358f7730f683877b571f0da43af66021e4b63279470ee26" {
		return Identity{}, errors.New("candidate SQL differs from the reviewed 28 to 30 transition")
	}
	// No runtime/extension/dependency upgrade is admitted incidentally. Changes
	// under postgres are the candidate's tested embedded migrator and immutable
	// SQL set; the runner separately rejects rewritten historical SQL.
	for _, path := range p.ChangedCompatibilityPaths {
		if strings.HasPrefix(path, "internal/platform/postgres/") {
			continue
		}
		if strings.HasSuffix(path, "_test.go") && (strings.HasPrefix(path, "internal/analytics/duckdb/") || strings.HasPrefix(path, "internal/analytics/ducklake/")) {
			continue
		}
		return Identity{}, fmt.Errorf("engine/dependency change requires separate qualification: %s", path)
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
	digest := sha256.Sum256(append([]byte("leapview/demo-upgrade-request/v1\n"), canonical...))
	id := Identity{Target: "app-leapview-demo-02", Predecessor: r.PredecessorImage, Candidate: r.CandidateImage, ArtifactAdmissionDigest: "sha256:" + hex.EncodeToString(digest[:])}
	if !strings.HasPrefix(id.Predecessor, "ghcr.io/flidai/leapview@") || !strings.HasPrefix(id.Candidate, "ghcr.io/flidai/leapview@") {
		return Identity{}, ErrIdentity
	}
	return id, id.validate()
}
