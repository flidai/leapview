package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

type trivyRunner struct {
	name   string
	prefix []string
}

func runTrivy(parent context.Context, cfg Config, root string, contract *securitypolicy.Exceptions) error {
	runner, err := chooseTrivy(parent, cfg, root)
	if err != nil {
		return err
	}
	args := append([]string(nil), runner.prefix...)
	args = append(args, "fs", "--scanners", "secret,misconfig", "--severity", "HIGH,CRITICAL", "--exit-code", "1", "--ignore-unfixed=false", "--skip-dirs", "node_modules", "--skip-dirs", ".data", "--skip-dirs", ".tmp")
	if contract == nil {
		args = append(args, ".")
		if err := runCommand(parent, cfg, root, cfg.Stdout, cfg.Stderr, runner.name, args...); err != nil {
			return commandFailure("trivy source scan", err)
		}
		return nil
	}

	args = append(args, "--format", "json", ".")
	output, diagnostics, scanErr := runCapture(parent, cfg, root, runner.name, args...)
	if scanErr == nil {
		return nil
	}
	if waived, parseErr := allTrivyFindingsWaived(output, *contract); parseErr == nil && waived {
		fmt.Fprintln(cfg.Stdout, "trivy source scan: all findings match exact, active exceptions")
		return nil
	} else if parseErr != nil {
		writeBytes(cfg.Stderr, diagnostics)
		writeBytes(cfg.Stderr, output)
		return fmt.Errorf("trivy source scan output is malformed: %w", parseErr)
	}
	writeBytes(cfg.Stderr, diagnostics)
	writeBytes(cfg.Stderr, output)
	return commandFailure("trivy source scan", scanErr)
}

func chooseTrivy(parent context.Context, cfg Config, root string) (trivyRunner, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return trivyRunner{}, fmt.Errorf("trivy scanner unavailable: pinned image requires Docker: %w", err)
	}
	if err := runCommand(parent, cfg, root, io.Discard, cfg.Stderr, "docker", "info"); err != nil {
		return trivyRunner{}, fmt.Errorf("trivy scanner unavailable: Docker is not accessible: %w", err)
	}
	return trivyRunner{
		name:   "docker",
		prefix: []string{"run", "--rm", "-v", filepath.Clean(root) + ":/work:ro", "-w", "/work", trivyImage},
	}, nil
}

type trivyReport struct {
	Results []trivyResult `json:"Results"`
}

type trivyResult struct {
	Vulnerabilities   []trivyVulnerability    `json:"Vulnerabilities"`
	Misconfigurations []trivyMisconfiguration `json:"Misconfigurations"`
	Secrets           []trivySecret           `json:"Secrets"`
}

type trivyVulnerability struct {
	ID       string `json:"VulnerabilityID"`
	Package  string `json:"PkgName"`
	Severity string `json:"Severity"`
}

type trivyMisconfiguration struct {
	ID       string `json:"ID"`
	Target   string `json:"Target"`
	Artifact string `json:"ArtifactName"`
	Severity string `json:"Severity"`
}

type trivySecret struct {
	RuleID   string `json:"RuleID"`
	Target   string `json:"Target"`
	Severity string `json:"Severity"`
}

func allTrivyFindingsWaived(data []byte, contract securitypolicy.Exceptions) (bool, error) {
	var raw struct {
		Results json.RawMessage `json:"Results"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&raw); err != nil {
		return false, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return false, errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return false, err
	}
	if len(raw.Results) == 0 || bytes.Equal(bytes.TrimSpace(raw.Results), []byte("null")) {
		return false, errors.New("missing Results")
	}
	var results []trivyResult
	if err := json.Unmarshal(raw.Results, &results); err != nil {
		return false, err
	}
	findings := 0
	for _, result := range results {
		for _, finding := range result.Vulnerabilities {
			findings++
			if !waived(contract, securitypolicy.Finding{Scanner: "trivy", Rule: finding.ID, Resource: finding.Package, Severity: finding.Severity}) {
				return false, nil
			}
		}
		for _, finding := range result.Misconfigurations {
			findings++
			resource := finding.Target
			if resource == "" {
				resource = finding.Artifact
			}
			if !waived(contract, securitypolicy.Finding{Scanner: "trivy", Rule: finding.ID, Resource: resource, Severity: finding.Severity}) {
				return false, nil
			}
		}
		for _, finding := range result.Secrets {
			findings++
			if !waived(contract, securitypolicy.Finding{Scanner: "trivy", Rule: finding.RuleID, Resource: finding.Target, Severity: finding.Severity, Class: "secret"}) {
				return false, nil
			}
		}
	}
	return findings > 0, nil
}

func waived(contract securitypolicy.Exceptions, finding securitypolicy.Finding) bool {
	_, ok := contract.Match(finding)
	return ok && finding.Rule != "" && finding.Resource != ""
}
