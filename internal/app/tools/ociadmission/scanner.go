package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

// scanLiveImage is shared by conventional and Nix admission. It keeps scanner
// pins, raw findings, policy exceptions and database evidence under one contract.
func (r commandRunner) scanLiveImage(opts admissionOptions, policy vulnerabilityPolicy, policySHA256 string, contract *securitypolicy.Exceptions, docker string, sbom []byte) (vulnerabilityReport, []byte, error) {
	report := newVulnerabilityReport(opts, policySHA256, "")
	cacheDir, err := os.MkdirTemp("", "ociadmission-trivy-cache-")
	if err != nil {
		return report, nil, rejectWithReport(opts, report, outcomeScannerError, "pinned vulnerability scanner cache is unavailable")
	}
	defer os.RemoveAll(cacheDir)
	trivyBin, trivyArgs, err := r.trivyCommand(policy, docker, cacheDir)
	if err != nil {
		return report, nil, rejectWithReport(opts, report, outcomeScannerError, err.Error())
	}
	versionArgs := append([]string{trivyBin}, trivyArgs...)
	versionArgs = append(versionArgs, "version", "--format", "json")
	versionJSON, err := r.runCommandParts(versionArgs)
	if err != nil {
		return report, nil, rejectWithReport(opts, report, outcomeScannerError, "could not determine trivy version")
	}
	actualVersion, err := scannerVersion(versionJSON)
	if err != nil || actualVersion != policy.ScannerVersion {
		if err == nil {
			report.Scanner.Version = sanitizeReportValue(actualVersion, r.env, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._+-", 64)
			return report, nil, rejectWithReport(opts, report, outcomeScannerError, "trivy version does not match pinned version")
		}
		return report, nil, rejectWithReport(opts, report, outcomeScannerError, "could not determine trivy version")
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
		return report, nil, rejectWithReport(opts, report, outcomeScannerError, summary)
	}
	if opts.vulnerabilityReportPath != "" || opts.admissionBundlePath != "" {
		db := report.Database.Vulnerability
		if db.Version == "" || db.UpdatedAt == "" || db.NextUpdate == "" || db.DownloadedAt == "" {
			return report, nil, rejectWithReport(opts, report, outcomeScannerError, "scanner vulnerability database metadata is unavailable")
		}
	}
	parsed, err := parseVulnerabilityReport(trivyJSON, contract, r.env)
	if err != nil {
		return report, nil, rejectWithReport(opts, report, outcomeInvalidReport, "vulnerability evidence is not machine-readable")
	}
	if opts.admissionBundlePath != "" {
		if err := verifyBundleScan(opts, trivyJSON, sbom); err != nil {
			return report, nil, rejectWithReport(opts, report, outcomeInvalidReport, err.Error())
		}
	}
	report.Revision = parsed.ImageRevision
	max, err := maxUnresolved(policy.MaxUnresolved)
	if err != nil {
		return report, nil, rejectWithReport(opts, report, outcomeScannerError, "vulnerability policy is not pinned")
	}
	report.UnresolvedCount = parsed.UnresolvedCount
	report.Findings = parsed.Findings
	report.FindingsTruncated = parsed.FindingsTruncated
	if parsed.UnresolvedCount > max {
		return report, nil, rejectWithReport(opts, report, outcomeRejected, fmt.Sprintf("vulnerability evidence exceeds policy (%d unresolved findings)", parsed.UnresolvedCount))
	}
	report.Outcome = outcomePassed
	if err := writeVulnerabilityReport(opts.vulnerabilityReportPath, report); err != nil {
		return report, nil, errors.New("could not write vulnerability report")
	}
	return report, trivyJSON, nil
}
