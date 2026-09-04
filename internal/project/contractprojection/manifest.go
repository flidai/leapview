package contractprojection

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

//go:embed exclusions.json
var exclusionManifestJSON []byte

// Exclusion records why one or more authoring fields are outside the external
// contract identity. Paths are relative to the generated resource root and may
// use * for one map key or array element, or ** for recursive filter nodes.
type Exclusion struct {
	Paths                       []string `json:"paths"`
	SourceShapeFingerprint      string   `json:"sourceShapeFingerprint"`
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

const SourceAuthority = "api/gen/data-resources-ir.json"

// LoadExclusionManifest returns the reviewed manifest embedded into the
// projection package. Keeping it beside the DTOs makes an exclusion change a
// reviewable contract-boundary change.
func LoadExclusionManifest() (ExclusionManifest, error) {
	return ParseExclusionManifest(exclusionManifestJSON)
}

// ParseExclusionManifest decodes and validates a reviewed manifest. Strict
// decoding and duplicate-key detection keep an unreviewed field from being
// smuggled into the manifest by relying on JSON's last-key-wins behavior.
func ParseExclusionManifest(raw []byte) (ExclusionManifest, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return ExclusionManifest{}, fmt.Errorf("decode contract exclusion manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest ExclusionManifest
	if err := decoder.Decode(&manifest); err != nil {
		return ExclusionManifest{}, fmt.Errorf("decode contract exclusion manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return ExclusionManifest{}, errors.New("decode contract exclusion manifest: trailing JSON value")
		}
		return ExclusionManifest{}, fmt.Errorf("decode contract exclusion manifest: trailing data: %w", err)
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
	if manifest.SourceAuthority != SourceAuthority {
		return fmt.Errorf("contract exclusion manifest: sourceAuthority %q does not match %q", manifest.SourceAuthority, SourceAuthority)
	}
	seen := map[string]struct{}{}
	for _, kind := range []string{"Source", "Model", "SemanticModel"} {
		exclusions, ok := manifest.Resources[kind]
		if !ok || len(exclusions) == 0 {
			return fmt.Errorf("contract exclusion manifest: %s exclusions are required", kind)
		}
		for index, exclusion := range exclusions {
			if len(exclusion.Paths) == 0 || exclusion.SourceShapeFingerprint == "" || exclusion.Reason == "" || exclusion.SecurityPrivacyImplications == "" || exclusion.CompatibilityImplications == "" {
				return fmt.Errorf("contract exclusion manifest: %s exclusion %d is incomplete", kind, index)
			}
			if !validSourceShapeFingerprint(exclusion.SourceShapeFingerprint) {
				return fmt.Errorf("contract exclusion manifest: %s exclusion %d has invalid sourceShapeFingerprint %q", kind, index, exclusion.SourceShapeFingerprint)
			}
			if !sort.StringsAreSorted(exclusion.Paths) {
				return fmt.Errorf("contract exclusion manifest: %s exclusion %d paths must be sorted", kind, index)
			}
			for _, path := range exclusion.Paths {
				key := kind + "\x00" + path
				if !validExclusionPath(path) {
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

func validSourceShapeFingerprint(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil
}

func validExclusionPath(path string) bool {
	if path == "" {
		return false
	}
	for _, segment := range strings.Split(path, ".") {
		if segment == "" {
			return false
		}
		if segment == "*" || segment == "**" {
			continue
		}
		for index, char := range segment {
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
				if index == 0 && char >= '0' && char <= '9' {
					return false
				}
				continue
			}
			return false
		}
	}
	return true
}

// rejectDuplicateJSONKeys scans all JSON objects before strict decoding.
// encoding/json intentionally accepts duplicate object members, which would
// make a signed/reviewed manifest ambiguous.
func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON token %v", token)
		}
		return fmt.Errorf("trailing JSON data: %w", err)
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("object member name is not a string")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate object member %q", name)
			}
			seen[name] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

// Excludes reports whether path is covered by a reviewed exclusion. A * path
// segment matches one map key or array element; ** matches zero or more path
// segments so recursive generated unions can be reviewed without a depth cap.
func (manifest ExclusionManifest) Excludes(kind, path string) bool {
	for _, exclusion := range manifest.Resources[kind] {
		for _, pattern := range exclusion.Paths {
			if ExclusionPathMatches(pattern, path) {
				return true
			}
		}
	}
	return false
}

// ExclusionPathMatches applies the manifest's exact leaf-pattern semantics.
// A wildcard matches one map key/array element and ** matches recursive
// filter nodes; neither wildcard matches an arbitrary parent subtree.
func ExclusionPathMatches(pattern, path string) bool {
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
