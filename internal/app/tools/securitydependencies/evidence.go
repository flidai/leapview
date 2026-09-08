package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// javascriptEvidenceRelativePath is deliberately outside the generated
// scanner output directories. It is the reviewed, repository-owned input for
// the default security check; refresh mode is the only mode that invokes Bun
// or npm.
const javascriptEvidenceRelativePath = ".security/javascript-vulnerability-evidence.json"

const (
	javascriptEvidenceSchema   = "leapview.securitydependencies/javascript-vulnerability-evidence/v1"
	javascriptEvidenceProvider = "npm-advisory-api"

	// Seven days bounds the age of a checked-in scan while allowing the
	// evidence to be refreshed on a normal weekly maintenance cadence.
	javascriptEvidenceMaxLifetime = 7 * 24 * time.Hour
)

type javascriptEvidence struct {
	Schema      string                    `json:"schema"`
	Provider    string                    `json:"provider"`
	GeneratedAt string                    `json:"generated_at"`
	ExpiresAt   string                    `json:"expires_at"`
	Graphs      []javascriptEvidenceGraph `json:"graphs"`
}

type javascriptEvidenceGraph struct {
	ID             string                      `json:"id"`
	Manager        string                      `json:"manager"`
	RuntimeVersion string                      `json:"runtime_version"`
	Scanner        string                      `json:"scanner"`
	ScannerVersion string                      `json:"scanner_version"`
	Command        []string                    `json:"command"`
	Manifest       string                      `json:"manifest"`
	Lockfile       string                      `json:"lockfile"`
	ManifestSHA256 string                      `json:"manifest_sha256"`
	LockfileSHA256 string                      `json:"lockfile_sha256"`
	Findings       []javascriptEvidenceFinding `json:"findings"`
}

type javascriptEvidenceFinding struct {
	Advisory   string `json:"advisory"`
	Dependency string `json:"dependency"`
	Severity   string `json:"severity"`
}

type javascriptEvidenceGraphSpec struct {
	id       string
	provider string
	scanner  string
	command  []string
	manifest string
	lockfile string
}

type npmVulnerabilityCounts struct {
	Info     *int `json:"info"`
	Low      *int `json:"low"`
	Moderate *int `json:"moderate"`
	High     *int `json:"high"`
	Critical *int `json:"critical"`
	Total    *int `json:"total"`
}

func (r *runner) evaluateCheckedInJavaScriptEvidence(buns, npms []string, contract *exceptionContract) error {
	if err := r.requireTrackedJavaScriptEvidence(); err != nil {
		return err
	}
	path := filepath.Join(r.root, javascriptEvidenceRelativePath)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read checked-in JavaScript vulnerability evidence: %w", err)
	}
	evidence, err := decodeJavaScriptEvidence(data)
	if err != nil {
		return fmt.Errorf("checked-in JavaScript vulnerability evidence is invalid: %w", err)
	}
	if err := validateJavaScriptEvidence(evidence, r.root, buns, npms, r.nowUTC()); err != nil {
		return fmt.Errorf("checked-in JavaScript vulnerability evidence is invalid: %w", err)
	}
	return r.evaluateJavaScriptFindings(evidence, contract)
}

func (r *runner) evaluateJavaScriptFindings(evidence javascriptEvidence, contract *exceptionContract) error {
	for _, graph := range evidence.Graphs {
		for _, finding := range graph.Findings {
			if strings.EqualFold(finding.Severity, "critical") {
				identity := findingIdentity{
					Scanner:  graph.Scanner,
					Rule:     finding.Advisory,
					Resource: finding.Dependency,
					Severity: finding.Severity,
				}
				if contract != nil && matches(*contract, identity) {
					return fmt.Errorf("protected Critical JavaScript finding cannot be waived: scanner=%s advisory=%s dependency=%s", identity.Scanner, identity.Rule, identity.Resource)
				}
				return fmt.Errorf("Critical JavaScript dependency finding: scanner=%s advisory=%s dependency=%s", identity.Scanner, identity.Rule, identity.Resource)
			}
		}
	}
	return nil
}

