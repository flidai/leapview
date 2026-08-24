package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

type commandRunner struct {
	env []string
}

func (r commandRunner) verifyLive(opts admissionOptions, policy vulnerabilityPolicy, policySHA256 string, contract *securitypolicy.Exceptions, stdout io.Writer) error {
	gh, ok := findExecutable("gh", r.env)
	if !ok {
		return errors.New("live verifier gh is missing")
	}
	docker, dockerOK := findExecutable("docker", r.env)
	if !dockerOK {
		return errors.New("live verifier docker is missing")
	}
	if _, err := r.run(gh, []string{"attestation", "verify", "--help"}, ""); err != nil {
		return errors.New("live verifier gh attestation is missing")
	}
	ghToken, _ := envValue(r.env, "GH_TOKEN")
	if ghToken == "" {
		ghToken, _ = envValue(r.env, "GITHUB_TOKEN")
	}
	ghEnv := setEnv(r.env, "GH_TOKEN", ghToken)
	attestation, err := r.runWithEnv(gh, []string{"attestation", "verify", "oci://" + opts.image, "--repo", repositoryIdentity, "--signer-workflow", opts.expectedWorkflow, "--source-digest", opts.sourceRevision, "--deny-self-hosted-runners", "--format", "json"}, "", ghEnv)
	if err != nil || !verifyAttestation(attestation, opts.expectedWorkflow, opts.sourceRevision) {
		return errors.New("attestation identity or source revision is wrong")
	}
	sbom, err := r.run(docker, []string{"buildx", "imagetools", "inspect", opts.image, "--format", "{{ json .SBOM }}"}, "")
	if err != nil || !hasSPDXDocument(sbom) {
		return errors.New("no SPDX SBOM was discoverable for this digest")
	}

	trivyBin, trivyArgs, err := r.trivyCommand(policy, docker)
	if err != nil {
		return err
	}
	versionArgs := append([]string{trivyBin}, trivyArgs...)
	versionArgs = append(versionArgs, "version", "--format", "json")
	versionJSON, err := r.runCommandParts(versionArgs)
	if err != nil {
		return errors.New("could not determine trivy version")
	}
	actualVersion, err := scannerVersion(versionJSON)
	if err != nil || actualVersion != policy.ScannerVersion {
		if err == nil {
			return errors.New("trivy version does not match pinned version")
		}
		return errors.New("could not determine trivy version")
	}
	args := append([]string{trivyBin}, trivyArgs...)
	args = append(args, "image", "--quiet", "--format", "json", "--exit-code", "0")
	for _, severity := range policy.Severity {
		args = append(args, "--severity", severity)
	}
	if *policy.IgnoreUnfixed {
		args = append(args, "--ignore-unfixed")
	}
	args = append(args, opts.image)
	trivyJSON, err := r.runCommandParts(args)
	if err != nil {
		return errors.New("pinned vulnerability scan could not complete")
	}
	unresolved, err := unresolvedCount(trivyJSON, contract)
	if err != nil {
		return errors.New("vulnerability evidence is not machine-readable")
	}
	max, err := maxUnresolved(policy.MaxUnresolved)
	if err != nil {
		return errors.New("vulnerability policy is not pinned")
	}
	if unresolved > max {
		return errors.New("vulnerability evidence exceeds policy")
	}
	digest := opts.image[strings.LastIndex(opts.image, "@")+1:]
	result := map[string]any{
		"schemaVersion": 1, "image": opts.image, "digest": digest, "registryDigest": digest,
		"attestation":         map[string]any{"verified": true, "repository": repositoryIdentity, "workflow": opts.expectedWorkflow, "sourceRevision": opts.sourceRevision},
		"sbom":                map[string]any{"discoverable": true, "predicateType": "https://spdx.dev/Document/v2.3"},
		"vulnerabilityPolicy": map[string]any{"sha256": policySHA256, "scanner": "trivy", "passed": true},
	}
	return writeResult(opts, r.env, result, stdout)
}

func (r commandRunner) trivyCommand(policy vulnerabilityPolicy, docker string) (string, []string, error) {
	if trivy, ok := findExecutable("trivy", r.env); ok {
		return trivy, nil, nil
	}
	if _, err := r.run(docker, []string{"info"}, ""); err != nil {
		return "", nil, errors.New("pinned trivy verifier cannot access Docker")
	}
	home, ok := envValue(r.env, "HOME")
	if !ok || home == "" {
		home = "/root"
	}
	return docker, []string{"run", "--rm", "--network", "host", "-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", home + "/.docker:/root/.docker:ro", policy.ScannerImage}, nil
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
