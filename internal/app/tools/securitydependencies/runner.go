// Command securitydependencies runs the repository's dependency vulnerability
// scanners.  It is intentionally repository-owned so local checks and CI use
// the same discovery, exception, and failure contract.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

const (
	govulncheckVersion = "v1.6.0"
	auditLevel         = "critical"
	defaultTimeout     = 10 * time.Minute
	rootLookupTimeout  = 30 * time.Second
	bunRetryBackoff    = 5 * time.Second
	maxDiagnosticBytes = 64 << 10
)

var ghsaPattern = regexp.MustCompile(`GHSA-[A-Za-z0-9-]+`)
var sensitiveDiagnostic = regexp.MustCompile(`(?i)(\b(?:token|password|secret|api[_-]?key|authorization|private[_-]?key)\b\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,}]+)`)

type exceptionContract = securitypolicy.Exceptions
type findingIdentity = securitypolicy.Finding

func matches(contract exceptionContract, finding findingIdentity) bool {
	_, ok := contract.Match(finding)
	return ok
}

type commandResult struct {
	stdout []byte
	stderr []byte
	status int
	err    error
}

type runner struct {
	root          string
	timeout       time.Duration
	stdout        io.Writer
	stderr        io.Writer
	bunRetryUsed  bool
	bunRetrySleep func(time.Duration)
	bunCommand    func(string, ...string) commandResult
}

// commandOutput is used instead of passing scanner output directly to a
// terminal.  A fixed limit keeps a broken scanner from flooding CI logs.
func commandOutput(data []byte) []byte {
	redact := func(value []byte) []byte {
		value = bytes.ReplaceAll(value, []byte{0}, []byte("?"))
		return sensitiveDiagnostic.ReplaceAll(value, []byte("$1[REDACTED]"))
	}
	if len(data) <= maxDiagnosticBytes {
		return redact(data)
	}
	truncated := append([]byte(nil), data[:maxDiagnosticBytes]...)
	truncated = redact(truncated)
	return append(truncated, []byte("\n[scanner output truncated]\n")...)
}

func (r *runner) command(dir, name string, args ...string) commandResult {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	status := 0
	if err != nil {
		status = 1
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			status = exitError.ExitCode()
			if status < 0 {
				status = 1
			}
		}
		if ctx.Err() != nil {
			err = fmt.Errorf("%w (command timed out after %s)", ctx.Err(), r.timeout)
		}
	}
	return commandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), status: status, err: err}
}

func (r *runner) run() error {
	// A runner corresponds to one scanner process invocation. Keep the Bun
	// transport retry budget process-wide rather than spending one retry per
	// discovered lockfile.
	r.bunRetryUsed = false
	contract, err := r.loadContract()
	if err != nil {
		return err
	}
	modules, buns, npms, err := discover(r.root)
	if err != nil {
		return err
	}
	for _, path := range modules {
		if err := r.scanGo(path, contract); err != nil {
			return err
		}
	}
	for _, path := range buns {
		if err := r.scanBun(path, contract); err != nil {
			return err
		}
	}
	for _, path := range npms {
		if err := r.scanNPM(path, contract); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) loadContract() (*exceptionContract, error) {
	coverage := filepath.Join(r.root, ".security", "coverage.yaml")
	policy := filepath.Join(r.root, "internal", "app", "tools", "securitypolicy", "main.go")
	if !fileExists(coverage) || !fileExists(policy) {
		return nil, nil
	}
	fmt.Fprintln(r.stdout, "dependency security: validating exception contract")
	contract, err := securitypolicy.LoadValidatedExceptions(r.root, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("validated exception contract is unavailable: %w", err)
	}
	return &contract, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func discover(root string) (modules, buns, npms []string, err error) {
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != root {
			relative, relativeErr := filepath.Rel(root, path)
			if relativeErr != nil {
				return relativeErr
			}
			if excludedPath(relative) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		switch info.Name() {
		case "go.mod":
			modules = append(modules, path)
		case "bun.lock":
			buns = append(buns, path)
		case "package-lock.json":
			npms = append(npms, path)
		}
		return nil
	})
	sort.Strings(modules)
	sort.Strings(buns)
	sort.Strings(npms)
	return
}

func excludedPath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		switch part {
		case ".data", ".tmp", "node_modules", "testdata":
			return true
		}
	}
	return false
}

