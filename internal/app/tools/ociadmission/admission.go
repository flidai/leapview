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
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/securitypolicy"
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
	platformPattern      = regexp.MustCompile(`^linux/(amd64|arm64)$`)
)

type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

type admissionOptions struct {
	image                   string
	OCIRepository           string
	expectedWorkflow        string
	sourceRevision          string
	policyPath              string
	platform                string
	mode                    string
	evidencePath            string
	outputPath              string
	vulnerabilityReportPath string
	admissionBundlePath     string
	releaseID               string
	releaseVersion          string
}

const (
	vulnerabilityReportSchema   = 1
	maxVulnerabilityReportBytes = 256 * 1024
	maxVulnerabilityJSONBytes   = 32 * 1024 * 1024
	// Max-length sanitized finding fields at this count stay below the byte cap.
	maxReportedFindings  = 128
	outcomeNotScanned    = "not-scanned"
	outcomeScannerError  = "scanner-error"
	outcomeInvalidReport = "invalid-report"
	outcomeRejected      = "rejected"
	outcomePassed        = "passed"
)

type vulnerabilityReport struct {
	SchemaVersion          int               `json:"schemaVersion"`
	Image                  string            `json:"image"`
	Revision               string            `json:"revision"`
	ExpectedSourceRevision string            `json:"expectedSourceRevision"`
	Platform               string            `json:"platform"`
	Scanner                reportScanner     `json:"scanner"`
	Database               reportDatabases   `json:"database"`
	PolicySHA256           string            `json:"policySHA256"`
	Outcome                string            `json:"outcome"`
	UnresolvedCount        int               `json:"unresolvedCount"`
	Findings               []reportedFinding `json:"findings"`
	FindingsTruncated      bool              `json:"findingsTruncated,omitempty"`
}

type reportScanner struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type reportDatabases struct {
	Vulnerability reportDatabaseMetadata `json:"vulnerability"`
	Java          reportDatabaseMetadata `json:"java"`
}

type reportDatabaseMetadata struct {
	Version      string `json:"version"`
	UpdatedAt    string `json:"updatedAt"`
	NextUpdate   string `json:"nextUpdate"`
	DownloadedAt string `json:"downloadedAt"`
}

type reportedFinding struct {
	Package          string `json:"package"`
	CVE              string `json:"cve"`
	InstalledVersion string `json:"installedVersion"`
	FixedVersion     string `json:"fixedVersion"`
}

