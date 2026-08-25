package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/flidai/leapview/internal/platform/compatibility"
)

type deniedStateEvidence struct {
	SchemaVersion int                 `json:"schemaVersion"`
	PolicyVersion string              `json:"policyVersion"`
	ReasonCode    string              `json:"reasonCode"`
	Unchanged     bool                `json:"unchanged"`
	Before        transitionStateHash `json:"before"`
	After         transitionStateHash `json:"after"`
}

type transitionStateHash struct {
	DeploymentEnvironment string `json:"deploymentEnvironmentSha256"`
	HostInstallMarker     string `json:"hostInstallMarkerSha256"`
	Database              string `json:"databaseSha256"`
	ActiveGeneration      string `json:"activeGeneration"`
}

func main() {
	evidenceDir := flag.String("evidence-dir", ".tmp/qualification/ubdr/transition-policy", "bounded evidence output directory")
	flag.Parse()
	if err := run(*evidenceDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(evidenceDir string) error {
	evidenceDir = strings.TrimSpace(evidenceDir)
	if evidenceDir == "" {
		return fmt.Errorf("evidence directory is required")
	}
	if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
		return err
	}
	policy, err := compatibility.EmbeddedPolicy()
	if err != nil {
		return err
	}
	policyDigest := sha256.Sum256(compatibility.EmbeddedPolicyDocument())
	schemaDigest := sha256.Sum256(compatibility.EmbeddedPolicySchema())
	if err := writeJSON(filepath.Join(evidenceDir, "policy-validation.json"), map[string]any{
		"schemaVersion": policy.SchemaVersion,
		"policyVersion": policy.PolicyVersion,
		"valid":         true,
		"policySha256":  hex.EncodeToString(policyDigest[:]),
		"schemaSha256":  hex.EncodeToString(schemaDigest[:]),
	}); err != nil {
		return err
	}

	legacy, ok := policy.ReleaseByID("v0.1.0")
	if !ok {
		return fmt.Errorf("embedded policy omits v0.1.0")
	}
	candidateImage := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	decision := policy.Evaluate(compatibility.Request{
		Operation: compatibility.OperationUpgrade,
		Current:   legacy.IdentityForPlatform("linux/amd64"),
		Next: compatibility.ReleaseIdentity{
			Version: "0.2.0", SourceRevision: strings.Repeat("b", 40),
			Image: candidateImage, Distribution: "public",
			Platform: runtime.GOOS + "/" + runtime.GOARCH,
		},
	})
	if !errors.Is(decision.Err(), compatibility.ErrV010FreshInstallOnly) {
		return fmt.Errorf("v0.1.0 transition did not fail closed: %#v", decision)
	}
	if err := writeJSON(filepath.Join(evidenceDir, "decision.json"), decision); err != nil {
		return err
	}

	root, err := os.MkdirTemp("", "leapview-transition-policy-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	deployment := []byte("LEAPVIEW_IMAGE=" + compatibility.ReleasedV010Image + "\nCOMPOSE_HTTPS=0\n")
	marker := []byte("{\"schemaVersion\":1,\"image\":\"" + compatibility.ReleasedV010Image + "\"}\n")
	database := []byte("released-v0.1.0-state\n")
	for path, contents := range map[string][]byte{
		"deployment.env":                 deployment,
		".host-install.json":             marker,
		compatibility.LegacyV010Database: database,
	} {
		if err := os.WriteFile(filepath.Join(root, path), contents, 0o600); err != nil {
			return err
		}
	}
	if err := os.Symlink(filepath.Join("releases", "sha256-v010"), filepath.Join(root, "current")); err != nil {
		return err
	}
	before, err := stateHash(root)
	if err != nil {
		return err
	}
	controller, err := composectl.New(composectl.Options{Root: root, DockerBin: "/bin/false"})
	if err != nil {
		return err
	}
	operationErr := controller.Upgrade(context.Background(), candidateImage)
	if !errors.Is(operationErr, compatibility.ErrV010FreshInstallOnly) {
		return fmt.Errorf("controller denial = %v", operationErr)
	}
	after, err := stateHash(root)
	if err != nil {
		return err
	}
	unchanged := before == after
	if !unchanged {
		return fmt.Errorf("denied transition changed persistent state")
	}
	return writeJSON(filepath.Join(evidenceDir, "denied-transition-state.json"), deniedStateEvidence{
		SchemaVersion: 1, PolicyVersion: policy.PolicyVersion,
		ReasonCode: decision.ReasonCode, Unchanged: true, Before: before, After: after,
	})
}

func stateHash(root string) (transitionStateHash, error) {
	deployment, err := os.ReadFile(filepath.Join(root, "deployment.env"))
	if err != nil {
		return transitionStateHash{}, err
	}
	marker, err := os.ReadFile(filepath.Join(root, ".host-install.json"))
	if err != nil {
		return transitionStateHash{}, err
	}
	database, err := os.ReadFile(filepath.Join(root, compatibility.LegacyV010Database))
	if err != nil {
		return transitionStateHash{}, err
	}
	active, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil {
		return transitionStateHash{}, err
	}
	return transitionStateHash{
		DeploymentEnvironment: digest(deployment),
		HostInstallMarker:     digest(marker),
		Database:              digest(database),
		ActiveGeneration:      active,
	}, nil
}

func digest(contents []byte) string {
	value := sha256.Sum256(contents)
	return hex.EncodeToString(value[:])
}

func writeJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(bytes.TrimSpace(encoded), '\n')
	return os.WriteFile(path, encoded, 0o600)
}
