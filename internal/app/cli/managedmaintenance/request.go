// Package managedmaintenance owns the compatible-image maintenance handoff.
// It never migrates or restores durable customer state.
package managedmaintenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"time"

	"github.com/flidai/leapview/internal/platform/ociref"
	"github.com/flidai/leapview/internal/platform/releasecontract"
)

type Release struct {
	ArtifactAdmissionDigest string `json:"artifactAdmissionDigest"`
	Image                   string `json:"image"`
	Revision                string `json:"revision"`
	ConfigurationDigest     string `json:"configurationDigest"`
	CredentialDigest        string `json:"credentialDigest"`
}
type Budgets struct {
	Phase time.Duration `json:"phase"`
	Total time.Duration `json:"total"`
}
type Request struct {
	Version      int                                 `json:"version"`
	Operation    string                              `json:"operation,omitempty"`
	Target       string                              `json:"target"`
	Predecessor  Release                             `json:"predecessor"`
	Candidate    Release                             `json:"candidate"`
	SourceBefore releasecontract.SourceCompatibility `json:"sourceBefore"`
	SourceAfter  releasecontract.SourceCompatibility `json:"sourceAfter"`
	Budgets      Budgets                             `json:"budgets"`
}

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var targetPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func (r Request) Validate() error {
	if r.Version != 1 || !targetPattern.MatchString(r.Target) {
		return errors.New("invalid managed maintenance target or version")
	}
	if r.Budgets.Phase <= 0 || r.Budgets.Total < r.Budgets.Phase || r.Budgets.Total > 24*time.Hour {
		return errors.New("finite phase and total maintenance budgets are required")
	}
	if r.Operation != "" && r.Operation != "enroll" {
		return errors.New("unsupported managed maintenance operation")
	}
	for _, release := range []Release{r.Predecessor, r.Candidate} {
		if _, err := ociref.ParseImmutable(release.Image); err != nil {
			return err
		}
		if !digestPattern.MatchString(release.ArtifactAdmissionDigest) || !revisionPattern.MatchString(release.Revision) || !digestPattern.MatchString(release.ConfigurationDigest) || !digestPattern.MatchString(release.CredentialDigest) {
			return errors.New("immutable release configuration and credential identities are required")
		}
	}
	if r.Operation == "enroll" {
		// Enrollment selects one existing, authenticated release. Both sides are
		// identical so interrupted enrollment can only recover that same release.
		if r.Predecessor != r.Candidate || !reflect.DeepEqual(r.SourceBefore, r.SourceAfter) {
			return errors.New("managed enrollment requires one exact release and source identity")
		}
	} else if r.Predecessor.Image == r.Candidate.Image {
		return errors.New("candidate and predecessor must be distinct immutable images")
	}
	if r.Predecessor.ConfigurationDigest != r.Candidate.ConfigurationDigest || r.Predecessor.CredentialDigest != r.Candidate.CredentialDigest {
		return errors.New("configuration or credential transition requires separately reviewed maintenance")
	}
	mode, pending, err := releasecontract.ClassifySources(r.SourceBefore, r.SourceAfter)
	if err != nil {
		return err
	}
	if mode != "image-only" || len(pending) != 0 {
		return errors.New("managed image handoff requires compatible source; migration or recovery must be separately admitted")
	}
	return nil
}
func (r Request) Digest() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