func (r *runner) scanGo(moduleFile string, contract *exceptionContract) error {
	dir := filepath.Dir(moduleFile)
	fmt.Fprintf(r.stdout, "govulncheck %s\n", dir)
	args := []string{"run", "golang.org/x/vuln/cmd/govulncheck@" + govulncheckVersion}
	if contract != nil {
		args = append(args, "-json")
	}
	args = append(args, "./...")
	result := r.command(dir, "go", args...)
	if contract == nil {
		r.emitDirect(result)
		return commandError("govulncheck", dir, result)
	}
	if result.status == 0 {
		return nil
	}
	if allGovulnFindingsWaived(result.stdout, *contract) {
		fmt.Fprintf(r.stdout, "govulncheck %s: all findings match exact, active exceptions\n", dir)
		return nil
	}
	r.emitFailure(result)
	return commandError("govulncheck", dir, result)
}

func (r *runner) scanBun(lockFile string, contract *exceptionContract) error {
	dir := filepath.Dir(lockFile)
	fmt.Fprintf(r.stdout, "bun audit %s\n", dir)
	args := []string{"audit", "--audit-level", auditLevel}
	if contract != nil {
		args = append(args, "--json")
	}
	result := r.runBunCommand(dir, args...)
	if contract == nil {
		r.emitDirect(result)
		return commandError("bun audit", dir, result)
	}
	if isBunLifecycleError(result) {
		r.emitFailure(result)
		return commandError("bun audit", dir, result)
	}
	count, critical, err := bunFindingCounts(result.stdout)
	if critical == 0 && r.shouldRetryBun(result) {
		r.bunRetryUsed = true
		fmt.Fprintf(r.stdout, "bun audit %s: retrying once after transport failure (%s backoff)\n", dir, bunRetryBackoff)
		r.waitForBunRetry()
		result = r.runBunCommand(dir, args...)
		if isBunLifecycleError(result) {
			r.emitFailure(result)
			return commandError("bun audit", dir, result)
		}
		count, critical, err = bunFindingCounts(result.stdout)
	}
	if err != nil {
		r.emitFailure(result)
		fmt.Fprintf(r.stderr, "bun audit %s: scanner output is not valid JSON\n", dir)
		return errors.New("bun audit output is malformed")
	}
	if critical == 0 && isBunTransportFailure(result) {
		r.emitFailure(result)
		fmt.Fprintf(r.stderr, "bun audit %s: transport failure exhausted the bounded retry budget\n", dir)
		return statusError("bun audit", dir, result.status)
	}
	if critical == 0 {
		if result.status == 1 && count > 0 {
			fmt.Fprintf(r.stdout, "bun audit %s: no Critical findings (%d below threshold)\n", dir, count)
			return nil
		}
		if result.status != 0 {
			r.emitFailure(result)
			fmt.Fprintf(r.stderr, "bun audit %s: scanner failed without decoded blocking findings\n", dir)
			return commandError("bun audit", dir, result)
		}
		fmt.Fprintf(r.stdout, "bun audit %s: no Critical findings\n", dir)
		return nil
	}
	status := result.status
	if status == 0 {
		status = 1
	}
	if allBunFindingsWaived(result.stdout, *contract) {
		fmt.Fprintf(r.stdout, "bun audit %s: all findings match exact, active exceptions\n", dir)
		return nil
	}
	r.emitFailure(result)
	return statusError("bun audit", dir, status)
}

func (r *runner) shouldRetryBun(result commandResult) bool {
	return !r.bunRetryUsed && isBunTransportFailure(result)
}

func (r *runner) runBunCommand(dir string, args ...string) commandResult {
	if r.bunCommand != nil {
		return r.bunCommand(dir, args...)
	}
	return r.command(dir, "bun", args...)
}

func (r *runner) waitForBunRetry() {
	if r.bunRetrySleep != nil {
		r.bunRetrySleep(bunRetryBackoff)
		return
	}
	time.Sleep(bunRetryBackoff)
}

