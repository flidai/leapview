// Package installationstate defines the durable host installation marker.
package installationstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/platform/ociref"
)

const (
	MarkerName    = ".host-install.json"
	SchemaVersion = 1
	PhasePrivate  = "private-bootstrap"
	PhasePublic   = "public"
)

type Config struct {
	SchemaVersion int    `json:"schemaVersion"`
	Domain        string `json:"domain"`
	AdminEmail    string `json:"adminEmail"`
	Environment   string `json:"environment"`
	Image         string `json:"image"`
	TargetID      string `json:"targetId,omitempty"`
	HTTPS         *bool  `json:"https"`
}

// Marker flattens the normalized install Config together with its explicit
// lifecycle phase and the deployment generation to which that phase applies.
type Marker struct {
	Config
	BootstrapPhase string `json:"bootstrapPhase"`
	Generation     string `json:"generation"`
}

func NewMarker(config Config, phase string) (Marker, error) {
	ref, err := ociref.ParseImmutable(strings.TrimSpace(config.Image))
	if err != nil {
		return Marker{}, fmt.Errorf("installation marker requires an immutable image: %w", err)
	}
	marker := Marker{Config: config, BootstrapPhase: phase, Generation: ref.Generation}
	if err := marker.Validate(); err != nil {
		return Marker{}, err
	}
	return marker, nil
}

func (marker Marker) Validate() error {
	if marker.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported installation marker schema version %d", marker.SchemaVersion)
	}
	if marker.BootstrapPhase != PhasePrivate && marker.BootstrapPhase != PhasePublic {
		return errors.New("installation marker requires an explicit bootstrap phase")
	}
	if marker.HTTPS == nil {
		return errors.New("installation marker requires an explicit HTTPS selection")
	}
	ref, err := ociref.ParseImmutable(marker.Image)
	if err != nil || marker.Image != strings.TrimSpace(marker.Image) || marker.Generation != ref.Generation {
		return errors.New("installation marker image and generation do not match")
	}
	return nil
}

func ReadMarker(root string) (Marker, bool, error) {
	contents, err := securefs.ReadPrivateFile(filepath.Join(root, MarkerName))
	if os.IsNotExist(err) {
		return Marker{}, false, nil
	}
	if err != nil {
		return Marker{}, false, err
	}
	var marker Marker
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return Marker{}, false, fmt.Errorf("parse host installation marker: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Marker{}, false, errors.New("host installation marker must contain exactly one JSON object")
	}
	if err := marker.Validate(); err != nil {
		return Marker{}, false, err
	}
	return marker, true, nil
}

func RequireForActiveHost(root, image string) (Marker, error) {
	marker, present, err := ReadMarker(root)
	if err != nil {
		return Marker{}, err
	}
	if !present {
		if _, currentErr := os.Lstat(filepath.Join(root, "current")); currentErr == nil {
			return Marker{}, errors.New("active host installation is missing its explicit phase marker")
		} else if !os.IsNotExist(currentErr) {
			return Marker{}, currentErr
		}
		return Marker{}, nil
	}
	if err := VerifyCurrent(root, marker, image); err != nil {
		return Marker{}, err
	}
	return marker, nil
}

func WriteMarker(root string, marker Marker) error {
	if err := marker.Validate(); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(filepath.Join(root, MarkerName), append(contents, '\n'))
}

func ActiveGeneration(root string) (string, error) {
	target, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil {
		return "", err
	}
	directory, generation := filepath.Split(filepath.Clean(target))
	if filepath.Clean(directory) != "releases" || generation == "" || strings.ContainsAny(generation, `/\\`) {
		return "", fmt.Errorf("active installation generation has unexpected target %q", target)
	}
	return generation, nil
}

func VerifyCurrent(root string, marker Marker, image string) error {
	if err := marker.Validate(); err != nil {
		return err
	}
	if marker.Image != image {
		return errors.New("host installation marker image differs from configured image")
	}
	generation, err := ActiveGeneration(root)
	if err != nil {
		return err
	}
	if marker.Generation != generation {
		return errors.New("host installation marker generation differs from active generation")
	}
	return nil
}