func decodeJavaScriptEvidence(data []byte) (javascriptEvidence, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return javascriptEvidence{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var evidence javascriptEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return javascriptEvidence{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return javascriptEvidence{}, errors.New("multiple JSON values")
		}
		return javascriptEvidence{}, err
	}
	return evidence, nil
}

func validateJavaScriptEvidence(evidence javascriptEvidence, root string, buns, npms []string, now time.Time) error {
	if evidence.Schema != javascriptEvidenceSchema {
		return fmt.Errorf("unsupported schema %q", evidence.Schema)
	}
	if evidence.Provider != javascriptEvidenceProvider {
		return fmt.Errorf("unsupported provider %q", evidence.Provider)
	}
	generatedAt, err := parseEvidenceTimestamp(evidence.GeneratedAt)
	if err != nil {
		return fmt.Errorf("generated_at: %w", err)
	}
	expiresAt, err := parseEvidenceTimestamp(evidence.ExpiresAt)
	if err != nil {
		return fmt.Errorf("expires_at: %w", err)
	}
	now = now.UTC()
	if generatedAt.After(now) {
		return errors.New("generated_at is in the future")
	}
	if !expiresAt.After(generatedAt) {
		return errors.New("expires_at must be after generated_at")
	}
	if expiresAt.Sub(generatedAt) > javascriptEvidenceMaxLifetime {
		return fmt.Errorf("evidence lifetime exceeds %s", javascriptEvidenceMaxLifetime)
	}
	if !expiresAt.After(now) {
		return errors.New("evidence is expired")
	}

	specs, err := expectedJavaScriptGraphSpecs(root, buns, npms)
	if err != nil {
		return err
	}
	if evidence.Graphs == nil {
		return errors.New("graphs is required")
	}
	if len(evidence.Graphs) != len(specs) {
		return fmt.Errorf("graphs contain %d entries, want exactly %d discovered graphs", len(evidence.Graphs), len(specs))
	}
	expected := make(map[string]javascriptEvidenceGraphSpec, len(specs))
	for _, spec := range specs {
		expected[spec.id] = spec
	}
	seen := make(map[string]struct{}, len(specs))
	previousID := ""
	for _, graph := range evidence.Graphs {
		if graph.ID <= previousID {
			return errors.New("graphs must be strictly sorted by id and unique")
		}
		previousID = graph.ID
		if _, ok := seen[graph.ID]; ok {
			return fmt.Errorf("duplicate graph %q", graph.ID)
		}
		seen[graph.ID] = struct{}{}
		spec, ok := expected[graph.ID]
		if !ok {
			return fmt.Errorf("unexpected graph %q", graph.ID)
		}
		if err := validateJavaScriptGraph(root, spec, graph); err != nil {
			return fmt.Errorf("graph %s: %w", graph.ID, err)
		}
	}
	for _, spec := range specs {
		if _, ok := seen[spec.id]; !ok {
			return fmt.Errorf("missing graph %q", spec.id)
		}
	}
	return nil
}

func parseEvidenceTimestamp(value string) (time.Time, error) {
	if value == "" || strings.TrimSpace(value) != value || !strings.HasSuffix(value, "Z") {
		return time.Time{}, errors.New("must be canonical UTC RFC3339")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("must be canonical UTC RFC3339")
	}
	return parsed.UTC(), nil
}

