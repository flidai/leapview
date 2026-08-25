// Command ociadmission admits a repository-owned OCI artifact only when its
// immutable digest, provenance, SBOM, and vulnerability evidence satisfy the
// repository contract.  The command intentionally keeps all verification
// decisions in typed Go code so local and CI admission exercise one contract.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/flidai/leapview/internal/app/securitypolicy"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	repositoryIdentity = "flidai/leapview"
	commandTimeout     = 2 * time.Minute
)

var (
	ociRepositoryPattern = regexp.MustCompile(`^[a-z0-9]+([./_-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*$`)
	imagePattern         = regexp.MustCompile(`^[a-z0-9]+([./_-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*@sha256:[0-9a-f]{64}$`)
	workflowPattern      = regexp.MustCompile(`^flidai/leapview/\.github/workflows/.*\.yml$`)
	revisionPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	semverPattern        = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	digestPattern        = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

type admissionOptions struct {
	image            string
	OCIRepository    string
	expectedWorkflow string
	sourceRevision   string
	policyPath       string
	mode             string
	evidencePath     string
	outputPath       string
}

type vulnerabilityPolicy struct {
	SchemaVersion  int         `json:"schemaVersion"`
	Scanner        string      `json:"scanner"`
	ScannerVersion string      `json:"scannerVersion"`
	ScannerImage   string      `json:"scannerImage"`
	Severity       []string    `json:"severity"`
	IgnoreUnfixed  *bool       `json:"ignoreUnfixed"`
	MaxUnresolved  json.Number `json:"maxUnresolved"`
}

type hermeticEvidence struct {
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
	VulnerabilityPolicy struct {
		SHA256  string `json:"sha256"`
		Scanner string `json:"scanner"`
		Passed  bool   `json:"passed"`
	} `json:"vulnerabilityPolicy"`
}

func runAdmission(args, env []string, stdout, stderr io.Writer) error {
	opts, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}
	if err := validateOptions(opts); err != nil {
		return err
	}
	policy, policyBytes, err := readPolicy(opts.policyPath)
	if err != nil {
		return err
	}
	policyHash := sha256.Sum256(policyBytes)
	policySHA256 := hex.EncodeToString(policyHash[:])
	runner := commandRunner{env: env}

	contract, err := runner.loadExceptionContract(opts.policyPath)
	if err != nil {
		return err
	}

	if opts.mode == "hermetic" {
		if err := verifyHermetic(opts, policySHA256); err != nil {
			return err
		}
		data, err := readJSONFile(opts.evidencePath)
		if err != nil {
			return fmt.Errorf("hermetic evidence is not valid JSON")
		}
		var evidence any
		if err := json.Unmarshal(data, &evidence); err != nil {
			return errors.New("hermetic evidence is not valid JSON")
		}
		if err := writeResult(opts, env, evidence, stdout); err != nil {
			return err
		}
		return nil
	}

	if token, ok := envValue(env, "GH_TOKEN"); !ok || strings.TrimSpace(token) == "" {
		if token, ok = envValue(env, "GITHUB_TOKEN"); !ok || strings.TrimSpace(token) == "" {
			return errors.New("live verification requires GH_TOKEN or GITHUB_TOKEN")
		}
	}
	githubRepository, ok := envValue(env, "GITHUB_REPOSITORY")
	if !ok || githubRepository == "" {
		githubRepository = repositoryIdentity
	}
	if githubRepository != repositoryIdentity {
		return errors.New("GitHub repository identity is not flidai/leapview")
	}
	if err := runner.verifyLive(opts, policy, policySHA256, contract, stdout); err != nil {
		return err
	}
	return nil
}

func parseOptions(args []string, stderr io.Writer) (admissionOptions, error) {
	var opts admissionOptions
	flags := flag.NewFlagSet("ociadmission", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.image, "image", "", "repository@sha256:digest")
	flags.StringVar(&opts.OCIRepository, "repository", "", "OCI repository")
	flags.StringVar(&opts.expectedWorkflow, "expected-workflow", "", "expected GitHub workflow")
	flags.StringVar(&opts.sourceRevision, "source-revision", "", "source commit SHA")
	flags.StringVar(&opts.policyPath, "policy", "", "vulnerability policy path")
	flags.StringVar(&opts.mode, "mode", "live", "live or hermetic")
	flags.StringVar(&opts.evidencePath, "evidence", "", "hermetic evidence path")
	flags.StringVar(&opts.outputPath, "output", "", "optional output path")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: ociadmission --image REPOSITORY@sha256:DIGEST")
		fmt.Fprintln(stderr, "  --repository OCI_REPOSITORY")
		fmt.Fprintln(stderr, "  --expected-workflow OWNER/REPO/.github/workflows/WORKFLOW.yml")
		fmt.Fprintln(stderr, "  --source-revision HEX_SHA")
		fmt.Fprintln(stderr, "  --policy PATH")
		fmt.Fprintln(stderr, "  [--mode live|hermetic] [--evidence PATH] [--output PATH]")
	}
	if err := flags.Parse(args); err != nil {
		return opts, usageError{message: err.Error()}
	}
	if flags.NArg() != 0 {
		return opts, usageError{message: fmt.Sprintf("unexpected argument: %s", flags.Arg(0))}
	}
	return opts, nil
}

