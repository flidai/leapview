package composectl

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

const (
	qualificationNativePhysicalPoolIdentityFile = "pool-identity.json"
	qualificationNativePhysicalPoolEvidenceFile = "shared-pool-evidence.json"
	qualificationNativePhysicalPoolMount        = "/run/leapview/qualification"
)

// qualificationNativePhysicalPoolArtifacts is the host-side, immutable
// description captured by the qualification generator.  Pool and Evidence
// are retained so callers can report the exact owner-validated identities;
// paths point to the canonical files used by the bootstrap command.
type qualificationNativePhysicalPoolArtifacts struct {
	EvidenceDir         string
	PoolPath            string
	EvidencePath        string
	Pool                physicalpool.PoolIdentity
	Evidence            physicalpool.Evidence
	PoolID              string
	CompatibilityDigest string
	EvidenceDigest      string
	ConformanceVersion  string
}

type qualificationNativePhysicalPoolBootstrapResult struct {
	PoolID              string
	CompatibilityDigest string
	EvidenceDigest      string
	ConformanceVersion  string
	Applied             bool
}

// prepareQualificationNativePhysicalPool asks the image to generate the
// target-owned physical-pool contract and evidence, then performs the same
// canonical bootstrap command in dry-run mode.  Only after the dry run has
// returned complete, matching identities are the pool IDs persisted to the
// serving environment.
func (c *Controller) prepareQualificationNativePhysicalPool(
	ctx context.Context,
	evidenceDir string,
) (qualificationNativePhysicalPoolArtifacts, error) {
	if c == nil {
		return qualificationNativePhysicalPoolArtifacts{}, errors.New("controller is required")
	}
	evidenceDir = strings.TrimSpace(evidenceDir)
	if evidenceDir == "" {
		return qualificationNativePhysicalPoolArtifacts{}, errors.New("qualification physical-pool evidence directory is required")
	}
	absEvidenceDir, err := filepath.Abs(evidenceDir)
	if err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, fmt.Errorf("resolve qualification physical-pool evidence directory: %w", err)
	}
	if err := os.MkdirAll(absEvidenceDir, 0o700); err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, fmt.Errorf("create qualification physical-pool evidence directory: %w", err)
	}

	output, err := c.qualificationCompose(
		ctx,
		c.root,
		"run", "--rm", "--no-deps", "leapview",
		"admin", "delivery", "pool", "qualify",
	)
	if err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, fmt.Errorf("generate qualification physical-pool artifacts: %w", err)
	}
	envelope, err := decodeQualificationNativePhysicalPoolEnvelope(output)
	if err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, err
	}
	artifacts, err := qualificationNativePhysicalPoolArtifactsFromEnvelope(absEvidenceDir, envelope)
	if err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, err
	}
	if err := writeQualificationNativePhysicalPoolArtifacts(artifacts); err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, err
	}

	result, err := c.runQualificationNativePhysicalPoolBootstrap(ctx, artifacts, false, nil)
	if err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, fmt.Errorf("dry-run qualification physical-pool bootstrap: %w", err)
	}
	if err := verifyQualificationNativePhysicalPoolBootstrapResult(result, artifacts, false); err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, err
	}
	for _, entry := range []struct {
		key   string
		value string
	}{
		{key: "LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID", value: artifacts.PoolID},
		{key: "LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST", value: artifacts.CompatibilityDigest},
	} {
		if err := appendOrReplaceQualificationEnv(c.path(appEnvName), entry.key, entry.value); err != nil {
			return qualificationNativePhysicalPoolArtifacts{}, fmt.Errorf("persist qualification physical-pool identity: %w", err)
		}
	}
	return artifacts, nil
}

