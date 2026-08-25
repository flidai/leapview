package compatibility

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/platform/ociref"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/mod/semver"
)

const (
	CurrentSchemaVersion  = 1
	ReleasedV010Image     = "ghcr.io/yacobolo/libredash@sha256:677caaf256cb3a0d61efd47b289debbd91984976a5a5c4b372196a5d79ce7153"
	LegacyV010Database    = "libredash.db"
	currentDatabase       = "leapview.db"
	freshInstallDirection = "preserve the v0.1.0 state and provision a fresh LeapView instance"
)

type Operation string

const (
	OperationFreshInstall Operation = "fresh-install"
	OperationUpgrade      Operation = "upgrade"
	OperationRollback     Operation = "rollback"
)

const (
	ReasonAllowedFreshInstall       = "transition.allowed.fresh_install"
	ReasonAllowedExplicitTransition = "transition.allowed.explicit"
	ReasonDeniedFreshInstallOnly    = "transition.denied.fresh_install_only"
	ReasonDeniedNoExplicitRule      = "transition.denied.no_explicit_rule"
	ReasonDeniedUnknownRelease      = "transition.denied.release_unknown"
	ReasonDeniedInvalidRequest      = "transition.denied.invalid_request"
)

var ErrV010FreshInstallOnly = errors.New("v0.1.0 state is fresh-install-only")

//go:embed release-transition-policy.json
var embeddedPolicyJSON []byte

//go:embed release-transition-policy.schema.json
var embeddedPolicySchemaJSON []byte

var (
	policySchemaOnce sync.Once
	policySchema     *jsonschema.Schema
	policySchemaErr  error
)

type Policy struct {
	SchemaVersion int          `json:"schemaVersion"`
	PolicyVersion string       `json:"policyVersion"`
	Releases      []Release    `json:"releases"`
	Transitions   []Transition `json:"transitions"`
}

type Release struct {
	ID             string          `json:"id"`
	Version        string          `json:"version"`
	SourceRevision string          `json:"sourceRevision"`
	Distribution   string          `json:"distribution"`
	Artifacts      []Artifact      `json:"artifacts"`
	LegacyMarkers  []string        `json:"legacyMarkers"`
	Defaults       ReleaseDefaults `json:"defaults"`
}

type Artifact struct {
	Platform string `json:"platform"`
	Image    string `json:"image"`
}

type ReleaseDefaults struct {
	FreshInstall Rule `json:"freshInstall"`
	Upgrade      Rule `json:"upgrade"`
	Rollback     Rule `json:"rollback"`
}

type Rule struct {
	Allowed      bool     `json:"allowed"`
	ReasonCode   string   `json:"reasonCode"`
	Remediation  string   `json:"remediation"`
	Requirements []string `json:"requirements"`
}