func isBunTransportFailure(result commandResult) bool {
	return !isBunLifecycleError(result) &&
		(hasBunTransportSignature(result.stdout) || hasBunTransportSignature(result.stderr))
}

func isBunLifecycleError(result commandResult) bool {
	return errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded)
}

func hasBunTransportSignature(data []byte) bool {
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if bytes.Equal(line, []byte("Timeout: audit request failed")) || bytes.Equal(line, []byte("ConnectionClosed: audit request failed")) {
			return true
		}
	}
	return false
}

func (r *runner) scanNPM(lockFile string, contract *exceptionContract) error {
	dir := filepath.Dir(lockFile)
	fmt.Fprintf(r.stdout, "npm audit %s\n", dir)
	args := []string{"audit", "--package-lock-only", "--audit-level=" + auditLevel, "--ignore-scripts"}
	if contract != nil {
		args = append(args, "--json")
	}
	result := r.command(dir, "npm", args...)
	if contract == nil {
		r.emitDirect(result)
		return commandError("npm audit", dir, result)
	}
	if result.status == 0 {
		return nil
	}
	if allNPMFindingsWaived(result.stdout, *contract) {
		fmt.Fprintf(r.stdout, "npm audit %s: all findings match exact, active exceptions\n", dir)
		return nil
	}
	r.emitFailure(result)
	return commandError("npm audit", dir, result)
}

func (r *runner) emitDirect(result commandResult) {
	if len(result.stdout) > 0 {
		_, _ = r.stdout.Write(commandOutput(result.stdout))
	}
	if len(result.stderr) > 0 {
		_, _ = r.stderr.Write(commandOutput(result.stderr))
	}
}

func (r *runner) emitFailure(result commandResult) {
	if len(result.stderr) > 0 {
		_, _ = r.stdout.Write(commandOutput(result.stderr))
	}
	if len(result.stdout) > 0 {
		_, _ = r.stdout.Write(commandOutput(result.stdout))
	}
}

func commandError(scanner, dir string, result commandResult) error {
	if result.err != nil {
		return fmt.Errorf("%s %s failed: %w", scanner, dir, result.err)
	}
	if result.status == 0 {
		return nil
	}
	return statusError(scanner, dir, result.status)
}

func statusError(scanner, dir string, status int) error {
	if status == 0 {
		status = 1
	}
	return fmt.Errorf("%s %s failed with status %d", scanner, dir, status)
}

type govulnEnvelope struct {
	Finding *govulnFinding `json:"finding"`
}

type govulnFinding struct {
	OSV   json.RawMessage `json:"osv"`
	Trace []struct {
		Module json.RawMessage `json:"module"`
	} `json:"trace"`
	Severity json.RawMessage `json:"severity"`
}

func allGovulnFindingsWaived(data []byte, contract exceptionContract) bool {
	findings, ok := decodeGovulnFindings(data)
	if !ok || len(findings) == 0 {
		return false
	}
	for _, finding := range findings {
		if finding.OSV == nil || len(finding.Trace) == 0 || finding.Trace[0].Module == nil || finding.Severity == nil {
			return false
		}
		identity := findingIdentity{Scanner: "govulncheck", Rule: jsonScalar(finding.OSV), Resource: jsonScalar(finding.Trace[0].Module), Severity: jsonScalar(finding.Severity)}
		if identity.Rule == "" || identity.Resource == "" || identity.Severity == "" || !matches(contract, identity) {
			return false
		}
	}
	return true
}

func decodeGovulnFindings(data []byte) ([]govulnFinding, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var findings []govulnFinding
	for {
		var envelope govulnEnvelope
		err := decoder.Decode(&envelope)
		if errors.Is(err, io.EOF) {
			return findings, true
		}
		if err != nil {
			return nil, false
		}
		if envelope.Finding != nil {
			findings = append(findings, *envelope.Finding)
		}
	}
}

