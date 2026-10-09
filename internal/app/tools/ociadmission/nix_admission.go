package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/release/artifactadmission"
)

type nixAdmissionOptions struct {
	kind, directory, sourceRoot, architecture, binaryVerifier, nativeDirectory, output, releaseID string
	evidenceOutput                                                                                string
	runID, attempt                                                                                int
}

type nixVerifiedEvidence struct {
	SchemaVersion                  int               `json:"schemaVersion"`
	Kind                           string            `json:"kind"`
	SourceRevision                 string            `json:"sourceRevision"`
	SignerRevision                 string            `json:"signerRevision"`
	Image                          string            `json:"image"`
	Platform                       string            `json:"platform"`
	Version                        string            `json:"version"`
	ArchiveSHA256                  string            `json:"archiveSHA256"`
	CandidateDigest                string            `json:"candidateDigest"`
	RegistryBindingDigest          string            `json:"registryBindingDigest"`
	SignedEvidenceBindingDigest    string            `json:"signedEvidenceBindingDigest"`
	QualifiedEvidenceBindingDigest string            `json:"qualifiedEvidenceBindingDigest"`
	RuntimeEvidence                json.RawMessage   `json:"runtimeEvidence"`
	GoEvidence                     json.RawMessage   `json:"goEvidence"`
	NativeEvidence                 json.RawMessage   `json:"nativeEvidence"`
	OriginalProducer               json.RawMessage   `json:"originalProducer"`
	FileHashes                     map[string]string `json:"fileHashes"`
	VerificationDigest             string            `json:"verificationDigest"`
}

// runNixAdmission invokes current protected verifiers itself. A caller cannot
// supply a verification JSON or merely assert that candidate gates passed.
// Producer authentication during subsequent transport is independently required.
func runNixAdmission(args, env []string, stdout, stderr io.Writer) error {
	var opts nixAdmissionOptions
	flags := flag.NewFlagSet("ociadmission admit-nix", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.kind, "kind", "", "application-image or site-image")
	flags.StringVar(&opts.directory, "directory", "", "authenticated retained input directory")
	flags.StringVar(&opts.sourceRoot, "source-root", "", "read-only exact original source checkout")
	flags.StringVar(&opts.architecture, "architecture", "", "amd64 or arm64")
	flags.StringVar(&opts.binaryVerifier, "binary-verifier", "", "current protected Go verifier executable")
	flags.StringVar(&opts.nativeDirectory, "native-directory", "", "complete embedded native evidence for application")
	flags.StringVar(&opts.output, "output", "", "fresh canonical admission bundle directory")
	flags.StringVar(&opts.evidenceOutput, "evidence-output", "", "optional fresh directory retaining raw scans even when admission is refused")
	flags.StringVar(&opts.releaseID, "release-id", "", "selected canonical release identity")
	flags.IntVar(&opts.runID, "run-id", 0, "exact original successful producer run")
	flags.IntVar(&opts.attempt, "attempt", 0, "exact original successful producer attempt")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("Nix admission takes only explicit flags")
	}
	verifierRevision, err := validateNixProducer(opts, env)
	if err != nil {
		return err
	}
	runner := commandRunner{env: env}
	python, ok := findExecutable("python3", env)
	if !ok {
		return errors.New("protected Nix verifier Python is unavailable")
	}
	temporary, err := os.MkdirTemp("", "nix-admission-verified-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	fresh := filepath.Join(temporary, "evidence")
	if opts.evidenceOutput != "" {
		fresh = opts.evidenceOutput
	}
	arguments := []string{"scripts/nix_release_verification.py", "--directory", opts.directory, "--source-root", opts.sourceRoot,
		"--kind", opts.kind, "--run-id", strconv.Itoa(opts.runID), "--attempt", strconv.Itoa(opts.attempt),
		"--architecture", opts.architecture, "--binary-verifier", opts.binaryVerifier, "--output", fresh}
	if opts.nativeDirectory != "" {
		arguments = append(arguments, "--native-directory", opts.nativeDirectory)
	}
	if _, err := runner.runWithTimeout(python, arguments, "", env, 45*time.Minute); err != nil {
		return errors.New("protected live Nix evidence verification failed")
	}
	verifiedBytes, err := readBundleFile(fresh, "verification.json", maxVulnerabilityJSONBytes)
	if err != nil {
		return err
	}
	verified, err := parseNixVerification(verifiedBytes, opts)
	if err != nil {
		return err
	}
	policyPath := ".github/security/container-vulnerability-policy.json"
	policy, policyBytes, err := readPolicy(policyPath)
	if err != nil {
		return err
	}
	policySHA := sha256.Sum256(policyBytes)
	scanOptions := admissionOptions{image: verified.Image, OCIRepository: strings.Split(verified.Image, "@")[0], sourceRevision: verified.SourceRevision,
		platform: verified.Platform, policyPath: policyPath, vulnerabilityReportPath: filepath.Join(fresh, "oci-vulnerability.json")}
	contract, err := runner.loadExceptionContract(policyPath)
	if err != nil {
		return err
	}
	docker, ok := findExecutable("docker", env)
	if !ok {
		return errors.New("live Nix OCI scan requires Docker")
	}
	report, rawScan, err := runner.scanLiveImage(scanOptions, policy, hex.EncodeToString(policySHA[:]), contract, docker, nil)
	if err != nil {
		return err
	}
	if err := verifyNixScanIdentity(rawScan, verified); err != nil {
		return err
	}
	return writeNixBundle(opts, env, fresh, verifiedBytes, verified, verifierRevision, policyBytes, rawScan, report, stdout)
}