type Transition struct {
	Operation Operation `json:"operation"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Platforms []string  `json:"platforms"`
	Decision  Rule      `json:"decision"`
}

type ReleaseIdentity struct {
	ReleaseID      string `json:"releaseId,omitempty"`
	Version        string `json:"version,omitempty"`
	SourceRevision string `json:"sourceRevision,omitempty"`
	Image          string `json:"image"`
	Distribution   string `json:"distribution,omitempty"`
	Platform       string `json:"platform"`
}

type Request struct {
	Operation Operation       `json:"operation"`
	Current   ReleaseIdentity `json:"current,omitempty"`
	Next      ReleaseIdentity `json:"next"`
}

type Decision struct {
	SchemaVersion int             `json:"schemaVersion"`
	PolicyVersion string          `json:"policyVersion"`
	Operation     Operation       `json:"operation"`
	Allowed       bool            `json:"allowed"`
	ReasonCode    string          `json:"reasonCode"`
	Remediation   string          `json:"remediation"`
	Requirements  []string        `json:"requirements"`
	Current       ReleaseIdentity `json:"current,omitempty"`
	Next          ReleaseIdentity `json:"next"`
}

type DecisionError struct {
	Decision Decision
}

func (e *DecisionError) Error() string {
	message := e.Decision.ReasonCode
	if e.Decision.Remediation != "" {
		message += ": " + e.Decision.Remediation
	}
	return message
}

func (e *DecisionError) Unwrap() error {
	if e.Decision.ReasonCode == ReasonDeniedFreshInstallOnly {
		return ErrV010FreshInstallOnly
	}
	return nil
}

func EmbeddedPolicy() (*Policy, error) {
	return ParsePolicy(embeddedPolicyJSON)
}

func EmbeddedPolicyDocument() []byte {
	return append([]byte(nil), embeddedPolicyJSON...)
}

func EmbeddedPolicySchema() []byte {
	return append([]byte(nil), embeddedPolicySchemaJSON...)
}

func ParsePolicy(data []byte) (*Policy, error) {
	compiled, err := compiledPolicySchema()
	if err != nil {
		return nil, err
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode release-transition policy JSON: %w", err)
	}
	if err := compiled.Validate(instance); err != nil {
		return nil, fmt.Errorf("validate release-transition policy schema: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("decode release-transition policy: %w", err)
	}
	if err := policy.validate(); err != nil {
		return nil, err
	}
	return &policy, nil
}

func compiledPolicySchema() (*jsonschema.Schema, error) {
	policySchemaOnce.Do(func() {
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(embeddedPolicySchemaJSON))
		if err != nil {
			policySchemaErr = fmt.Errorf("decode release-transition policy schema: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		const schemaURL = "https://leapview.dev/schemas/release-transition-policy.schema.json"
		if err := compiler.AddResource(schemaURL, document); err != nil {
			policySchemaErr = fmt.Errorf("register release-transition policy schema: %w", err)
			return
		}
		policySchema, policySchemaErr = compiler.Compile(schemaURL)
	})
	return policySchema, policySchemaErr
}

func (p *Policy) validate() error {
	if p.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported release-transition schemaVersion %d", p.SchemaVersion)
	}
	if strings.TrimSpace(p.PolicyVersion) == "" {
		return fmt.Errorf("release-transition policyVersion is required")
	}
	if len(p.Releases) == 0 {
		return fmt.Errorf("release-transition policy requires at least one release")
	}
	releases := make(map[string]Release, len(p.Releases))
	images := make(map[string]string)
	for _, release := range p.Releases {
		if _, exists := releases[release.ID]; exists {
			return fmt.Errorf("duplicate release %q", release.ID)
		}
		if !semver.IsValid("v" + strings.TrimPrefix(release.Version, "v")) {
			return fmt.Errorf("release %q version is not semantic", release.ID)
		}
		if release.ID != "v"+strings.TrimPrefix(release.Version, "v") {
			return fmt.Errorf("release %q id does not match version %q", release.ID, release.Version)
		}
		if len(release.SourceRevision) != 40 || !isLowerHex(release.SourceRevision) {
			return fmt.Errorf("release %q sourceRevision must be a 40-character lowercase Git SHA", release.ID)
		}
		if strings.TrimSpace(release.Distribution) == "" || len(release.Artifacts) == 0 {
			return fmt.Errorf("release %q distribution and artifacts are required", release.ID)
		}
		platforms := make(map[string]struct{}, len(release.Artifacts))
		for _, artifact := range release.Artifacts {
			if !supportedPlatform(artifact.Platform) {
				return fmt.Errorf("release %q has unsupported platform %q", release.ID, artifact.Platform)
			}
			if _, exists := platforms[artifact.Platform]; exists {
				return fmt.Errorf("release %q has duplicate platform %q", release.ID, artifact.Platform)
			}
			platforms[artifact.Platform] = struct{}{}
			if err := ociref.ValidateImmutable(artifact.Image); err != nil {
				return fmt.Errorf("release %q platform %q image: %w", release.ID, artifact.Platform, err)
			}
			if prior, exists := images[artifact.Image]; exists {
				return fmt.Errorf("release image %q is ambiguous between %q and %q", artifact.Image, prior, release.ID)
			}
			images[artifact.Image] = release.ID
		}
		markers := make(map[string]struct{}, len(release.LegacyMarkers))
		for _, marker := range release.LegacyMarkers {
			if marker == "" || filepath.IsAbs(marker) || filepath.Clean(marker) != marker || marker == "." || strings.HasPrefix(marker, ".."+string(filepath.Separator)) {
				return fmt.Errorf("release %q has unsafe legacy marker %q", release.ID, marker)
			}
			if _, exists := markers[marker]; exists {
				return fmt.Errorf("release %q has duplicate legacy marker %q", release.ID, marker)
			}
			markers[marker] = struct{}{}
		}
		for name, rule := range map[string]Rule{
			"freshInstall": release.Defaults.FreshInstall,
			"upgrade":      release.Defaults.Upgrade,
			"rollback":     release.Defaults.Rollback,
		} {
			if err := validateRule(rule); err != nil {
				return fmt.Errorf("release %q default %s: %w", release.ID, name, err)
			}
		}
		releases[release.ID] = release
	}
	keys := make(map[string]struct{})
	for _, transition := range p.Transitions {
		if transition.Operation != OperationUpgrade && transition.Operation != OperationRollback {
			return fmt.Errorf("transition operation %q is unsupported", transition.Operation)
		}
		from, fromOK := releases[transition.From]
		to, toOK := releases[transition.To]
		if !fromOK || !toOK {
			return fmt.Errorf("transition %s %q to %q references an unknown release", transition.Operation, transition.From, transition.To)
		}
		if len(transition.Platforms) == 0 {
			return fmt.Errorf("transition %s %q to %q requires at least one platform", transition.Operation, transition.From, transition.To)
		}
		if err := validateRule(transition.Decision); err != nil {
			return fmt.Errorf("transition %s %q to %q: %w", transition.Operation, transition.From, transition.To, err)
		}
		for _, platform := range transition.Platforms {
			if _, ok := from.artifactForPlatform(platform); !ok {
				return fmt.Errorf("transition %s %q to %q platform %q is absent from source release", transition.Operation, transition.From, transition.To, platform)
			}
			if _, ok := to.artifactForPlatform(platform); !ok {
				return fmt.Errorf("transition %s %q to %q platform %q is absent from target release", transition.Operation, transition.From, transition.To, platform)
			}
			key := strings.Join([]string{string(transition.Operation), transition.From, transition.To, platform}, "\x00")
			if _, exists := keys[key]; exists {
				return fmt.Errorf("duplicate transition %s %q to %q for %q", transition.Operation, transition.From, transition.To, platform)
			}
			keys[key] = struct{}{}
		}
	}
	return nil
}

func supportedPlatform(platform string) bool {
	return platform == "linux/amd64" || platform == "linux/arm64"
}

func (p *Policy) Validate() error {
	if p == nil {
		return fmt.Errorf("release-transition policy is required")
	}
	return p.validate()
}

func validateRule(rule Rule) error {
	if rule.Allowed && !strings.HasPrefix(rule.ReasonCode, "transition.allowed.") {
		return fmt.Errorf("allowed decision reasonCode must start with transition.allowed.")
	}
	if !rule.Allowed {
		if !strings.HasPrefix(rule.ReasonCode, "transition.denied.") {
			return fmt.Errorf("denied decision reasonCode must start with transition.denied.")
		}
		if strings.TrimSpace(rule.Remediation) == "" {
			return fmt.Errorf("denied decision remediation is required")
		}
	}
	seen := make(map[string]struct{}, len(rule.Requirements))
	for _, requirement := range rule.Requirements {
		if _, ok := seen[requirement]; ok {
			return fmt.Errorf("duplicate requirement %q", requirement)
		}
		seen[requirement] = struct{}{}
	}
	return nil
}

func isLowerHex(value string) bool {
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func (p *Policy) ReleaseByID(id string) (Release, bool) {
	for _, release := range p.Releases {
		if release.ID == id {
			return release, true
		}
	}
	return Release{}, false
}

func (r Release) IdentityForPlatform(platform string) ReleaseIdentity {
	artifact, _ := r.artifactForPlatform(platform)
	return ReleaseIdentity{
		ReleaseID: r.ID, Version: r.Version, SourceRevision: r.SourceRevision,
		Image: artifact.Image, Distribution: r.Distribution, Platform: platform,
	}
}

func (r Release) artifactForPlatform(platform string) (Artifact, bool) {
	for _, artifact := range r.Artifacts {
		if artifact.Platform == platform {
			return artifact, true
		}
	}
	return Artifact{}, false
}

func (p *Policy) Evaluate(request Request) Decision {
	decision := Decision{
		SchemaVersion: CurrentSchemaVersion, PolicyVersion: p.PolicyVersion,
		Operation: request.Operation, Current: request.Current, Next: request.Next,
		ReasonCode:  ReasonDeniedInvalidRequest,
		Remediation: "provide complete immutable release identities for a supported operation",
	}
	if request.Operation != OperationFreshInstall && request.Operation != OperationUpgrade && request.Operation != OperationRollback {
		return decision
	}
	if request.Operation == OperationFreshInstall {
		next, ok := p.resolve(request.Next)
		if !ok {
			return p.unknownDecision(decision)
		}
		decision.Next = next.IdentityForPlatform(request.Next.Platform)
		return applyRule(decision, next.Defaults.FreshInstall)
	}

	if request.Operation == OperationUpgrade {
		current, currentOK := p.resolve(request.Current)
		next, nextOK := p.resolve(request.Next)
		if currentOK && nextOK {
			decision.Current = current.IdentityForPlatform(request.Current.Platform)
			decision.Next = next.IdentityForPlatform(request.Next.Platform)
			if rule, ok := p.transitionRule(request.Operation, current.ID, next.ID, request.Current.Platform); ok {
				return applyRule(decision, rule)
			}
			return applyRule(decision, current.Defaults.Upgrade)
		}
		if currentOK {
			decision.Current = current.IdentityForPlatform(request.Current.Platform)
			if request.Next.ReleaseID != "" {
				return p.unknownDecision(decision)
			}
			return applyRule(decision, current.Defaults.Upgrade)
		}
		if nextOK {
			decision.Next = next.IdentityForPlatform(request.Next.Platform)
			return applyRule(decision, next.Defaults.Upgrade)
		}
		return p.unknownDecision(decision)
	}

	next, nextOK := p.resolve(request.Next)
	current, currentOK := p.resolve(request.Current)
	if currentOK && nextOK {
		decision.Current = current.IdentityForPlatform(request.Current.Platform)
		decision.Next = next.IdentityForPlatform(request.Next.Platform)
		if rule, ok := p.transitionRule(request.Operation, current.ID, next.ID, request.Current.Platform); ok {
			return applyRule(decision, rule)
		}
		return applyRule(decision, next.Defaults.Rollback)
	}
	if nextOK {
		decision.Next = next.IdentityForPlatform(request.Next.Platform)
		return applyRule(decision, next.Defaults.Rollback)
	}
	if currentOK {
		decision.Current = current.IdentityForPlatform(request.Current.Platform)
		return applyRule(decision, current.Defaults.Rollback)
	}
	return p.unknownDecision(decision)
}

func (p *Policy) resolve(identity ReleaseIdentity) (Release, bool) {
	if identity.Platform == "" || identity.Image == "" {
		return Release{}, false
	}
	for _, release := range p.Releases {
		artifact, ok := release.artifactForPlatform(identity.Platform)
		if !ok || artifact.Image != identity.Image {
			continue
		}
		if identity.ReleaseID != "" && identity.ReleaseID != release.ID ||
			identity.Version != "" && identity.Version != release.Version ||
			identity.SourceRevision != "" && identity.SourceRevision != release.SourceRevision ||
			identity.Distribution != "" && identity.Distribution != release.Distribution {
			return Release{}, false
		}
		return release, true
	}
	return Release{}, false
}

func (p *Policy) transitionRule(operation Operation, from, to, platform string) (Rule, bool) {
	for _, transition := range p.Transitions {
		if transition.Operation == operation && transition.From == from && transition.To == to && contains(transition.Platforms, platform) {
			return transition.Decision, true
		}
	}
	return Rule{}, false
}

func (p *Policy) unknownDecision(decision Decision) Decision {
	decision.ReasonCode = ReasonDeniedUnknownRelease
	decision.Remediation = "use exact immutable artifacts named by the versioned release-transition policy"
	decision.Requirements = []string{}
	return decision
}

func applyRule(decision Decision, rule Rule) Decision {
	decision.Allowed = rule.Allowed
	decision.ReasonCode = rule.ReasonCode
	decision.Remediation = rule.Remediation
	decision.Requirements = append([]string{}, rule.Requirements...)
	return decision
}

func (d Decision) Err() error {
	if d.Allowed {
		return nil
	}
	return &DecisionError{Decision: d}
}

func (p *Policy) EvaluateImages(operation Operation, current, next, platform string) Decision {
	return p.Evaluate(Request{
		Operation: operation,
		Current:   ReleaseIdentity{Image: strings.TrimSpace(current), Platform: platform},
		Next:      ReleaseIdentity{Image: strings.TrimSpace(next), Platform: platform},
	})
}

// ValidateUpgradeImages remains the small compatibility boundary used by
// callers which only have immutable OCI identities. New code should retain the
// returned Decision when it needs qualification evidence.
func ValidateUpgradeImages(current, next string) error {
	policy, err := EmbeddedPolicy()
	if err != nil {
		return err
	}
	return policy.EvaluateImages(OperationUpgrade, current, next, runtime.GOOS+"/"+runtime.GOARCH).Err()
}

func ValidateRollbackImages(current, next string) error {
	policy, err := EmbeddedPolicy()
	if err != nil {
		return err
	}
	return policy.EvaluateImages(OperationRollback, current, next, runtime.GOOS+"/"+runtime.GOARCH).Err()
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// RejectLegacyState prevents the renamed LeapView process from silently
// creating a new database beside a released v0.1.0 LibreDash database.
func RejectLegacyState(databasePath string) error {
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" || databasePath == ":memory:" {
		return nil
	}
	databasePath, _, _ = strings.Cut(databasePath, "?")
	if filepath.Base(databasePath) != currentDatabase {
		return nil
	}
	legacyPath := filepath.Join(filepath.Dir(databasePath), LegacyV010Database)
	if _, err := os.Lstat(legacyPath); err == nil {
		return fmt.Errorf("%w: found %s; %s", ErrV010FreshInstallOnly, legacyPath, freshInstallDirection)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect v0.1.0 state marker %s: %w", legacyPath, err)
	}
	return nil
}
