package providerrestore

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/platform/ociref"
	"github.com/flidai/leapview/internal/recoveryset"
)

const (
	HandoffSchemaVersion = 1
	HandoffKind          = "leapview/fai981-replacement-handoff"
	HandoffAvailable     = "available"
)

var sourceRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type ProviderEndpoint struct {
	Role                string `json:"role"`
	Provider            string `json:"provider"`
	ResourceID          string `json:"resourceId"`
	Endpoint            string `json:"endpoint"`
	Database            string `json:"database,omitempty"`
	Region              string `json:"region,omitempty"`
	Bucket              string `json:"bucket,omitempty"`
	CredentialSecretKey string `json:"credentialSecretKey"`
}

type SecretBundleReference struct {
	Provider string   `json:"provider"`
	URI      string   `json:"uri"`
	SHA256   string   `json:"sha256"`
	Version  string   `json:"version"`
	Keys     []string `json:"keys"`
}

type ReplacementHandoff struct {
	SchemaVersion  int                           `json:"schemaVersion"`
	Kind           string                        `json:"kind"`
	Status         string                        `json:"status"`
	RecoverySetID  string                        `json:"recoverySetId"`
	FrontierDigest string                        `json:"frontierDigest"`
	TargetID       string                        `json:"targetId"`
	Artifact       compatibility.ReleaseIdentity `json:"artifact"`
	Providers      []ProviderEndpoint            `json:"providers"`
	Secrets        SecretBundleReference         `json:"secrets"`
	AvailableAt    time.Time                     `json:"availableAt"`
}

type HandoffRequest struct {
	Set              recoveryset.RecoverySet
	ArtifactIdentity string
	Databases        []DatabaseResult
	Objects          []ObjectResult
}

type HandoffExpectations struct {
	OccurrenceID     string
	TargetID         string
	RecoverySetID    string
	FrontierDigest   string
	ArtifactIdentity string
}

type HandoffProvider interface {
	CreateHandoff(context.Context, HandoffRequest) (ReplacementHandoff, error)
}

func (handoff ReplacementHandoff) Validate(expectedSet recoveryset.RecoverySet, artifactIdentity string) error {
	if handoff.SchemaVersion != HandoffSchemaVersion || handoff.Kind != HandoffKind || handoff.Status != HandoffAvailable || handoff.RecoverySetID != expectedSet.ID || handoff.FrontierDigest != expectedSet.FrontierDigest || handoff.TargetID != expectedSet.Delivery.TargetID || handoff.AvailableAt.IsZero() {
		return fmt.Errorf("%w: replacement handoff identity is incomplete", ErrInconsistent)
	}
	if handoff.Artifact.Image != artifactIdentity || ociref.ValidateImmutable(handoff.Artifact.Image) != nil || placeholderOCIIdentity(handoff.Artifact.Image) || !sourceRevisionPattern.MatchString(handoff.Artifact.SourceRevision) || strings.Trim(handoff.Artifact.SourceRevision, string(handoff.Artifact.SourceRevision[0])) == "" || strings.TrimSpace(handoff.Artifact.Version) == "" || handoff.Artifact.Distribution != "oci" || handoff.Artifact.Platform != "linux/amd64" {
		return fmt.Errorf("%w: replacement handoff artifact is not an exact runnable release", ErrInconsistent)
	}
	roles := make([]string, 0, len(handoff.Providers))
	for _, endpoint := range handoff.Providers {
		if err := validateProviderEndpoint(endpoint); err != nil {
			return err
		}
		roles = append(roles, endpoint.Role)
	}
	slices.Sort(roles)
	if !slices.Equal(roles, []string{"control", "ducklake", "objects"}) {
		return fmt.Errorf("%w: replacement handoff must contain control, ducklake, and object endpoints", ErrInconsistent)
	}
	if err := validateSecretBundleReference(handoff.Secrets); err != nil {
		return err
	}
	for _, endpoint := range handoff.Providers {
		if !slices.Contains(handoff.Secrets.Keys, endpoint.CredentialSecretKey) {
			return fmt.Errorf("%w: provider credential reference is absent from the secret bundle", ErrInconsistent)
		}
	}
	return nil
}

func placeholderOCIIdentity(value string) bool {
	_, digest, ok := strings.Cut(value, "@sha256:")
	return ok && len(digest) == 64 && strings.Trim(digest, string(digest[0])) == ""
}