// applyQualificationNativePhysicalPool repeats the canonical bootstrap after
// Initialize has created the control baseline.  The operation-only migrator
// URL is passed through the process environment and named --env forwarding;
// it is never written to leapview.env or any artifact.
func (c *Controller) applyQualificationNativePhysicalPool(
	ctx context.Context,
	topology *qualificationNativePostgresTopology,
	artifacts qualificationNativePhysicalPoolArtifacts,
) error {
	if c == nil {
		return errors.New("controller is required")
	}
	if err := validateQualificationNativePhysicalPoolArtifacts(artifacts); err != nil {
		return fmt.Errorf("qualification physical-pool artifacts changed: %w", err)
	}
	operationEnvironment, err := qualificationNativePostgresOperationEnvironment(topology)
	if err != nil {
		return err
	}
	// Remove any stale operation-only values before and after the command.  A
	// missing environment is tolerated for callers that only exercise the
	// command seam; Initialize normally creates it before this method runs.
	if err := removeQualificationNativeOperationURLsIfPresent(c.path(appEnvName)); err != nil {
		return fmt.Errorf("remove qualification operation URLs from serving environment: %w", err)
	}
	result, err := c.runQualificationNativePhysicalPoolBootstrap(ctx, artifacts, true, operationEnvironment)
	if err != nil {
		return fmt.Errorf("apply qualification physical-pool bootstrap: %w", err)
	}
	if err := verifyQualificationNativePhysicalPoolBootstrapResult(result, artifacts, true); err != nil {
		return err
	}
	if err := removeQualificationNativeOperationURLsIfPresent(c.path(appEnvName)); err != nil {
		return fmt.Errorf("remove qualification operation URLs from serving environment: %w", err)
	}
	return nil
}

func decodeQualificationNativePhysicalPoolEnvelope(output []byte) (adminoffline.QualificationPoolArtifacts, error) {
	artifacts, err := adminoffline.UnmarshalQualificationPoolArtifacts(output)
	if err != nil {
		return adminoffline.QualificationPoolArtifacts{}, fmt.Errorf("decode qualification physical-pool generator output: %w", err)
	}
	return artifacts, nil
}

func qualificationNativePhysicalPoolArtifactsFromEnvelope(
	evidenceDir string,
	envelope adminoffline.QualificationPoolArtifacts,
) (qualificationNativePhysicalPoolArtifacts, error) {
	identity := envelope.Pool
	if err := identity.Validate(); err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, fmt.Errorf("qualification physical-pool identity: %w", err)
	}
	pool, err := physicalpool.NewPhysicalPool(identity)
	if err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, fmt.Errorf("qualification physical-pool identity: %w", err)
	}
	if err := pool.Validate(); err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, fmt.Errorf("qualification physical-pool identity: %w", err)
	}
	encodedEvidence, err := json.Marshal(envelope.Evidence)
	if err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, fmt.Errorf("encode qualification physical-pool evidence artifact: %w", err)
	}
	evidence, err := physicalpool.UnmarshalEvidenceArtifact(encodedEvidence)
	if err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, fmt.Errorf("qualification physical-pool evidence: %w", err)
	}
	if !pool.Identity.Compatibility.StableEqual(evidence.Compatibility) {
		return qualificationNativePhysicalPoolArtifacts{}, errors.New("qualification physical-pool identity and evidence storage contract differ")
	}
	compatibilityDigest, err := evidence.Compatibility.Digest()
	if err != nil {
		return qualificationNativePhysicalPoolArtifacts{}, fmt.Errorf("qualification physical-pool compatibility digest: %w", err)
	}
	return qualificationNativePhysicalPoolArtifacts{
		EvidenceDir:         evidenceDir,
		PoolPath:            filepath.Join(evidenceDir, qualificationNativePhysicalPoolIdentityFile),
		EvidencePath:        filepath.Join(evidenceDir, qualificationNativePhysicalPoolEvidenceFile),
		Pool:                pool.Identity,
		Evidence:            evidence,
		PoolID:              string(pool.ID),
		CompatibilityDigest: compatibilityDigest,
		EvidenceDigest:      evidence.Digest,
		ConformanceVersion:  evidence.ConformanceVersion,
	}, nil
}

func writeQualificationNativePhysicalPoolArtifacts(artifacts qualificationNativePhysicalPoolArtifacts) error {
	poolContents, err := json.MarshalIndent(artifacts.Pool, "", "  ")
	if err != nil {
		return fmt.Errorf("encode qualification physical-pool identity: %w", err)
	}
	poolContents = append(poolContents, '\n')
	evidenceContents, err := physicalpool.MarshalEvidenceArtifact(artifacts.Evidence)
	if err != nil {
		return fmt.Errorf("encode qualification physical-pool evidence: %w", err)
	}
	evidenceContents = append(evidenceContents, '\n')
	if err := writeQualificationNativePhysicalPoolReadableAtomic(artifacts.PoolPath, poolContents); err != nil {
		return fmt.Errorf("write qualification physical-pool identity: %w", err)
	}
	if err := writeQualificationNativePhysicalPoolReadableAtomic(artifacts.EvidencePath, evidenceContents); err != nil {
		return fmt.Errorf("write qualification physical-pool evidence: %w", err)
	}
	return nil
}

