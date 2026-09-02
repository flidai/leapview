package contractprojection

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

//go:embed exclusions.json
var exclusionManifestJSON []byte

// Exclusion records why one or more authoring fields are outside the external
// contract identity. Paths are relative to the generated resource root and may
// use * for one map key or array element.
type Exclusion struct {
	Paths                       []string `json:"paths"`
	Reason                      string   `json:"reason"`
	SecurityPrivacyImplications string   `json:"securityPrivacyImplications"`
	CompatibilityImplications   string   `json:"compatibilityImplications"`
}

type ExclusionManifest struct {
	SchemaVersion   int                    `json:"schemaVersion"`
	Profile         string                 `json:"profile"`
	SourceAuthority string                 `json:"sourceAuthority"`
	Resources       map[string][]Exclusion `json:"resources"`
}

// LoadExclusionManifest returns the reviewed manifest embedded into the
// projection package. Keeping it beside the DTOs makes an exclusion change a
// reviewable contract-boundary change.
func LoadExclusionManifest() (ExclusionManifest, error) {
	var manifest ExclusionManifest
	if err := json.Unmarshal(exclusionManifestJSON, &manifest); err != nil {
		return ExclusionManifest{}, fmt.Errorf("decode contract exclusion manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return ExclusionManifest{}, err
	}
	return manifest, nil
}

func (manifest ExclusionManifest) Validate() error {
	if manifest.SchemaVersion != 1 {
		return fmt.Errorf("contract exclusion manifest: unsupported schemaVersion %d", manifest.SchemaVersion)
	}
	if manifest.Profile != Profile {
		return fmt.Errorf("contract exclusion manifest: profile %q does not match %q", manifest.Profile, Profile)
	}
	if manifest.SourceAuthority == "" {
		return errors.New("contract exclusion manifest: sourceAuthority is required")
	}
	seen := map[string]struct{}{}
	for _, kind := range []string{"Source", "Model", "SemanticModel"} {
		exclusions, ok := manifest.Resources[kind]
		if !ok || len(exclusions) == 0 {
			return fmt.Errorf("contract exclusion manifest: %s exclusions are required", kind)
		}
		for index, exclusion := range exclusions {
			if len(exclusion.Paths) == 0 || exclusion.Reason == "" || exclusion.SecurityPrivacyImplications == "" || exclusion.CompatibilityImplications == "" {
				return fmt.Errorf("contract exclusion manifest: %s exclusion %d is incomplete", kind, index)
			}
			if !sort.StringsAreSorted(exclusion.Paths) {
				return fmt.Errorf("contract exclusion manifest: %s exclusion %d paths must be sorted", kind, index)
			}
			for _, path := range exclusion.Paths {
				key := kind + "\x00" + path
				if path == "" || strings.HasPrefix(path, ".") || strings.HasSuffix(path, ".") {
					return fmt.Errorf("contract exclusion manifest: invalid %s path %q", kind, path)
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("contract exclusion manifest: duplicate %s path %q", kind, path)
				}
				seen[key] = struct{}{}
			}
		}
	}
	if len(manifest.Resources) != 3 {
		return fmt.Errorf("contract exclusion manifest: unexpected resource kind count %d", len(manifest.Resources))
	}
	return nil
}

// Excludes reports whether path is covered by a reviewed exclusion. A * path
// segment matches one map key or array element; ** matches zero or more path
// segments so recursive generated unions can be reviewed without a depth cap.
func (manifest ExclusionManifest) Excludes(kind, path string) bool {
	for _, exclusion := range manifest.Resources[kind] {
		for _, pattern := range exclusion.Paths {
			if exclusionPathMatches(pattern, path) {
				return true
			}
		}
	}
	return false
}

func exclusionPathMatches(pattern, path string) bool {
	patterns := strings.Split(pattern, ".")
	segments := strings.Split(path, ".")
	return matchExclusionSegments(patterns, segments)
}

func matchExclusionSegments(patterns, segments []string) bool {
	if len(patterns) == 0 {
		return len(segments) == 0
	}
	if patterns[0] == "**" {
		return matchExclusionSegments(patterns[1:], segments) || len(segments) > 0 && matchExclusionSegments(patterns, segments[1:])
	}
	return len(segments) > 0 && (patterns[0] == "*" || patterns[0] == segments[0]) && matchExclusionSegments(patterns[1:], segments[1:])
}