func validateJavaScriptGraph(root string, spec javascriptEvidenceGraphSpec, graph javascriptEvidenceGraph) error {
	if graph.Manager != spec.provider {
		return fmt.Errorf("manager %q does not match %q", graph.Manager, spec.provider)
	}
	if graph.RuntimeVersion == "" || strings.TrimSpace(graph.RuntimeVersion) != graph.RuntimeVersion || strings.ContainsAny(graph.RuntimeVersion, "\r\n") {
		return errors.New("runtime identity/version is missing or malformed")
	}
	if graph.Scanner != spec.scanner {
		return fmt.Errorf("scanner %q does not match %q", graph.Scanner, spec.scanner)
	}
	if graph.ScannerVersion == "" || strings.TrimSpace(graph.ScannerVersion) != graph.ScannerVersion || strings.ContainsAny(graph.ScannerVersion, "\r\n") {
		return errors.New("scanner identity/version is missing or malformed")
	}
	if !equalStringSlices(graph.Command, spec.command) {
		return fmt.Errorf("command %v does not match %v", graph.Command, spec.command)
	}
	if graph.Manifest != spec.manifest || graph.Lockfile != spec.lockfile {
		return errors.New("manifest and lockfile do not match the discovered sibling pair")
	}
	if !validDigest(graph.ManifestSHA256) || !validDigest(graph.LockfileSHA256) {
		return errors.New("manifest_sha256 and lockfile_sha256 must be lowercase 64-character SHA-256 digests")
	}
	manifestDigest, err := digestFile(filepath.Join(root, filepath.FromSlash(graph.Manifest)))
	if err != nil {
		return fmt.Errorf("read manifest for digest verification: %w", err)
	}
	if manifestDigest != graph.ManifestSHA256 {
		return errors.New("manifest digest mismatch")
	}
	lockDigest, err := digestFile(filepath.Join(root, filepath.FromSlash(graph.Lockfile)))
	if err != nil {
		return fmt.Errorf("read lockfile for digest verification: %w", err)
	}
	if lockDigest != graph.LockfileSHA256 {
		return errors.New("lockfile digest mismatch")
	}
	if graph.Findings == nil {
		return errors.New("findings is required (use [] when there are no findings)")
	}
	previousKey := ""
	seenFindings := make(map[string]struct{}, len(graph.Findings))
	for _, finding := range graph.Findings {
		if finding.Advisory == "" || strings.TrimSpace(finding.Advisory) != finding.Advisory || finding.Dependency == "" || strings.TrimSpace(finding.Dependency) != finding.Dependency {
			return errors.New("finding advisory and dependency are required")
		}
		if !usableBunSeverity(finding.Severity) || strings.ToLower(finding.Severity) != finding.Severity {
			return fmt.Errorf("finding severity %q is invalid", finding.Severity)
		}
		identityKey := finding.Dependency + "\x00" + finding.Advisory
		if _, ok := seenFindings[identityKey]; ok {
			return errors.New("findings contain duplicate dependency/advisory identities")
		}
		seenFindings[identityKey] = struct{}{}
		key := identityKey + "\x00" + finding.Severity
		if key <= previousKey {
			return errors.New("findings must be strictly sorted and unique")
		}
		previousKey = key
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func digestFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func expectedJavaScriptGraphSpecs(root string, buns, npms []string) ([]javascriptEvidenceGraphSpec, error) {
	specs := make([]javascriptEvidenceGraphSpec, 0, len(buns)+len(npms))
	appendSpec := func(lockFile, provider, scanner string, command []string) error {
		manifest := filepath.Join(filepath.Dir(lockFile), "package.json")
		files := []struct{ label, path string }{{"lockfile", lockFile}, {"manifest", manifest}}
		for _, file := range files {
			info, statErr := os.Lstat(file.path)
			if statErr != nil {
				return fmt.Errorf("%s %s is unavailable: %w", file.label, file.path, statErr)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s %s must be a regular file", file.label, file.path)
			}
		}
		relLock, err := filepath.Rel(root, lockFile)
		if err != nil || filepath.IsAbs(relLock) || relLock == ".." || strings.HasPrefix(relLock, ".."+string(filepath.Separator)) {
			return fmt.Errorf("lockfile %s is outside repository root", lockFile)
		}
		relManifest, err := filepath.Rel(root, manifest)
		if err != nil {
			return err
		}
		specs = append(specs, javascriptEvidenceGraphSpec{
			id:       provider + ":" + filepath.ToSlash(relLock),
			provider: provider,
			scanner:  scanner,
			command:  command,
			manifest: filepath.ToSlash(relManifest),
			lockfile: filepath.ToSlash(relLock),
		})
		return nil
	}
	for _, lockFile := range buns {
		if err := appendSpec(lockFile, "bun", "bun-audit", []string{"bun", "audit", "--audit-level", auditLevel, "--json"}); err != nil {
			return nil, err
		}
	}
	for _, lockFile := range npms {
		if err := appendSpec(lockFile, "npm", "npm-audit", []string{"npm", "audit", "--package-lock-only", "--audit-level=" + auditLevel, "--ignore-scripts", "--json"}); err != nil {
			return nil, err
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].id < specs[j].id })
	return specs, nil
}

func (r *runner) refreshJavaScriptEvidence(buns, npms []string) (javascriptEvidence, error) {
	specs, err := expectedJavaScriptGraphSpecs(r.root, buns, npms)
	if err != nil {
		return javascriptEvidence{}, err
	}
	graphs := make([]javascriptEvidenceGraph, 0, len(specs))
	for _, spec := range specs {
		manifestBefore, err := digestFile(filepath.Join(r.root, filepath.FromSlash(spec.manifest)))
		if err != nil {
			return javascriptEvidence{}, err
		}
		lockBefore, err := digestFile(filepath.Join(r.root, filepath.FromSlash(spec.lockfile)))
		if err != nil {
			return javascriptEvidence{}, err
		}
		var graph javascriptEvidenceGraph
		switch spec.provider {
		case "bun":
			graph, err = r.refreshBunGraph(spec)
		case "npm":
			graph, err = r.refreshNPMGraph(spec)
		default:
			err = fmt.Errorf("unsupported JavaScript provider %q", spec.provider)
		}
		if err != nil {
			return javascriptEvidence{}, err
		}
		if graph.ManifestSHA256 != manifestBefore || graph.LockfileSHA256 != lockBefore {
			return javascriptEvidence{}, fmt.Errorf("graph %s manifest or lockfile changed during refresh", spec.id)
		}
		graphs = append(graphs, graph)
	}
	now := r.nowUTC()
	evidence := javascriptEvidence{
		Schema:      javascriptEvidenceSchema,
		Provider:    javascriptEvidenceProvider,
		GeneratedAt: now.Format(time.RFC3339Nano),
		ExpiresAt:   now.Add(javascriptEvidenceMaxLifetime).Format(time.RFC3339Nano),
		Graphs:      graphs,
	}
	if err := validateJavaScriptEvidence(evidence, r.root, buns, npms, now); err != nil {
		return javascriptEvidence{}, err
	}
	return evidence, nil
}

func (r *runner) refreshBunGraph(spec javascriptEvidenceGraphSpec) (javascriptEvidenceGraph, error) {
	dir := filepath.Dir(filepath.Join(r.root, filepath.FromSlash(spec.lockfile)))
	version, err := r.scannerVersion("bun", dir, r.runBunCommand)
	if err != nil {
		return javascriptEvidenceGraph{}, err
	}
	args := []string{"audit", "--audit-level", auditLevel, "--json"}
	result := r.runBunCommand(dir, args...)
	if isBunLifecycleError(result) {
		return javascriptEvidenceGraph{}, commandError("bun audit", dir, result)
	}
	count, critical, parseErr := bunFindingCounts(result.stdout)
	if critical == 0 && r.shouldRetryBun(result) {
		r.bunRetryUsed = true
		fmt.Fprintf(r.stdout, "bun audit %s: retrying once after transport failure (%s backoff)\n", dir, bunRetryBackoff)
		r.waitForBunRetry()
		result = r.runBunCommand(dir, args...)
		if isBunLifecycleError(result) {
			return javascriptEvidenceGraph{}, commandError("bun audit", dir, result)
		}
		count, critical, parseErr = bunFindingCounts(result.stdout)
	}
	if !bunAuditStderrIsOnlyBanner(result.stderr, version) {
		return javascriptEvidenceGraph{}, scannerDiagnosticError("bun audit", dir, result)
	}
	if result.status != 0 && result.status != 1 {
		return javascriptEvidenceGraph{}, statusError("bun audit", dir, result.status)
	}
	if parseErr != nil {
		return javascriptEvidenceGraph{}, fmt.Errorf("bun audit %s output is malformed: %w", dir, parseErr)
	}
	if isBunTransportFailure(result) {
		return javascriptEvidenceGraph{}, statusError("bun audit", dir, result.status)
	}
	if critical == 0 && result.status != 0 && count == 0 {
		return javascriptEvidenceGraph{}, commandError("bun audit", dir, result)
	}
	findings, err := parseBunEvidenceFindings(result.stdout)
	if err != nil {
		return javascriptEvidenceGraph{}, fmt.Errorf("bun audit %s output is malformed: %w", dir, err)
	}
	return r.makeEvidenceGraph(spec, version, findings)
}

func bunAuditStderrIsOnlyBanner(stderr []byte, version string) bool {
	trimmed := bytes.TrimSpace(stderr)
	if len(trimmed) == 0 {
		return true
	}
	plain := ansiEscapePattern.ReplaceAll(trimmed, nil)
	pattern := `^bun audit v` + regexp.QuoteMeta(version) + ` \([0-9a-f]{7,64}\)$`
	matched, err := regexp.Match(pattern, plain)
	return err == nil && matched
}

func (r *runner) refreshNPMGraph(spec javascriptEvidenceGraphSpec) (javascriptEvidenceGraph, error) {
	dir := filepath.Dir(filepath.Join(r.root, filepath.FromSlash(spec.lockfile)))
	version, err := r.scannerVersion("npm", dir, r.runNPMCommand)
	if err != nil {
		return javascriptEvidenceGraph{}, err
	}
	result := r.runNPMCommand(dir, spec.command[1:]...)
	if isBunLifecycleError(result) {
		return javascriptEvidenceGraph{}, commandError("npm audit", dir, result)
	}
	if len(bytes.TrimSpace(result.stderr)) != 0 {
		return javascriptEvidenceGraph{}, scannerDiagnosticError("npm audit", dir, result)
	}
	if result.status != 0 && result.status != 1 {
		return javascriptEvidenceGraph{}, statusError("npm audit", dir, result.status)
	}
	findings, err := parseNPMEvidenceFindings(result.stdout)
	if err != nil {
		return javascriptEvidenceGraph{}, fmt.Errorf("npm audit %s output is malformed: %w", dir, err)
	}
	if result.status != 0 && len(findings) == 0 {
		return javascriptEvidenceGraph{}, commandError("npm audit", dir, result)
	}
	return r.makeEvidenceGraph(spec, version, findings)
}

func (r *runner) scannerVersion(name, dir string, command func(string, ...string) commandResult) (string, error) {
	result := command(dir, "--version")
	if isBunLifecycleError(result) {
		return "", commandError(name+" --version", dir, result)
	}
	if len(bytes.TrimSpace(result.stderr)) != 0 {
		return "", scannerDiagnosticError(name+" --version", dir, result)
	}
	if result.err != nil || result.status != 0 {
		return "", commandError(name+" --version", dir, result)
	}
	version := strings.TrimSpace(string(result.stdout))
	if version == "" || strings.ContainsAny(version, "\r\n") {
		return "", fmt.Errorf("%s %s returned an unusable version identity", name, dir)
	}
	return version, nil
}

func scannerDiagnosticError(scanner, dir string, result commandResult) error {
	if result.status != 0 {
		return fmt.Errorf("%s %s emitted diagnostics on stderr (status %d)", scanner, dir, result.status)
	}
	return fmt.Errorf("%s %s emitted diagnostics on stderr", scanner, dir)
}

func (r *runner) makeEvidenceGraph(spec javascriptEvidenceGraphSpec, version string, findings []javascriptEvidenceFinding) (javascriptEvidenceGraph, error) {
	manifestDigest, err := digestFile(filepath.Join(r.root, filepath.FromSlash(spec.manifest)))
	if err != nil {
		return javascriptEvidenceGraph{}, err
	}
	lockDigest, err := digestFile(filepath.Join(r.root, filepath.FromSlash(spec.lockfile)))
	if err != nil {
		return javascriptEvidenceGraph{}, err
	}
	sortJavaScriptFindings(findings)
	return javascriptEvidenceGraph{
		ID:             spec.id,
		Manager:        spec.provider,
		RuntimeVersion: version,
		Scanner:        spec.scanner,
		ScannerVersion: version,
		Command:        append([]string(nil), spec.command...),
		Manifest:       spec.manifest,
		Lockfile:       spec.lockfile,
		ManifestSHA256: manifestDigest,
		LockfileSHA256: lockDigest,
		Findings:       findings,
	}, nil
}

func parseBunEvidenceFindings(data []byte) ([]javascriptEvidenceFinding, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	var packages map[string]json.RawMessage
	if err := json.Unmarshal(data, &packages); err != nil || packages == nil {
		if err == nil {
			err = errors.New("expected object")
		}
		return nil, err
	}
	findings := make([]javascriptEvidenceFinding, 0)
	for dependency, rawPackage := range packages {
		if dependency == "" || bytes.Equal(bytes.TrimSpace(rawPackage), []byte("null")) {
			return nil, errors.New("package findings are not an array")
		}
		var rawFindings []json.RawMessage
		if err := json.Unmarshal(rawPackage, &rawFindings); err != nil || rawFindings == nil {
			if err == nil {
				err = errors.New("package findings are not an array")
			}
			return nil, err
		}
		if len(rawFindings) == 0 {
			return nil, errors.New("package advisory array is empty")
		}
		for _, raw := range rawFindings {
			var shape map[string]json.RawMessage
			if err := json.Unmarshal(raw, &shape); err != nil || shape == nil {
				return nil, errors.New("finding is not an object")
			}
			var severity string
			severityRaw, ok := shape["severity"]
			if !ok || json.Unmarshal(severityRaw, &severity) != nil || !usableBunSeverity(severity) {
				return nil, errors.New("finding severity is not usable")
			}
			var id json.RawMessage
			_ = json.Unmarshal(shape["id"], &id)
			var url string
			_ = json.Unmarshal(shape["url"], &url)
			advisory := ghsaPattern.FindString(url)
			if advisory == "" {
				advisory = jsonScalar(id)
			}
			if advisory == "" {
				advisory = strings.TrimSpace(url)
			}
			if advisory == "" {
				return nil, errors.New("finding advisory identity is missing")
			}
			findings = append(findings, javascriptEvidenceFinding{Advisory: advisory, Dependency: dependency, Severity: strings.ToLower(severity)})
		}
	}
	sortJavaScriptFindings(findings)
	return findings, nil
}

func parseNPMEvidenceFindings(data []byte) ([]javascriptEvidenceFinding, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	var root struct {
		Metadata struct {
			Dependencies struct {
				Total *int `json:"total"`
			} `json:"dependencies"`
			Vulnerabilities npmVulnerabilityCounts `json:"vulnerabilities"`
		} `json:"metadata"`
		Vulnerabilities json.RawMessage `json:"vulnerabilities"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if root.Metadata.Dependencies.Total == nil || *root.Metadata.Dependencies.Total <= 0 {
		return nil, errors.New("metadata.dependencies.total is required")
	}
	if len(root.Vulnerabilities) == 0 || bytes.Equal(bytes.TrimSpace(root.Vulnerabilities), []byte("null")) {
		return nil, errors.New("vulnerabilities is required")
	}
	var vulnerabilities map[string]json.RawMessage
	if err := json.Unmarshal(root.Vulnerabilities, &vulnerabilities); err != nil || vulnerabilities == nil {
		if err == nil {
			err = errors.New("vulnerabilities is not an object")
		}
		return nil, err
	}
	if err := validateNPMVulnerabilityCounts(root.Metadata.Vulnerabilities, vulnerabilities); err != nil {
		return nil, err
	}
	findings := make([]javascriptEvidenceFinding, 0)
	for dependency, rawVulnerability := range vulnerabilities {
		if dependency == "" {
			return nil, errors.New("npm vulnerability dependency is empty")
		}
		var vulnerabilityShape map[string]json.RawMessage
		if err := json.Unmarshal(rawVulnerability, &vulnerabilityShape); err != nil || vulnerabilityShape == nil {
			return nil, errors.New("npm vulnerability is not an object")
		}
		var vulnerability struct {
			Severity string            `json:"severity"`
			Via      []json.RawMessage `json:"via"`
		}
		if err := json.Unmarshal(rawVulnerability, &vulnerability); err != nil {
			return nil, err
		}
		if !usableBunSeverity(vulnerability.Severity) {
			return nil, errors.New("npm finding severity is not usable")
		}
		if len(vulnerability.Via) == 0 {
			return nil, errors.New("npm vulnerability via is required")
		}
		foundAdvisory := false
		for _, rawVia := range vulnerability.Via {
			var viaString string
			if err := json.Unmarshal(rawVia, &viaString); err == nil {
				if viaString == "" || strings.TrimSpace(viaString) != viaString {
					return nil, errors.New("npm advisory identity is missing")
				}
				findings = append(findings, javascriptEvidenceFinding{Advisory: viaString, Dependency: dependency, Severity: strings.ToLower(vulnerability.Severity)})
				foundAdvisory = true
				continue
			}
			var via map[string]json.RawMessage
			if err := json.Unmarshal(rawVia, &via); err != nil || via == nil {
				return nil, errors.New("npm advisory is not an object or string")
			}
			advisory := jsonScalar(via["source"])
			if advisory == "" {
				advisory = jsonScalar(via["id"])
			}
			if advisory == "" || strings.TrimSpace(advisory) != advisory {
				return nil, errors.New("npm advisory identity is missing")
			}
			severity := jsonScalar(via["severity"])
			if severity == "" {
				severity = vulnerability.Severity
			}
			if !usableBunSeverity(severity) {
				return nil, errors.New("npm finding severity is not usable")
			}
			if severityRank(vulnerability.Severity) > severityRank(severity) {
				severity = vulnerability.Severity
			}
			findings = append(findings, javascriptEvidenceFinding{Advisory: advisory, Dependency: dependency, Severity: strings.ToLower(severity)})
			foundAdvisory = true
		}
		if !foundAdvisory {
			return nil, errors.New("npm vulnerability via contains no advisory identity")
		}
	}
	sortJavaScriptFindings(findings)
	return findings, nil
}

func validateNPMVulnerabilityCounts(summary npmVulnerabilityCounts, vulnerabilities map[string]json.RawMessage) error {
	counts := []struct {
		severity string
		count    *int
	}{
		{"info", summary.Info}, {"low", summary.Low}, {"moderate", summary.Moderate},
		{"high", summary.High}, {"critical", summary.Critical},
	}
	total := 0
	for _, entry := range counts {
		if entry.count == nil || *entry.count < 0 {
			return fmt.Errorf("metadata.vulnerabilities.%s is missing or invalid", entry.severity)
		}
		total += *entry.count
	}
	if summary.Total == nil || *summary.Total < 0 || *summary.Total != total || *summary.Total != len(vulnerabilities) {
		return errors.New("metadata.vulnerabilities totals do not match vulnerability records")
	}
	actual := map[string]int{}
	for _, raw := range vulnerabilities {
		var shape struct {
			Severity string `json:"severity"`
		}
		if err := json.Unmarshal(raw, &shape); err != nil || !usableBunSeverity(shape.Severity) {
			return errors.New("npm vulnerability severity is not usable")
		}
		actual[strings.ToLower(shape.Severity)]++
	}
	for _, entry := range counts {
		if actual[entry.severity] != *entry.count {
			return fmt.Errorf("metadata.vulnerabilities.%s does not match vulnerability records", entry.severity)
		}
	}
	return nil
}

func severityRank(severity string) int {
	switch strings.ToLower(severity) {
	case "low":
		return 1
	case "moderate":
		return 2
	case "high":
		return 3
	case "critical":
		return 4
	default:
		return 0
	}
}

func sortJavaScriptFindings(findings []javascriptEvidenceFinding) {
	sort.Slice(findings, func(i, j int) bool {
		left := findings[i].Dependency + "\x00" + findings[i].Advisory + "\x00" + findings[i].Severity
		right := findings[j].Dependency + "\x00" + findings[j].Advisory + "\x00" + findings[j].Severity
		return left < right
	})
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func writeJavaScriptEvidenceAtomic(path string, evidence javascriptEvidence) error {
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".javascript-vulnerability-evidence-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	return nil
}