// writeQualificationNativePhysicalPoolReadableAtomic is intentionally
// separate from private environment writes: these artifacts contain no
// credentials and are bind-mounted into the non-root LeapView image.
func writeQualificationNativePhysicalPoolReadableAtomic(path string, contents []byte) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("qualification physical-pool artifact path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".qualification-pool-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o644); err != nil {
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	cleanup = false
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (c *Controller) runQualificationNativePhysicalPoolBootstrap(
	ctx context.Context,
	artifacts qualificationNativePhysicalPoolArtifacts,
	apply bool,
	operationEnvironment map[string]string,
) (qualificationNativePhysicalPoolBootstrapResult, error) {
	args := qualificationNativePhysicalPoolBootstrapArguments(artifacts, apply, operationEnvironment)
	var output []byte
	var err error
	if len(operationEnvironment) == 0 {
		output, err = c.qualificationCompose(ctx, c.root, args...)
	} else {
		output, err = c.qualificationComposeEnvironment(ctx, c.root, operationEnvironment, args...)
	}
	if err != nil {
		return qualificationNativePhysicalPoolBootstrapResult{}, err
	}
	return parseQualificationNativePhysicalPoolBootstrapResult(output)
}

func qualificationNativePhysicalPoolBootstrapArguments(
	artifacts qualificationNativePhysicalPoolArtifacts,
	apply bool,
	operationEnvironment map[string]string,
) []string {
	args := []string{"run", "--rm", "--no-deps"}
	// Bind mount each non-secret artifact as read-only.  This keeps the parent
	// evidence directory private even when it contains other qualification
	// reports, while the artifact files themselves remain readable by the image
	// user.
	args = append(args,
		"--volume", artifacts.PoolPath+":"+qualificationNativePhysicalPoolMount+"/"+qualificationNativePhysicalPoolIdentityFile+":ro",
		"--volume", artifacts.EvidencePath+":"+qualificationNativePhysicalPoolMount+"/"+qualificationNativePhysicalPoolEvidenceFile+":ro",
	)
	names := make([]string, 0, len(operationEnvironment))
	for name := range operationEnvironment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		args = append(args, "--env", name)
	}
	args = append(args,
		"leapview", "admin", "delivery", "pool", "bootstrap",
		"--pool", qualificationNativePhysicalPoolMount+"/"+qualificationNativePhysicalPoolIdentityFile,
		"--evidence", qualificationNativePhysicalPoolMount+"/"+qualificationNativePhysicalPoolEvidenceFile,
	)
	if apply {
		args = append(args, "--apply")
	}
	return args
}

func parseQualificationNativePhysicalPoolBootstrapResult(output []byte) (qualificationNativePhysicalPoolBootstrapResult, error) {
	values := make(map[string]string, 5)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return qualificationNativePhysicalPoolBootstrapResult{}, fmt.Errorf("qualification physical-pool bootstrap returned malformed line %q", line)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if _, exists := values[key]; exists {
			return qualificationNativePhysicalPoolBootstrapResult{}, fmt.Errorf("qualification physical-pool bootstrap returned duplicate field %q", key)
		}
		switch key {
		case "pool_id", "compatibility_digest", "evidence_digest", "conformance_version", "applied":
			values[key] = value
		default:
			return qualificationNativePhysicalPoolBootstrapResult{}, fmt.Errorf("qualification physical-pool bootstrap returned unknown field %q", key)
		}
	}
	if err := scanner.Err(); err != nil {
		return qualificationNativePhysicalPoolBootstrapResult{}, fmt.Errorf("read qualification physical-pool bootstrap result: %w", err)
	}
	for _, key := range []string{"pool_id", "compatibility_digest", "evidence_digest", "conformance_version", "applied"} {
		if strings.TrimSpace(values[key]) == "" {
			return qualificationNativePhysicalPoolBootstrapResult{}, fmt.Errorf("qualification physical-pool bootstrap returned incomplete field %q", key)
		}
	}
	for _, key := range []string{"pool_id", "compatibility_digest", "evidence_digest"} {
		if !qualificationNativePhysicalPoolDigest(values[key]) {
			return qualificationNativePhysicalPoolBootstrapResult{}, fmt.Errorf("qualification physical-pool bootstrap returned invalid %s", key)
		}
	}
	if values["applied"] != "true" && values["applied"] != "false" {
		return qualificationNativePhysicalPoolBootstrapResult{}, errors.New("qualification physical-pool bootstrap returned invalid applied flag")
	}
	applied := values["applied"] == "true"
	return qualificationNativePhysicalPoolBootstrapResult{
		PoolID:              values["pool_id"],
		CompatibilityDigest: values["compatibility_digest"],
		EvidenceDigest:      values["evidence_digest"],
		ConformanceVersion:  values["conformance_version"],
		Applied:             applied,
	}, nil
}