func validateNixProducer(opts nixAdmissionOptions, env []string) (string, error) {
	value := func(key string) string { v, _ := envValue(env, key); return v }
	if (opts.kind != "application-image" && opts.kind != "site-image") || (opts.architecture != "amd64" && opts.architecture != "arm64") ||
		opts.runID < 1 || opts.attempt < 1 || opts.directory == "" || opts.sourceRoot == "" || opts.output == "" || opts.releaseID == "" ||
		opts.releaseID != strings.TrimSpace(opts.releaseID) || !filepath.IsAbs(opts.binaryVerifier) ||
		(opts.kind == "application-image" && opts.nativeDirectory == "") || (opts.kind == "site-image" && opts.nativeDirectory != "") {
		return "", errors.New("Nix admission requires exact kind/platform/producer/source/output and complete applicable evidence")
	}
	if value("GITHUB_REPOSITORY") != repositoryIdentity || value("GITHUB_EVENT_NAME") != "workflow_dispatch" || value("GITHUB_REF") != "refs/heads/main" ||
		value("GITHUB_WORKFLOW_REF") != artifactadmission.NixAdmissionWorkflow+"@refs/heads/main" || !revisionPattern.MatchString(value("GITHUB_SHA")) ||
		!runNumberPattern.MatchString(value("GITHUB_RUN_ID")) || !runNumberPattern.MatchString(value("GITHUB_RUN_ATTEMPT")) {
		return "", errors.New("Nix admission requires its exact protected main producer")
	}
	if _, err := os.Lstat(opts.output); !os.IsNotExist(err) {
		return "", errors.New("Nix admission output must not exist")
	}
	if opts.evidenceOutput != "" {
		if _, err := os.Lstat(opts.evidenceOutput); !os.IsNotExist(err) {
			return "", errors.New("Nix raw evidence output must not exist")
		}
		bundle, err := filepath.Abs(opts.output)
		if err != nil {
			return "", err
		}
		evidence, err := filepath.Abs(opts.evidenceOutput)
		if err != nil {
			return "", err
		}
		relative, err := filepath.Rel(bundle, evidence)
		if err != nil || relative == "." || (!strings.HasPrefix(relative, "../") && relative != "..") {
			return "", errors.New("Nix raw scans must stay separate from canonical bundle")
		}
	}
	return value("GITHUB_SHA"), nil
}