func bunFindingCounts(data []byte) (findingCount, criticalCount int, err error) {
	var packages map[string]json.RawMessage
	if err = json.Unmarshal(data, &packages); err != nil || packages == nil {
		if err == nil {
			err = errors.New("expected object")
		}
		return
	}
	for _, rawPackage := range packages {
		if bytes.Equal(bytes.TrimSpace(rawPackage), []byte("null")) {
			return findingCount, criticalCount, errors.New("package findings are not an array")
		}
		var rawFindings []json.RawMessage
		if err = json.Unmarshal(rawPackage, &rawFindings); err != nil || rawFindings == nil {
			if err == nil {
				err = errors.New("package findings are not an array")
			}
			return
		}
		for _, raw := range rawFindings {
			var finding struct {
				Severity string `json:"severity"`
			}
			if err = json.Unmarshal(raw, &finding); err != nil || !json.Valid(raw) {
				if err == nil {
					err = errors.New("finding is not an object")
				}
				return
			}
			// Unmarshalling a scalar into the struct fails; an object without a
			// severity is malformed rather than an advisory below the threshold.
			var shape map[string]json.RawMessage
			if err = json.Unmarshal(raw, &shape); err != nil || shape == nil {
				if err == nil {
					err = errors.New("finding is not an object")
				}
				return
			}
			severity, present := shape["severity"]
			if !present || json.Unmarshal(severity, &finding.Severity) != nil {
				return findingCount, criticalCount, errors.New("finding severity is not a usable string")
			}
			if !usableBunSeverity(finding.Severity) {
				return findingCount, criticalCount, errors.New("finding severity is not usable")
			}
			findingCount++
			if strings.EqualFold(finding.Severity, "critical") {
				criticalCount++
			}
		}
	}
	return findingCount, criticalCount, nil
}

func usableBunSeverity(severity string) bool {
	if severity == "" {
		return false
	}
	switch strings.ToLower(severity) {
	case "low", "moderate", "high", "critical":
		return true
	default:
		return false
	}
}

func allBunFindingsWaived(data []byte, contract exceptionContract) bool {
	var packages map[string][]json.RawMessage
	if json.Unmarshal(data, &packages) != nil || packages == nil {
		return false
	}
	found := false
	for resource, rawFindings := range packages {
		for _, raw := range rawFindings {
			var finding struct {
				ID       json.RawMessage `json:"id"`
				Severity string          `json:"severity"`
				URL      string          `json:"url"`
			}
			if json.Unmarshal(raw, &finding) != nil || !strings.EqualFold(finding.Severity, "critical") {
				continue
			}
			rule := ghsaPattern.FindString(finding.URL)
			if rule == "" {
				rule = jsonScalar(finding.ID)
			}
			if rule == "" || resource == "" {
				return false
			}
			found = true
			if !matches(contract, findingIdentity{Scanner: "bun-audit", Rule: rule, Resource: resource, Severity: finding.Severity}) {
				return false
			}
		}
	}
	return found
}

func allNPMFindingsWaived(data []byte, contract exceptionContract) bool {
	var root struct {
		Vulnerabilities map[string]npmVulnerability `json:"vulnerabilities"`
	}
	if json.Unmarshal(data, &root) != nil || root.Vulnerabilities == nil {
		return false
	}
	found := false
	for resource, vulnerability := range root.Vulnerabilities {
		if len(vulnerability.Via) == 0 {
			return false
		}
		for _, via := range vulnerability.Via {
			if via.Object == nil {
				return false
			}
			rule := jsonScalar(via.Source)
			if rule == "" {
				rule = jsonScalar(via.ID)
			}
			if rule == "" {
				return false
			}
			severity := jsonScalar(via.Severity)
			if severity == "" {
				severity = vulnerability.Severity
			}
			found = true
			if !matches(contract, findingIdentity{Scanner: "npm-audit", Rule: rule, Resource: resource, Severity: severity}) {
				return false
			}
		}
	}
	return found
}

type npmVulnerability struct {
	Severity string   `json:"severity"`
	Via      []npmVia `json:"via"`
}

type npmVia struct {
	Object   *struct{}       `json:"-"`
	Source   json.RawMessage `json:"source"`
	ID       json.RawMessage `json:"id"`
	Severity json.RawMessage `json:"severity"`
}

func (v *npmVia) UnmarshalJSON(data []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return errors.New("npm advisory is not an object")
	}
	v.Object = &struct{}{}
	v.Source = object["source"]
	v.ID = object["id"]
	v.Severity = object["severity"]
	return nil
}

func jsonScalar(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	switch value := value.(type) {
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}
