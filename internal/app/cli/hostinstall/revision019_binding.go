package hostinstall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

const (
	revision019PredecessorImage = "ghcr.io/flidai/leapview@sha256:4a4455ff0048704acf0df1a9308a39a09b4c786f801fe7f3a383ada089d21368"
	revision019BindingName      = ".host-target-binding.json"
)

type revision019Binding struct {
	SchemaVersion int    `json:"schemaVersion"`
	Image         string `json:"image"`
	TargetID      string `json:"targetId"`
	LegacyConfig  Config `json:"legacyConfig"`
}

// readUpgradeInstallation accepts the provisioner-owned target binding only
// for the one immutable predecessor whose installer predates targetId support.
// Every other installation continues to use its own target-bound marker.
func readUpgradeInstallation(root string) (Config, composectl.InitOptions, error) {
	installed, normalized, err := readAndValidateConfig(filepath.Join(root, installMarkerName))
	if err != nil || installed.TargetID != "" || installed.Image != revision019PredecessorImage {
		return installed, normalized, err
	}
	binding, err := readRevision019Binding(root)
	if err != nil {
		return Config{}, composectl.InitOptions{}, err
	}
	if err := validateRevision019Binding(binding, installed); err != nil {
		return Config{}, composectl.InitOptions{}, err
	}
	installed.TargetID = binding.TargetID
	return installed, normalized, nil
}

func verifyPreparedRevision019Binding(root string, config Config) error {
	binding, err := readRevision019Binding(root)
	if err != nil {
		return err
	}
	return validateRevision019Binding(binding, config)
}

func readRevision019Binding(root string) (revision019Binding, error) {
	contents, err := securefs.ReadPrivateFile(filepath.Join(root, revision019BindingName))
	if err != nil {
		return revision019Binding{}, fmt.Errorf("read revision-019 provisioned target binding: %w", err)
	}
	var binding revision019Binding
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return revision019Binding{}, fmt.Errorf("parse revision-019 target binding: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return revision019Binding{}, fmt.Errorf("revision-019 target binding must contain exactly one object")
	}
	return binding, nil
}

func validateRevision019Binding(binding revision019Binding, installed Config) error {
	if binding.SchemaVersion != 1 || binding.Image != revision019PredecessorImage ||
		binding.TargetID == "" || len(binding.TargetID) > 512 ||
		binding.TargetID != strings.TrimSpace(binding.TargetID) ||
		strings.IndexFunc(binding.TargetID, func(character rune) bool { return character < 32 }) >= 0 ||
		binding.LegacyConfig.TargetID != "" ||
		!configsEqual(binding.LegacyConfig, installed) {
		return fmt.Errorf("revision-019 target binding does not match the installed predecessor")
	}
	return nil
}
