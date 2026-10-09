package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/flidai/leapview/internal/release/artifactadmission"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

// runVerifyReceipt checks canonical content after the handoff authenticates its
// producer and ZIP digest through GitHub. It does not establish producer trust
// for arbitrary local JSON and cannot install or publish a receipt itself.
func runVerifyReceipt(args []string, stdout, stderr io.Writer) error {
	var bundle, expectedDigest, producerRevision string
	var opts admissionOptions
	flags := flag.NewFlagSet("ociadmission verify-receipt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&bundle, "bundle", "", "authenticated producer bundle directory")
	flags.StringVar(&expectedDigest, "admission-digest", "", "exact domain-separated admission digest")
	flags.StringVar(&opts.image, "image", "", "exact immutable application image")
	flags.StringVar(&opts.sourceRevision, "source-revision", "", "authenticated source commit SHA")
	flags.StringVar(&opts.platform, "platform", "", "selected image platform")
	flags.StringVar(&opts.expectedWorkflow, "expected-workflow", "", "authenticated producer workflow")
	flags.StringVar(&producerRevision, "producer-revision", "", "exact authenticated Nix admission revision")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || bundle == "" || !digestPattern.MatchString(expectedDigest) ||
		!revisionPattern.MatchString(opts.sourceRevision) || !platformPattern.MatchString(opts.platform) ||
		(opts.expectedWorkflow != artifactsWorkflow && opts.expectedWorkflow != releaseWorkflow && opts.expectedWorkflow != artifactadmission.NixAdmissionWorkflow) {
		return errors.New("verify-receipt requires exact bundle, digest, source, platform and producer")
	}
	if err := artifactadmission.ValidateReference(opts.image); err != nil {
		return err
	}
	document, err := readBundleFile(bundle, "admission.json", artifactadmission.MaxCanonicalBytes)
	if err != nil {
		return err
	}
	admission, err := artifactadmission.ParseCanonical(document)
	if err != nil {
		return err
	}
	digest, err := admission.Digest()
	if err != nil {
		return err
	}
	if admission.NixEvidence != nil {
		if opts.expectedWorkflow != artifactadmission.NixAdmissionWorkflow || !revisionPattern.MatchString(producerRevision) {
			return errors.New("Nix receipt requires its separately authenticated admission producer")
		}
		return verifyNixReceipt(bundle, opts, admission, expectedDigest, producerRevision, stdout)
	}
	if producerRevision != "" {
		return errors.New("legacy receipt cannot borrow Nix producer authority")
	}
	if digest != expectedDigest || admission.Release.Image != opts.image || admission.Release.SourceRevision != opts.sourceRevision ||
		admission.Release.Platform != opts.platform || admission.Provenance.Workflow != opts.expectedWorkflow ||
		admission.Repository != "ghcr.io/flidai/leapview" || admission.Release.Distribution != "distroless" ||
		admission.ArchitectureMarker != transitionpreflight.ArchitecturePostgreSQL {
		return errors.New("canonical receipt differs from selected managed application identity")
	}
	if _, err := admission.ArtifactIdentity(); err != nil {
		return err
	}
	for name, expected := range map[string]string{
		"verified-attestation.json":           admission.Provenance.Reference,
		"sbom.json":                           admission.SBOM.Reference,
		"container-vulnerability-policy.json": admission.SecurityPolicy.Reference,
	} {
		data, err := readBundleFile(bundle, name, maxVulnerabilityJSONBytes)
		if err != nil {
			return err
		}
		if evidenceDigest(data) != expected {
			return fmt.Errorf("canonical receipt %s reference differs from evidence bytes", name)
		}
	}
	config, err := readBundleFile(bundle, "image-config.json", maxVulnerabilityJSONBytes)
	if err != nil {
		return err
	}
	sbom, err := readBundleFile(bundle, "sbom.json", maxVulnerabilityJSONBytes)
	if err != nil {
		return err
	}
	opts.releaseVersion = admission.Release.Version
	if err := verifyBundleImage(opts, config, sbom); err != nil {
		return err
	}
	scan, err := readBundleFile(bundle, "trivy-report.json", maxVulnerabilityJSONBytes)
	if err != nil {
		return err
	}
	if err := verifyBundleScan(opts, scan, sbom); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, digest)
	return err
}

func readBundleFile(bundle, name string, limit int) ([]byte, error) {
	path := filepath.Join(bundle, name)
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > int64(limit) {
		return nil, fmt.Errorf("bundle %s must be a bounded regular file", name)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, fmt.Errorf("bundle %s changed during verification", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(data) != int(before.Size()) {
		return nil, fmt.Errorf("bundle %s changed or exceeds its byte limit", name)
	}
	return data, nil
}