func parseNixVerification(data []byte, opts nixAdmissionOptions) (nixVerifiedEvidence, error) {
	var result nixVerifiedEvidence
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, errors.New("protected Nix verification document is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return result, errors.New("Nix verification has trailing data")
	}
	if result.SchemaVersion != 1 || result.Kind != opts.kind || result.Platform != "linux/"+opts.architecture ||
		!revisionPattern.MatchString(result.SourceRevision) || !revisionPattern.MatchString(result.SignerRevision) ||
		result.Version == "" || result.Version != strings.TrimSpace(result.Version) || len(result.FileHashes) == 0 || len(result.FileHashes) > 4096 {
		return result, errors.New("Nix verification differs from selected output")
	}
	wantRepository := "ghcr.io/flidai/leapview"
	if opts.kind == "site-image" {
		wantRepository += "-site"
	}
	if !strings.HasPrefix(result.Image, wantRepository+"@") || artifactadmission.ValidateReference(result.Image) != nil {
		return result, errors.New("Nix verification names a foreign immutable image")
	}
	var object map[string]any
	identityDecoder := json.NewDecoder(bytes.NewReader(data))
	identityDecoder.UseNumber()
	if identityDecoder.Decode(&object) != nil {
		return result, errors.New("Nix verification is malformed")
	}
	delete(object, "verificationDigest")
	canonical, err := nixCanonicalJSON(object)
	if err != nil || evidenceDigest(append([]byte("leapview/nix-release-verification/v1\n"), canonical...)) != result.VerificationDigest {
		return result, errors.New("Nix verification digest differs from protected evidence")
	}
	for name, digest := range result.FileHashes {
		if filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != name || strings.Contains(name, "\\") || strings.HasPrefix(name, "../") || !digestPattern.MatchString(digest) {
			return result, errors.New("Nix verification has invalid evidence inventory")
		}
	}
	for _, digest := range []string{result.ArchiveSHA256, result.CandidateDigest, result.RegistryBindingDigest, result.SignedEvidenceBindingDigest, result.QualifiedEvidenceBindingDigest} {
		if !digestPattern.MatchString(digest) {
			return result, errors.New("Nix verification has an invalid original content binding")
		}
	}
	var producer struct {
		SchemaVersion  int    `json:"schemaVersion"`
		Kind           string `json:"kind"`
		RunID          int    `json:"runId"`
		Attempt        int    `json:"attempt"`
		Architecture   string `json:"architecture"`
		SignerRevision string `json:"signerRevision"`
	}
	if json.Unmarshal(result.OriginalProducer, &producer) != nil || producer.SchemaVersion != 1 || producer.Kind != opts.kind || producer.Architecture != opts.architecture || producer.SignerRevision != result.SignerRevision || producer.RunID < 1 || producer.Attempt < 1 ||
		(opts.runID != 0 && producer.RunID != opts.runID) || (opts.attempt != 0 && producer.Attempt != opts.attempt) {
		return result, errors.New("Nix verification original producer differs from selected attempt")
	}
	return result, nil
}

func verifyNixScanIdentity(raw []byte, verified nixVerifiedEvidence) error {
	var scan struct {
		ArtifactName, ArtifactType string
		Metadata                   struct{ ImageConfig bundleImageConfig }
	}
	if json.Unmarshal(raw, &scan) != nil || scan.ArtifactName != verified.Image || scan.ArtifactType != "container_image" {
		return errors.New("Nix OCI scan is not bound to exact registry digest")
	}
	config, labels := scan.Metadata.ImageConfig, scan.Metadata.ImageConfig.Config.Labels
	if config.OS+"/"+config.Architecture != verified.Platform || labels["org.opencontainers.image.revision"] != verified.SourceRevision ||
		labels["org.opencontainers.image.version"] != verified.Version || labels["org.opencontainers.image.source"] != "https://github.com/"+repositoryIdentity ||
		labels["dev.leapview.build.dirty"] != "false" || labels["dev.leapview.build.kind"] != verified.Kind {
		return errors.New("Nix OCI scan configuration differs from exact qualified output")
	}
	return nil
}