func validateOptions(opts admissionOptions) error {
	if opts.mode != "live" && opts.mode != "hermetic" {
		return errors.New("mode must be live or hermetic")
	}
	if opts.image == "" || opts.OCIRepository == "" || opts.expectedWorkflow == "" || opts.sourceRevision == "" || opts.policyPath == "" {
		return usageError{message: "required admission argument is missing"}
	}
	if !ociRepositoryPattern.MatchString(opts.OCIRepository) {
		return errors.New("OCI repository is invalid")
	}
	if !strings.HasPrefix(opts.image, opts.OCIRepository+"@sha256:") {
		return errors.New("image must use the expected repository and a digest")
	}
	if !imagePattern.MatchString(opts.image) {
		return errors.New("image must be repository@sha256:<64 lowercase hex>")
	}
	if !workflowPattern.MatchString(opts.expectedWorkflow) {
		return errors.New("workflow identity is outside flidai/leapview")
	}
	if !revisionPattern.MatchString(opts.sourceRevision) {
		return errors.New("source revision must be a full commit SHA")
	}
	if info, err := os.Stat(opts.policyPath); err != nil || info.IsDir() {
		return errors.New("vulnerability policy is missing")
	}
	if opts.mode == "hermetic" && opts.evidencePath == "" {
		return errors.New("hermetic mode requires evidence")
	}
	return nil
}

func readPolicy(path string) (vulnerabilityPolicy, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return vulnerabilityPolicy{}, nil, errors.New("vulnerability policy is missing")
	}
	var policy vulnerabilityPolicy
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&policy); err != nil {
		return vulnerabilityPolicy{}, nil, errors.New("vulnerability policy is not valid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return vulnerabilityPolicy{}, nil, errors.New("vulnerability policy is not valid JSON")
	}
	if err := validatePolicy(policy); err != nil {
		return vulnerabilityPolicy{}, nil, errors.New("vulnerability policy is not pinned")
	}
	return policy, data, nil
}

func validatePolicy(policy vulnerabilityPolicy) error {
	if policy.SchemaVersion != 1 || policy.Scanner != "trivy" || !semverPattern.MatchString(policy.ScannerVersion) {
		return errors.New("policy identity")
	}
	wantImage := "aquasec/trivy:" + policy.ScannerVersion + "@sha256:"
	if !strings.HasPrefix(policy.ScannerImage, wantImage) || !digestPattern.MatchString(strings.TrimPrefix(policy.ScannerImage, "aquasec/trivy:"+policy.ScannerVersion+"@")) {
		return errors.New("policy scanner image")
	}
	if len(policy.Severity) == 0 {
		return errors.New("policy severity")
	}
	for _, severity := range policy.Severity {
		switch severity {
		case "CRITICAL", "HIGH", "MEDIUM", "LOW":
		default:
			return errors.New("policy severity")
		}
	}
	if policy.IgnoreUnfixed == nil || policy.MaxUnresolved == "" {
		return errors.New("policy max")
	}
	if _, err := maxUnresolved(policy.MaxUnresolved); err != nil {
		return errors.New("policy max")
	}
	return nil
}

func verifyHermetic(opts admissionOptions, policySHA256 string) error {
	data, err := os.ReadFile(opts.evidencePath)
	if err != nil {
		return errors.New("hermetic mode requires evidence")
	}
	var evidence hermeticEvidence
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&evidence); err != nil {
		return errors.New("hermetic evidence is not valid JSON")
	}
	digest := opts.image[strings.LastIndex(opts.image, "@")+1:]
	if evidence.SchemaVersion != 1 || evidence.Image != opts.image || evidence.Digest != digest || evidence.RegistryDigest != digest ||
		!evidence.Attestation.Verified || evidence.Attestation.Repository != repositoryIdentity || evidence.Attestation.Workflow != opts.expectedWorkflow || evidence.Attestation.SourceRevision != opts.sourceRevision ||
		!evidence.SBOM.Discoverable || evidence.SBOM.PredicateType != "https://spdx.dev/Document/v2.3" || evidence.VulnerabilityPolicy.SHA256 != policySHA256 || evidence.VulnerabilityPolicy.Scanner != "trivy" || !evidence.VulnerabilityPolicy.Passed {
		return errors.New("hermetic evidence is missing verified identity, SBOM, digest, or policy")
	}
	return nil
}