func qualificationNativePhysicalPoolDigest(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	raw := strings.TrimPrefix(value, "sha256:")
	if len(raw) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

func verifyQualificationNativePhysicalPoolBootstrapResult(
	result qualificationNativePhysicalPoolBootstrapResult,
	artifacts qualificationNativePhysicalPoolArtifacts,
	expectApplied bool,
) error {
	if result.Applied != expectApplied {
		return fmt.Errorf("qualification physical-pool bootstrap applied=%t, want %t", result.Applied, expectApplied)
	}
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"pool_id", result.PoolID, artifacts.PoolID},
		{"compatibility_digest", result.CompatibilityDigest, artifacts.CompatibilityDigest},
		{"evidence_digest", result.EvidenceDigest, artifacts.EvidenceDigest},
		{"conformance_version", result.ConformanceVersion, artifacts.ConformanceVersion},
	}
	for _, check := range checks {
		if check.got != check.want {
			return fmt.Errorf("qualification physical-pool bootstrap %s changed: got %q, want %q", check.name, check.got, check.want)
		}
	}
	return nil
}

func validateQualificationNativePhysicalPoolArtifacts(artifacts qualificationNativePhysicalPoolArtifacts) error {
	if strings.TrimSpace(artifacts.PoolPath) == "" || strings.TrimSpace(artifacts.EvidencePath) == "" {
		return errors.New("artifact paths are required")
	}
	poolBytes, err := os.ReadFile(artifacts.PoolPath)
	if err != nil {
		return fmt.Errorf("read pool identity: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(poolBytes))
	decoder.DisallowUnknownFields()
	var identity physicalpool.PoolIdentity
	if err := decoder.Decode(&identity); err != nil {
		return fmt.Errorf("decode pool identity: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("decode pool identity: multiple JSON values")
		}
		return fmt.Errorf("decode pool identity: %w", err)
	}
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("pool identity: %w", err)
	}
	pool, err := physicalpool.NewPhysicalPool(identity)
	if err != nil {
		return err
	}
	if string(pool.ID) != artifacts.PoolID || pool.Identity != artifacts.Pool {
		return errors.New("pool identity does not match prepared artifact")
	}
	evidenceBytes, err := os.ReadFile(artifacts.EvidencePath)
	if err != nil {
		return fmt.Errorf("read evidence: %w", err)
	}
	evidence, err := physicalpool.UnmarshalEvidenceArtifact(evidenceBytes)
	if err != nil {
		return fmt.Errorf("decode evidence: %w", err)
	}
	if !reflect.DeepEqual(evidence, artifacts.Evidence) || evidence.Digest != artifacts.EvidenceDigest || evidence.ConformanceVersion != artifacts.ConformanceVersion {
		return errors.New("evidence does not match prepared artifact")
	}
	compatibilityDigest, err := evidence.Compatibility.Digest()
	if err != nil {
		return err
	}
	if compatibilityDigest != artifacts.CompatibilityDigest || !pool.Identity.Compatibility.StableEqual(evidence.Compatibility) {
		return errors.New("pool identity and evidence do not match prepared artifact")
	}
	return nil
}

func removeQualificationNativeOperationURLsIfPresent(path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return removeQualificationNativeOperationURLs(path)
}