func validateProviderEndpoint(endpoint ProviderEndpoint) error {
	if strings.TrimSpace(endpoint.Role) == "" || endpoint.Role != strings.TrimSpace(endpoint.Role) || strings.TrimSpace(endpoint.Provider) == "" || strings.TrimSpace(endpoint.ResourceID) == "" || strings.TrimSpace(endpoint.Database) != endpoint.Database || strings.TrimSpace(endpoint.CredentialSecretKey) == "" {
		return fmt.Errorf("%w: provider endpoint identity is incomplete", ErrInconsistent)
	}
	parsed, err := url.Parse(endpoint.Endpoint)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.Hostname() == "" || containsCredentialQuery(parsed) {
		return fmt.Errorf("%w: provider endpoint is invalid or contains credentials", ErrInconsistent)
	}
	switch endpoint.Role {
	case "control", "ducklake":
		if parsed.Scheme != "postgres" || endpoint.Database == "" || endpoint.Bucket != "" {
			return fmt.Errorf("%w: database handoff endpoint is invalid", ErrInconsistent)
		}
	case "objects":
		if (parsed.Scheme != "http" && parsed.Scheme != "https") || endpoint.Bucket == "" || endpoint.Region == "" || endpoint.Database != "" {
			return fmt.Errorf("%w: object handoff endpoint is invalid", ErrInconsistent)
		}
	default:
		return fmt.Errorf("%w: provider endpoint role is unknown", ErrInconsistent)
	}
	return nil
}

func containsCredentialQuery(endpoint *url.URL) bool {
	for key := range endpoint.Query() {
		switch strings.ToLower(key) {
		case "password", "pass", "passwd", "secret", "secretkey", "accesskey", "token", "apikey", "api_key":
			return true
		}
	}
	return false
}

func validateSecretBundleReference(reference SecretBundleReference) error {
	parsed, err := url.Parse(reference.URI)
	if reference.Provider != "host-provisioned-root-file" || err != nil || parsed.Scheme != "file" || parsed.Host != "" || !filepath.IsAbs(parsed.Path) || parsed.Path != filepath.Clean(parsed.Path) || strings.TrimSpace(reference.Version) == "" || len(reference.Keys) == 0 {
		return fmt.Errorf("%w: secret bundle reference is invalid", ErrInconsistent)
	}
	decoded, err := hex.DecodeString(reference.SHA256)
	if err != nil || len(decoded) != 32 || reference.SHA256 != strings.ToLower(reference.SHA256) {
		return fmt.Errorf("%w: secret bundle digest is invalid", ErrInconsistent)
	}
	keys := slices.Clone(reference.Keys)
	slices.Sort(keys)
	if slices.Contains(keys, "") {
		return fmt.Errorf("%w: secret bundle key is empty", ErrInconsistent)
	}
	for index := 1; index < len(keys); index++ {
		if keys[index] == keys[index-1] {
			return fmt.Errorf("%w: secret bundle keys are not unique", ErrInconsistent)
		}
	}
	return nil
}

func HandoffPresent(handoff ReplacementHandoff) bool {
	return handoff.SchemaVersion != 0 || handoff.Kind != "" || handoff.Status != ""
}

func ValidateHandoffReport(report Report, expected HandoffExpectations) error {
	if report.Status != StatusSucceeded || !HandoffPresent(report.Handoff) {
		return fmt.Errorf("%w: successful provider restore has no replacement handoff", ErrInconsistent)
	}
	if strings.TrimSpace(expected.OccurrenceID) == "" || strings.TrimSpace(expected.TargetID) == "" || strings.TrimSpace(expected.RecoverySetID) == "" || strings.TrimSpace(expected.FrontierDigest) == "" || strings.TrimSpace(expected.ArtifactIdentity) == "" {
		return fmt.Errorf("%w: authoritative handoff expectations are incomplete", ErrInvalid)
	}
	if report.OccurrenceID != expected.OccurrenceID || report.TargetID != expected.TargetID || report.RecoverySetID != expected.RecoverySetID || report.FrontierDigest != expected.FrontierDigest {
		return fmt.Errorf("%w: provider restore report does not match authoritative handoff expectations", ErrInconsistent)
	}
	set := recoveryset.RecoverySet{ID: expected.RecoverySetID, FrontierDigest: expected.FrontierDigest, Delivery: recoveryset.DeliveryPointer{TargetID: expected.TargetID}}
	if err := report.Handoff.Validate(set, expected.ArtifactIdentity); err != nil {
		return err
	}
	return validateHandoffResults(report.Handoff, report.Databases, report.Objects)
}

func validateHandoffResults(handoff ReplacementHandoff, databases []DatabaseResult, objects []ObjectResult) error {
	databaseByRole := map[string]string{}
	for _, database := range databases {
		databaseByRole[string(database.DatabaseRole)] = database.DatabaseIdentity
	}
	for _, endpoint := range handoff.Providers {
		switch endpoint.Role {
		case "control", "ducklake":
			if databaseByRole[endpoint.Role] != endpoint.Database {
				return fmt.Errorf("%w: handoff database endpoint does not match the restored provider result", ErrInconsistent)
			}
		case "objects":
			for _, object := range objects {
				parsed, err := url.Parse(object.URI)
				if err != nil || parsed.Scheme != "s3" || parsed.Host != endpoint.Bucket {
					return fmt.Errorf("%w: handoff object endpoint does not match the restored provider result", ErrInconsistent)
				}
			}
		}
	}
	return nil
}
