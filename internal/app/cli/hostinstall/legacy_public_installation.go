package hostinstall

import (
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/flidai/leapview/internal/app/cli/installationstate"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

// This exact qualified public predecessor predates installation phases. Its
// source and payload remain immutable regression boundaries, not a schema-based
// exception for other missing or damaged installation markers.
const legacyPublicImage = "ghcr.io/flidai/leapview@sha256:cae683fbdf86032adf78213b88f83612eb545307791adc1a5e040f9402ba41ff"
const legacyPublicRevision = "77ecf56bec6ee4f9a4018c869c802ee545285583"

func readNativeUpgradeInstallation(root string, request NativeRequest) (validatedNativeInstallation, error) {
	installed, markerErr := readNativeInstallation(root)
	if markerErr == nil || request.PredecessorImage != legacyPublicImage || request.PredecessorRevision != legacyPublicRevision {
		return installed, markerErr
	}
	raw, err := securefs.ReadPrivateFile(filepath.Join(root, installMarkerName))
	if err != nil {
		return validatedNativeInstallation{}, err
	}
	if len(raw) > 1<<20 || rejectDuplicateJSONKeys(raw) != nil {
		return validatedNativeInstallation{}, errors.New("legacy public installation marker is not bounded strict JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return validatedNativeInstallation{}, err
	}
	// A phase field, including null or an invalid value, belongs to the current
	// lifecycle and must never be reinterpreted as a historical public install.
	if _, present := fields["bootstrapPhase"]; present {
		return validatedNativeInstallation{}, markerErr
	}
	if _, present := fields["generation"]; present {
		return validatedNativeInstallation{}, markerErr
	}
	var image string
	if json.Unmarshal(fields["image"], &image) != nil || image != request.PredecessorImage {
		return validatedNativeInstallation{}, errors.New("legacy public installation image differs from the admitted predecessor")
	}
	config, _, err := readAndValidateConfig(filepath.Join(root, installMarkerName))
	if err != nil {
		return validatedNativeInstallation{}, err
	}
	if config.Image != request.PredecessorImage || config.TargetID == "" {
		return validatedNativeInstallation{}, errors.New("legacy public installation requires its exact image and provisioned target")
	}
	// Admission still independently checks the actual container source, active
	// generation and immutable payload. Keep the original marker bytes so capture
	// and recovery retain the predecessor's own installation contract.
	return validatedNativeInstallation{Config: config}, nil
}

func nativeCandidatePublicMarker(root string, request NativeRequest) (installationstate.Marker, error) {
	installed, err := readNativeUpgradeInstallation(root, request)
	if err != nil {
		return installationstate.Marker{}, err
	}
	installed.Config.Image = request.CandidateImage
	return installationstate.NewMarker(installed.Config, installationstate.PhasePublic)
}
