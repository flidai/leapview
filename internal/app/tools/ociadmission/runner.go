package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

type commandRunner struct {
	env []string
}

var (
	credentialHeaderPattern = regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)[^\s,;]+`)
	credentialURLPattern    = regexp.MustCompile(`(?i)(://[^:/\s]+:)[^@/\s]+@`)
	credentialQueryPattern  = regexp.MustCompile(`(?i)(token|password|secret|key)=([^&\s]+)`)
)

func (r commandRunner) verifyLive(opts admissionOptions, policy vulnerabilityPolicy, policyBytes []byte, policySHA256 string, contract *securitypolicy.Exceptions, stdout io.Writer) error {
	report := newVulnerabilityReport(opts, policySHA256, "")
	rejectNotScanned := func(message string) error {
		return rejectWithReport(opts, report, outcomeNotScanned, message)
	}
	gh, ok := findExecutable("gh", r.env)
	if !ok {
		return rejectNotScanned("live verifier gh is missing")
	}
	docker, dockerOK := findExecutable("docker", r.env)
	if !dockerOK {
		return rejectNotScanned("live verifier docker is missing")
	}
	if _, err := r.run(gh, []string{"attestation", "verify", "--help"}, ""); err != nil {
		return rejectNotScanned("live verifier gh attestation is missing")
	}
	ghToken, _ := envValue(r.env, "GH_TOKEN")
	if ghToken == "" {
		ghToken, _ = envValue(r.env, "GITHUB_TOKEN")
	}
	ghEnv := setEnv(r.env, "GH_TOKEN", ghToken)
	attestation, err := r.runWithEnv(gh, []string{"attestation", "verify", "oci://" + opts.image, "--repo", repositoryIdentity, "--signer-workflow", opts.expectedWorkflow, "--source-digest", opts.sourceRevision, "--deny-self-hosted-runners", "--format", "json"}, "", ghEnv)
	if err != nil || !verifyAttestation(attestation, opts.expectedWorkflow, opts.sourceRevision) {
		return rejectNotScanned("attestation identity or source revision is wrong")
	}
	sbom, err := r.run(docker, []string{"buildx", "imagetools", "inspect", opts.image, "--format", "{{ json .SBOM }}"}, "")
	if err != nil || !hasSPDXDocument(sbom) {
		return rejectNotScanned("no SPDX SBOM was discoverable for this digest")
	}
	var imageConfig []byte
	if opts.admissionBundlePath != "" {
		imageConfig, err = r.run(docker, []string{"buildx", "imagetools", "inspect", opts.image, "--format", "{{ json .Image }}"}, "")
		if err != nil {
			return rejectNotScanned("exact image configuration is unavailable")
		}
		if err := verifyBundleImage(opts, imageConfig, sbom); err != nil {
			return rejectNotScanned(err.Error())
		}
	}

	cacheDir, err := os.MkdirTemp("", "ociadmission-trivy-cache-")
	if err != nil {
		return rejectWithReport(opts, report, outcomeScannerError, "pinned vulnerability scanner cache is unavailable")
	}
	defer os.RemoveAll(cacheDir)
	trivyBin, trivyArgs, err := r.trivyCommand(policy, docker, cacheDir)
	if err != nil {
		return rejectWithReport(opts, report, outcomeScannerError, err.Error())
	}
	versionArgs := append([]string{trivyBin}, trivyArgs...)
	versionArgs = append(versionArgs, "version", "--format", "json")
	versionJSON, err := r.runCommandParts(versionArgs)
	if err != nil {
		return rejectWithReport(opts, report, outcomeScannerError, "could not determine trivy version")
	}
	actualVersion, err := scannerVersion(versionJSON)
	if err != nil || actualVersion != policy.ScannerVersion {
		if err == nil {
			report.Scanner.Version = sanitizeReportValue(actualVersion, r.env, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._+-", 64)
			return rejectWithReport(opts, report, outcomeScannerError, "trivy version does not match pinned version")
		}
		return rejectWithReport(opts, report, outcomeScannerError, "could not determine trivy version")
	}
	report.Scanner.Version = actualVersion
	args := append([]string{trivyBin}, trivyArgs...)
	args = append(args, "image", "--quiet", "--format", "json", "--exit-code", "0")
	for _, severity := range policy.Severity {
		args = append(args, "--severity", severity)
	}
	if *policy.IgnoreUnfixed {
		args = append(args, "--ignore-unfixed")
	}
	if opts.platform != "" {
		args = append(args, "--platform", opts.platform)
	}
	args = append(args, opts.image)
	trivyJSON, diagnostic, err := r.runCommandPartsWithDiagnostic(args)
	report.Database = reportDatabasesFromCache(cacheDir, r.env)
	if err != nil {
		summary := "pinned vulnerability scan could not complete"
		if diagnostic != "" {
			summary += ": " + diagnostic
		}
		return rejectWithReport(opts, report, outcomeScannerError, summary)
	}
	if opts.vulnerabilityReportPath != "" || opts.admissionBundlePath != "" {
		db := report.Database.Vulnerability
		if db.Version == "" || db.UpdatedAt == "" || db.NextUpdate == "" || db.DownloadedAt == "" {
			return rejectWithReport(opts, report, outcomeScannerError, "scanner vulnerability database metadata is unavailable")
		}
	}
	parsed, err := parseVulnerabilityReport(trivyJSON, contract, r.env)
	if err != nil {
		return rejectWithReport(opts, report, outcomeInvalidReport, "vulnerability evidence is not machine-readable")
	}
	if opts.admissionBundlePath != "" {
		if err := verifyBundleScan(opts, trivyJSON, sbom); err != nil {
			return rejectWithReport(opts, report, outcomeInvalidReport, err.Error())
		}
	}
	report.Revision = parsed.ImageRevision
	max, err := maxUnresolved(policy.MaxUnresolved)
	if err != nil {
		return rejectWithReport(opts, report, outcomeScannerError, "vulnerability policy is not pinned")
	}
	report.UnresolvedCount = parsed.UnresolvedCount
	report.Findings = parsed.Findings
	report.FindingsTruncated = parsed.FindingsTruncated
	if parsed.UnresolvedCount > max {
		return rejectWithReport(opts, report, outcomeRejected, fmt.Sprintf("vulnerability evidence exceeds policy (%d unresolved findings)", parsed.UnresolvedCount))
	}
	report.Outcome = outcomePassed
	if err := writeVulnerabilityReport(opts.vulnerabilityReportPath, report); err != nil {
		return errors.New("could not write vulnerability report")
	}
	if opts.admissionBundlePath != "" {
		if err := writeAdmissionBundle(opts, r.env, policyBytes, attestation, sbom, imageConfig, trivyJSON, report); err != nil {
			return err
		}
	}
	digest := opts.image[strings.LastIndex(opts.image, "@")+1:]
	vulnerabilityResult := map[string]any{"sha256": policySHA256, "scanner": "trivy", "passed": true}
	if opts.platform != "" {
		vulnerabilityResult["platform"] = opts.platform
	}
	result := map[string]any{
		"schemaVersion": 1, "image": opts.image, "digest": digest, "registryDigest": digest,
		"attestation":         map[string]any{"verified": true, "repository": repositoryIdentity, "workflow": opts.expectedWorkflow, "sourceRevision": opts.sourceRevision},
		"sbom":                map[string]any{"discoverable": true, "predicateType": "https://spdx.dev/Document/v2.3"},
		"vulnerabilityPolicy": vulnerabilityResult,
	}
	return writeResult(opts, r.env, result, stdout)
}

func (r commandRunner) trivyCommand(policy vulnerabilityPolicy, docker, cacheDir string) (string, []string, error) {
	if trivy, ok := findExecutable("trivy", r.env); ok {
		return trivy, []string{"--cache-dir", cacheDir}, nil
	}
	if _, err := r.run(docker, []string{"info"}, ""); err != nil {
		return "", nil, errors.New("pinned trivy verifier cannot access Docker")
	}
	home, ok := envValue(r.env, "HOME")
	if !ok || home == "" {
		home = "/root"
	}
	return docker, []string{"run", "--rm", "--network", "host", "-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", home + "/.docker:/root/.docker:ro", "-v", cacheDir + ":/root/.cache/trivy", policy.ScannerImage, "--cache-dir", "/root/.cache/trivy"}, nil
}

func (r commandRunner) run(name string, args []string, stdin string) ([]byte, error) {
	return r.runWithEnv(name, args, stdin, r.env)
}

func (r commandRunner) runWithEnv(name string, args []string, stdin string, env []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stderr = io.Discard
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return output, err
}

func (r commandRunner) runCommandParts(parts []string) ([]byte, error) {
	if len(parts) == 0 {
		return nil, errors.New("empty command")
	}
	return r.run(parts[0], parts[1:], "")
}

func (r commandRunner) runCommandPartsWithDiagnostic(parts []string) ([]byte, string, error) {
	if len(parts) == 0 {
		return nil, "", errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Env = r.env
	output := boundedDiagnostic{limit: maxVulnerabilityJSONBytes}
	cmd.Stdout = &output
	diagnostic := boundedDiagnostic{limit: 4096}
	cmd.Stderr = &diagnostic
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if output.overflow && err == nil {
		err = errors.New("scanner output exceeds bounded size")
	}
	// Never expose a truncated credential: exact secret matching cannot redact
	// a value cut off at the bounded stderr boundary.
	if diagnostic.overflow {
		return []byte(output.value.String()), "scanner stderr exceeded bounded size", err
	}
	return []byte(output.value.String()), r.redactDiagnostic(diagnostic.value.String()), err
}

type boundedDiagnostic struct {
	value    strings.Builder
	limit    int
	overflow bool
}

func (b *boundedDiagnostic) Write(data []byte) (int, error) {
	length := len(data)
	remaining := b.limit - b.value.Len()
	if remaining > 0 {
		if len(data) > remaining {
			b.overflow = true
			data = data[:remaining]
		}
		_, _ = b.value.Write(data)
	} else if length > 0 {
		b.overflow = true
	}
	return length, nil
}

func (r commandRunner) redactDiagnostic(value string) string {
	for _, entry := range r.env {
		key, secret, ok := strings.Cut(entry, "=")
		if ok && secret != "" && sensitiveEnvironmentKey(key) {
			value = strings.ReplaceAll(value, secret, "***")
		}
	}
	value = credentialHeaderPattern.ReplaceAllString(value, `${1}***`)
	value = credentialURLPattern.ReplaceAllString(value, `${1}***@`)
	value = credentialQueryPattern.ReplaceAllString(value, `${1}=***`)
	value = strings.Map(func(char rune) rune {
		if char < 32 || char == 127 {
			return ' '
		}
		return char
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	const limit = 256
	if len(value) > limit {
		value = strings.ToValidUTF8(value[:limit], "") + "..."
	}
	return value
}

func (r commandRunner) loadExceptionContract(policyPath string) (*securitypolicy.Exceptions, error) {
	git, ok := findExecutable("git", r.env)
	if !ok {
		return nil, nil
	}
	rootBytes, err := r.run(git, []string{"-C", filepath.Dir(policyPath), "rev-parse", "--show-toplevel"}, "")
	if err != nil {
		return nil, nil
	}
	root := strings.TrimSpace(string(rootBytes))
	if root == "" {
		return nil, nil
	}
	if _, err := os.Stat(filepath.Join(root, ".security", "coverage.yaml")); err != nil {
		return nil, nil
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "app", "tools", "securitypolicy", "main.go")); err != nil {
		return nil, nil
	}
	contract, err := securitypolicy.LoadValidatedExceptions(root, time.Now().UTC())
	if err != nil {
		return nil, errors.New("validated exception contract is unavailable")
	}
	return &contract, nil
}
func findExecutable(name string, env []string) (string, bool) {
	if strings.ContainsRune(name, os.PathSeparator) {
		info, err := os.Stat(name)
		return name, err == nil && !info.IsDir() && info.Mode()&0o111 != 0
	}
	pathValue, ok := envValue(env, "PATH")
	if !ok {
		pathValue = os.Getenv("PATH")
	}
	for _, directory := range strings.Split(pathValue, string(os.PathListSeparator)) {
		if directory == "" {
			directory = "."
		}
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, true
		}
	}
	return "", false
}

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix), true
		}
	}
	return "", false
}

func setEnv(env []string, key, value string) []string {
	result := make([]string, 0, len(env)+1)
	found := false
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			if !found {
				result = append(result, prefix+value)
				found = true
			}
			continue
		}
		result = append(result, entry)
	}
	if !found {
		result = append(result, prefix+value)
	}
	return result
}