func verifyAttestation(data []byte, workflow, revision string) bool {
	var entries []struct {
		VerificationResult struct {
			Signature struct {
				Certificate map[string]any `json:"certificate"`
			} `json:"signature"`
		} `json:"verificationResult"`
	}
	if json.Unmarshal(data, &entries) != nil || len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		cert := entry.VerificationResult.Signature.Certificate
		repositoryValue := firstJSONAlternative(cert, "sourceRepositoryURI", "sourceRepository")
		if !matchesRepositoryIdentity(repositoryValue) {
			continue
		}
		workflowValue := firstJSONAlternative(cert, "buildSignerURI", "subjectAlternativeName", "workflow", "workflowPath", "buildConfigURI")
		revisionValue := firstJSONAlternative(cert, "sourceRepositoryDigest", "sourceDigest")
		if matchesWorkflowIdentity(workflowValue, workflow) && revisionValue == revision {
			return true
		}
	}
	return false
}

func matchesRepositoryIdentity(value string) bool {
	return value == repositoryIdentity || value == "https://github.com/"+repositoryIdentity
}

func matchesWorkflowIdentity(value, workflow string) bool {
	for _, expected := range []string{workflow, "https://github.com/" + workflow} {
		if value == expected || strings.HasPrefix(value, expected+"@") {
			return true
		}
	}
	return false
}

func hasSPDXDocument(data []byte) bool {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return false
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch item := v.(type) {
		case map[string]any:
			if stringValue(item["SPDXID"]) == "SPDXRef-DOCUMENT" {
				return true
			}
			for _, child := range item {
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range item {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
}

func scannerVersion(data []byte) (string, error) {
	var value struct {
		Version string `json:"Version"`
		Lower   string `json:"version"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return "", err
	}
	if value.Version != "" {
		return value.Version, nil
	}
	if value.Lower != "" {
		return value.Lower, nil
	}
	return "", errors.New("scanner version is missing")
}

func unresolvedCount(data []byte, contract *securitypolicy.Exceptions) (int, error) {
	var report struct {
		Results []struct {
			Vulnerabilities []struct {
				Rule     any `json:"VulnerabilityID"`
				Package  any `json:"PkgName"`
				Target   any `json:"Target"`
				Severity any `json:"Severity"`
			} `json:"Vulnerabilities"`
		} `json:"Results"`
	}
	if json.Unmarshal(data, &report) != nil {
		return 0, errors.New("invalid vulnerability report")
	}
	count := 0
	for _, result := range report.Results {
		for _, vulnerability := range result.Vulnerabilities {
			rule := stringValue(vulnerability.Rule)
			resource := stringValue(vulnerability.Package)
			if resource == "" {
				resource = stringValue(vulnerability.Target)
			}
			severity := stringValue(vulnerability.Severity)
			if rule == "" || resource == "" || !matchesException(contract, rule, resource, severity) {
				count++
			}
		}
	}
	return count, nil
}

func matchesException(contract *securitypolicy.Exceptions, rule, resource, severity string) bool {
	if contract == nil {
		return false
	}
	_, ok := contract.Match(securitypolicy.Finding{Scanner: "trivy", Rule: rule, Resource: resource, Severity: severity})
	return ok
}

func writeResult(opts admissionOptions, env []string, result any, stdout io.Writer) error {
	data, err := json.Marshal(result)
	if err != nil {
		return errors.New("could not encode admission result")
	}
	if opts.outputPath != "" {
		if err := os.MkdirAll(filepath.Dir(opts.outputPath), 0o755); err != nil {
			return errors.New("could not create admission output directory")
		}
		if err := os.WriteFile(opts.outputPath, append(data, '\n'), 0o644); err != nil {
			return errors.New("could not write admission output")
		}
	}
	if outputPath, ok := envValue(env, "GITHUB_OUTPUT"); ok && outputPath != "" {
		file, err := os.OpenFile(outputPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return errors.New("could not write GitHub output")
		}
		defer file.Close()
		digest := opts.image[strings.LastIndex(opts.image, "@")+1:]
		if _, err := fmt.Fprintf(file, "image=%s\ndigest=%s\n", opts.image, digest); err != nil {
			return errors.New("could not write GitHub output")
		}
	}
	if _, err := fmt.Fprintln(stdout, opts.image); err != nil {
		return err
	}
	return nil
}

func readJSONFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return json.Marshal(value)
}

func maxUnresolved(number json.Number) (int, error) {
	value, err := strconv.ParseFloat(string(number), 64)
	if err != nil || value < 0 || math.Trunc(value) != value || value > float64(int(^uint(0)>>1)) {
		return 0, errors.New("invalid maximum")
	}
	return int(value), nil
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return string(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

// firstJSONAlternative mirrors jq's `a // b` precedence: only an absent,
// null, or false value advances to the fallback. An explicit empty or
// wrong-typed canonical claim remains authoritative and therefore fails
// identity validation instead of being bypassed by a secondary claim.
func firstJSONAlternative(values map[string]any, keys ...string) string {
	for _, key := range keys {
		value, exists := values[key]
		if !exists || value == nil || value == false {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return ""
		}
		return text
	}
	return ""
}

func redactError(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}
