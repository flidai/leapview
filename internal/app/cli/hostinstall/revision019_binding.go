package hostinstall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/flidai/leapview/internal/app/cli/installationstate"
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
	contents, err := securefs.ReadPrivateFile(filepath.Join(root, installMarkerName))
	if err != nil {
		return Config{}, composectl.InitOptions{}, err
	}
	var header struct {
		Image          string          `json:"image"`
		BootstrapPhase json.RawMessage `json:"bootstrapPhase"`
		Generation     json.RawMessage `json:"generation"`
	}
	if err := json.Unmarshal(contents, &header); err != nil {
		return Config{}, composectl.InitOptions{}, fmt.Errorf("parse installed host marker: %w", err)
	}
	var installed Config
	var normalized composectl.InitOptions
	// Retain the existing, explicitly pinned historical migration exception.
	// Its immutable installer predates both target binding and bootstrap phases;
	// the separately provisioned binding below must still match the entire old
	// configuration. No other phase-less marker is an upgrade authority.
	if header.Image == revision019PredecessorImage && len(header.BootstrapPhase) == 0 && len(header.Generation) == 0 {
		installed, normalized, err = readAndValidateConfig(filepath.Join(root, installMarkerName))
		if err != nil {
			return Config{}, composectl.InitOptions{}, err
		}
		if installed.TargetID != "" {
			return Config{}, composectl.InitOptions{}, fmt.Errorf("historical predecessor marker must use its provisioned target binding")
		}
	} else {
		marker, present, markerErr := installationstate.ReadMarker(root)
		if markerErr != nil {
			return Config{}, composectl.InitOptions{}, fmt.Errorf("read installed host marker: %w", markerErr)
		}
		if !present {
			return Config{}, composectl.InitOptions{}, fmt.Errorf("installed host marker is missing")
		}
		installed, normalized, err = normalizeConfig(marker.Config)
		if err != nil {
			return Config{}, composectl.InitOptions{}, err
		}
		if marker.BootstrapPhase != installationstate.PhasePublic {
			return Config{}, composectl.InitOptions{}, fmt.Errorf("host upgrade is unavailable during first-install private bootstrap")
		}
	}
	if installed.TargetID != "" || installed.Image != revision019PredecessorImage {
		return installed, normalized, nil
	}
	contents, targetID, err := readRevision019Binding(root, installed)
	if err != nil {
		return Config{}, composectl.InitOptions{}, err
	}
	installed.TargetID = targetID
	return installed, normalized, nil
}

func readRevision019Binding(root string, installed Config) ([]byte, string, error) {
	contents, err := securefs.ReadPrivateFile(filepath.Join(root, revision019BindingName))
	if err != nil {
		return nil, "", fmt.Errorf("read revision-019 provisioned target binding: %w", err)
	}
	var binding revision019Binding
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return nil, "", fmt.Errorf("parse revision-019 target binding: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, "", fmt.Errorf("revision-019 target binding must contain exactly one object")
	}
	if binding.SchemaVersion != 1 || binding.Image != revision019PredecessorImage ||
		binding.TargetID == "" || len(binding.TargetID) > 512 ||
		binding.TargetID != strings.TrimSpace(binding.TargetID) ||
		strings.IndexFunc(binding.TargetID, func(character rune) bool { return character < 32 }) >= 0 ||
		binding.LegacyConfig.TargetID != "" ||
		!configsEqual(binding.LegacyConfig, installed) {
		return nil, "", fmt.Errorf("revision-019 target binding does not match the installed predecessor")
	}
	return contents, binding.TargetID, nil
}

func isUnphasedRevision019(root string) (bool, error) {
	contents, err := securefs.ReadPrivateFile(filepath.Join(root, installMarkerName))
	if err != nil {
		return false, err
	}
	var header struct {
		Image          string          `json:"image"`
		BootstrapPhase json.RawMessage `json:"bootstrapPhase"`
		Generation     json.RawMessage `json:"generation"`
	}
	if err := json.Unmarshal(contents, &header); err != nil {
		return false, fmt.Errorf("parse installed host marker: %w", err)
	}
	return header.Image == revision019PredecessorImage && len(header.BootstrapPhase) == 0 && len(header.Generation) == 0, nil
}

// readNativeInstallation returns the current validated host configuration and,
// for current markers, the strict lifecycle marker. The sole marker-less result
// is the exact revision-019 predecessor accepted by readUpgradeInstallation
// after its provisioned target binding has been verified.
type validatedNativeInstallation struct {
	Config        Config
	Marker        *installationstate.Marker
	LegacyBinding []byte
}

func readNativeInstallation(root string) (validatedNativeInstallation, error) {
	installed, _, err := readUpgradeInstallation(root)
	if err != nil {
		return validatedNativeInstallation{}, err
	}
	legacy, err := isUnphasedRevision019(root)
	if err != nil {
		return validatedNativeInstallation{}, err
	}
	if legacy {
		if installed.TargetID == "" {
			return validatedNativeInstallation{}, fmt.Errorf("revision-019 predecessor has no validated target binding")
		}
		legacyConfig := installed
		legacyConfig.TargetID = ""
		binding, targetID, err := readRevision019Binding(root, legacyConfig)
		if err != nil {
			return validatedNativeInstallation{}, err
		}
		if targetID != installed.TargetID {
			return validatedNativeInstallation{}, fmt.Errorf("revision-019 target binding changed while reading installation")
		}
		return validatedNativeInstallation{Config: installed, LegacyBinding: binding}, nil
	}
	marker, present, err := installationstate.ReadMarker(root)
	if err != nil {
		return validatedNativeInstallation{}, fmt.Errorf("read installed host marker: %w", err)
	}
	if !present {
		return validatedNativeInstallation{}, fmt.Errorf("installed host marker is missing")
	}
	if !configsEqual(marker.Config, installed) {
		return validatedNativeInstallation{}, fmt.Errorf("installed host marker differs from validated configuration")
	}
	return validatedNativeInstallation{Config: installed, Marker: &marker}, nil
}
