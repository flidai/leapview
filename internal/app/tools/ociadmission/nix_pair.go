package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/flidai/leapview/internal/release/artifactadmission"
)

// Check both receipts produced by this workflow before its overall run can
// become a successful authenticated handoff producer. This grants no adoption.
func runNixPair(args []string, stdout, stderr io.Writer) error {
	var directory, revision, kind string
	var runID, attempt int
	flags := flag.NewFlagSet("ociadmission verify-nix-pair", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&directory, "directory", "", "exact current run artifact directories")
	flags.StringVar(&revision, "producer-revision", "", "exact current protected revision")
	flags.StringVar(&kind, "kind", "", "exact output kind")
	flags.IntVar(&runID, "run-id", 0, "exact current protected run")
	flags.IntVar(&attempt, "attempt", 0, "exact current protected attempt")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || directory == "" || !revisionPattern.MatchString(revision) || runID < 1 || attempt < 1 || (kind != "site-image" && kind != "application-image") {
		return errors.New("Nix pair requires exact protected producer and output identity")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		return errors.New("Nix pair requires exactly two platform bundles")
	}
	var source, version, releaseID string
	for _, arch := range []string{"amd64", "arm64"} {
		name := "nix-admission-" + kind + "-" + strconv.Itoa(runID) + "-" + strconv.Itoa(attempt) + "-" + arch
		bundle := filepath.Join(directory, name)
		info, err := os.Lstat(bundle)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Nix pair bundle is absent or redirected")
		}
		data, err := readBundleFile(bundle, "admission.json", artifactadmission.MaxCanonicalBytes)
		if err != nil {
			return err
		}
		admission, err := artifactadmission.ParseCanonical(data)
		if err != nil || admission.NixEvidence == nil || admission.NixEvidence.Kind != kind || admission.Release.Platform != "linux/"+arch {
			return errors.New("Nix pair receipt has a foreign output kind or platform")
		}
		if source == "" {
			source, version, releaseID = admission.Release.SourceRevision, admission.Release.Version, admission.Release.ReleaseID
		}
		if admission.Release.SourceRevision != source || admission.Release.Version != version || admission.Release.ReleaseID != releaseID {
			return errors.New("Nix pair combines different source or release identities")
		}
		data, err = readBundleFile(bundle, "binding.json", maxVulnerabilityJSONBytes)
		if err != nil {
			return err
		}
		var binding admissionBinding
		if json.Unmarshal(data, &binding) != nil || binding.RunID != strconv.Itoa(runID) || binding.RunAttempt != strconv.Itoa(attempt) || binding.ProducerRevision != revision || binding.Workflow != artifactadmission.NixAdmissionWorkflow {
			return errors.New("Nix pair has a different admission producer")
		}
		digest, err := admission.Digest()
		if err != nil {
			return err
		}
		opts := admissionOptions{image: admission.Release.Image, sourceRevision: source, platform: admission.Release.Platform, expectedWorkflow: artifactadmission.NixAdmissionWorkflow}
		if err := verifyNixReceipt(bundle, opts, admission, digest, revision, io.Discard); err != nil {
			return err
		}
	}
	_, err = io.WriteString(stdout, "verified exact Nix platform pair\n")
	return err
}