type parsedVulnerabilityReport struct {
	UnresolvedCount   int
	Findings          []reportedFinding
	FindingsTruncated bool
	ImageRevision     string
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
		SHA256   string `json:"sha256"`
		Scanner  string `json:"scanner"`
		Passed   bool   `json:"passed"`
		Platform string `json:"platform,omitempty"`
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
	if err := validateReportOutputPaths(opts, env); err != nil {
		return err
	}
	if err := validateBundleOptions(opts, env); err != nil {
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

	report := newVulnerabilityReport(opts, policySHA256, "")
	if token, ok := envValue(env, "GH_TOKEN"); !ok || strings.TrimSpace(token) == "" {
		if token, ok = envValue(env, "GITHUB_TOKEN"); !ok || strings.TrimSpace(token) == "" {
			return rejectWithReport(opts, report, outcomeNotScanned, "live verification requires GH_TOKEN or GITHUB_TOKEN")
		}
	}
	githubRepository, ok := envValue(env, "GITHUB_REPOSITORY")
	if !ok || githubRepository == "" {
		githubRepository = repositoryIdentity
	}
	if githubRepository != repositoryIdentity {
		return rejectWithReport(opts, report, outcomeNotScanned, "GitHub repository identity is not flidai/leapview")
	}
	if err := runner.verifyLive(opts, policy, policyBytes, policySHA256, contract, stdout); err != nil {
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
	flags.StringVar(&opts.platform, "platform", "", "target image platform (linux/amd64 or linux/arm64)")
	flags.StringVar(&opts.mode, "mode", "live", "live or hermetic")
	flags.StringVar(&opts.evidencePath, "evidence", "", "hermetic evidence path")
	flags.StringVar(&opts.outputPath, "output", "", "optional output path")
	flags.StringVar(&opts.vulnerabilityReportPath, "vulnerability-report", "", "optional sanitized vulnerability report path")
	flags.StringVar(&opts.admissionBundlePath, "admission-bundle", "", "optional fresh directory for an authenticated producer's canonical receipt and evidence")
	flags.StringVar(&opts.releaseID, "release-id", "", "immutable release identity for the canonical receipt")
	flags.StringVar(&opts.releaseVersion, "release-version", "", "exact image version for the canonical receipt")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: ociadmission --image REPOSITORY@sha256:DIGEST")
		fmt.Fprintln(stderr, "  --repository OCI_REPOSITORY")
		fmt.Fprintln(stderr, "  --expected-workflow OWNER/REPO/.github/workflows/WORKFLOW.yml")
		fmt.Fprintln(stderr, "  --source-revision HEX_SHA")
		fmt.Fprintln(stderr, "  --policy PATH")
		fmt.Fprintln(stderr, "  [--platform linux/amd64|linux/arm64]")
		fmt.Fprintln(stderr, "  [--mode live|hermetic] [--evidence PATH] [--output PATH] [--vulnerability-report PATH]")
		fmt.Fprintln(stderr, "  [--admission-bundle DIR --release-id ID --release-version VERSION]")
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
	if opts.platform != "" && !platformPattern.MatchString(opts.platform) {
		return errors.New("platform must be linux/amd64 or linux/arm64")
	}
	if info, err := os.Stat(opts.policyPath); err != nil || info.IsDir() {
		return errors.New("vulnerability policy is missing")
	}
	if opts.mode == "hermetic" && opts.evidencePath == "" {
		return errors.New("hermetic mode requires evidence")
	}
	return nil
}

func validateReportOutputPaths(opts admissionOptions, env []string) error {
	if opts.vulnerabilityReportPath == "" {
		return nil
	}
	canonical := func(path string) (string, error) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			return resolved, nil
		}
		if parent, err := filepath.EvalSymlinks(filepath.Dir(absolute)); err == nil {
			return filepath.Join(parent, filepath.Base(absolute)), nil
		}
		return absolute, nil
	}
	reportPath, err := canonical(opts.vulnerabilityReportPath)
	if err != nil {
		return errors.New("vulnerability report path is invalid")
	}
	githubOutput, _ := envValue(env, "GITHUB_OUTPUT")
	for _, output := range []string{opts.outputPath, githubOutput} {
		if output == "" {
			continue
		}
		outputPath, err := canonical(output)
		if err != nil {
			return errors.New("admission output path is invalid")
		}
		if reportPath == outputPath {
			return errors.New("vulnerability report path must be separate from admission outputs")
		}
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
		!evidence.SBOM.Discoverable || evidence.SBOM.PredicateType != "https://spdx.dev/Document/v2.3" || evidence.VulnerabilityPolicy.SHA256 != policySHA256 || evidence.VulnerabilityPolicy.Scanner != "trivy" || !evidence.VulnerabilityPolicy.Passed || evidence.VulnerabilityPolicy.Platform != opts.platform {
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

func parseVulnerabilityReport(data []byte, contract *securitypolicy.Exceptions, env []string) (parsedVulnerabilityReport, error) {
	if len(data) == 0 || len(data) > maxVulnerabilityJSONBytes {
		return parsedVulnerabilityReport{}, errors.New("invalid vulnerability report")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return parsedVulnerabilityReport{}, errors.New("invalid vulnerability report")
	}
	if _, exists := root["Results"]; !exists {
		return parsedVulnerabilityReport{}, errors.New("invalid vulnerability report")
	}
	var report struct {
		Metadata struct {
			ImageConfig struct {
				Config struct {
					Labels map[string]string `json:"Labels"`
				} `json:"config"`
			} `json:"ImageConfig"`
		} `json:"Metadata"`
		Results []struct {
			Vulnerabilities []struct {
				Rule             any `json:"VulnerabilityID"`
				Package          any `json:"PkgName"`
				Target           any `json:"Target"`
				Severity         any `json:"Severity"`
				InstalledVersion any `json:"InstalledVersion"`
				FixedVersion     any `json:"FixedVersion"`
			} `json:"Vulnerabilities"`
		} `json:"Results"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return parsedVulnerabilityReport{}, errors.New("invalid vulnerability report")
	}
	parsed := parsedVulnerabilityReport{Findings: make([]reportedFinding, 0)}
	imageRevision := report.Metadata.ImageConfig.Config.Labels["org.opencontainers.image.revision"]
	if revisionPattern.MatchString(imageRevision) && sanitizeReportValue(imageRevision, env, "0123456789abcdef", 40) == imageRevision {
		parsed.ImageRevision = imageRevision
	}
	for _, result := range report.Results {
		for _, vulnerability := range result.Vulnerabilities {
			rule := stringValue(vulnerability.Rule)
			resource := stringValue(vulnerability.Package)
			if resource == "" {
				resource = stringValue(vulnerability.Target)
			}
			severity := stringValue(vulnerability.Severity)
			if rule != "" && resource != "" && matchesException(contract, rule, resource, severity) {
				continue
			}
			parsed.UnresolvedCount++
			if len(parsed.Findings) >= maxReportedFindings {
				parsed.FindingsTruncated = true
				continue
			}
			parsed.Findings = append(parsed.Findings, reportedFinding{
				Package:          sanitizeReportValue(stringValue(vulnerability.Package), env, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789@+_.:/-", 256),
				CVE:              sanitizeReportValue(rule, env, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._:-", 128),
				InstalledVersion: sanitizeReportValue(stringValue(vulnerability.InstalledVersion), env, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.+~_:,@-", 256),
				FixedVersion:     sanitizeReportValue(stringValue(vulnerability.FixedVersion), env, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.+~_:,@-", 256),
			})
		}
	}
	return parsed, nil
}

func sanitizeReportValue(value string, env []string, allowed string, maxLength int) string {
	value = strings.TrimSpace(value)
	for _, entry := range env {
		key, secret, ok := strings.Cut(entry, "=")
		if !ok || secret == "" || !sensitiveEnvironmentKey(key) {
			continue
		}
		value = strings.ReplaceAll(value, secret, "")
	}
	if len(value) == 0 || len(value) > maxLength {
		return ""
	}
	for _, char := range value {
		if char > 127 || !strings.ContainsRune(allowed, char) {
			return ""
		}
	}
	return value
}

func sensitiveEnvironmentKey(key string) bool {
	key = strings.ToUpper(key)
	for _, marker := range []string{"TOKEN", "PASSWORD", "SECRET", "CREDENTIAL", "AUTH", "KEY"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func newVulnerabilityReport(opts admissionOptions, policySHA256, scannerVersion string) vulnerabilityReport {
	return vulnerabilityReport{
		SchemaVersion: vulnerabilityReportSchema,
		Image:         opts.image, ExpectedSourceRevision: opts.sourceRevision, Platform: opts.platform,
		Scanner:      reportScanner{Name: "trivy", Version: scannerVersion},
		PolicySHA256: policySHA256, Findings: make([]reportedFinding, 0),
	}
}

func writeVulnerabilityReport(path string, report vulnerabilityReport) error {
	if path == "" {
		return nil
	}
	if len(report.Findings) > maxReportedFindings {
		return errors.New("vulnerability report contains too many findings")
	}
	data, err := json.Marshal(report)
	if err != nil {
		return errors.New("could not encode vulnerability report")
	}
	for len(data)+1 > maxVulnerabilityReportBytes && len(report.Findings) > 0 {
		report.FindingsTruncated = true
		report.Findings = report.Findings[:len(report.Findings)-1]
		data, err = json.Marshal(report)
		if err != nil {
			return errors.New("could not encode vulnerability report")
		}
	}
	if len(data)+1 > maxVulnerabilityReportBytes {
		return errors.New("vulnerability report exceeds bounded size")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return errors.New("could not create vulnerability report directory")
	}
	temporary, err := os.CreateTemp(directory, ".oci-vulnerability-report-*")
	if err != nil {
		return errors.New("could not write vulnerability report")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return errors.New("could not write vulnerability report")
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return errors.New("could not write vulnerability report")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("could not write vulnerability report")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return errors.New("could not write vulnerability report")
	}
	return nil
}

func readDatabaseMetadata(path string, env []string) reportDatabaseMetadata {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > 16*1024 {
		return reportDatabaseMetadata{}
	}
	var values map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil || values == nil {
		return reportDatabaseMetadata{}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return reportDatabaseMetadata{}
	}
	metadataValue := func(keys ...string) string {
		for _, key := range keys {
			if value, exists := values[key]; exists {
				return stringValue(value)
			}
		}
		return ""
	}
	return reportDatabaseMetadata{
		Version:      sanitizeReportValue(metadataValue("Version", "version"), env, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._:-", 64),
		UpdatedAt:    sanitizeReportValue(metadataValue("UpdatedAt", "updatedAt"), env, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.:+-TZ", 64),
		NextUpdate:   sanitizeReportValue(metadataValue("NextUpdate", "nextUpdate"), env, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.:+-TZ", 64),
		DownloadedAt: sanitizeReportValue(metadataValue("DownloadedAt", "downloadedAt"), env, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.:+-TZ", 64),
	}
}

func reportDatabasesFromCache(cacheDir string, env []string) reportDatabases {
	return reportDatabases{
		Vulnerability: readDatabaseMetadata(filepath.Join(cacheDir, "db", "metadata.json"), env),
		Java:          readDatabaseMetadata(filepath.Join(cacheDir, "java-db", "metadata.json"), env),
	}
}

func rejectWithReport(opts admissionOptions, report vulnerabilityReport, outcome, summary string) error {
	report.Outcome = outcome
	if report.Findings == nil {
		report.Findings = make([]reportedFinding, 0)
	}
	if err := writeVulnerabilityReport(opts.vulnerabilityReportPath, report); err != nil {
		return fmt.Errorf("%s; could not write vulnerability report", summary)
	}
	return errors.New(summary)
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

func redactError(err error, env []string) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	for _, entry := range env {
		key, secret, ok := strings.Cut(entry, "=")
		if ok && secret != "" && sensitiveEnvironmentKey(key) {
			value = strings.ReplaceAll(value, secret, "***")
		}
	}
	value = strings.Map(func(char rune) rune {
		if char < 32 || char == 127 {
			return ' '
		}
		return char
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	const maxSummaryBytes = 512
	if len(value) > maxSummaryBytes {
		value = strings.ToValidUTF8(value[:maxSummaryBytes], "") + "..."
	}
	return value
}
