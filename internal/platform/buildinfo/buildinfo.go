// Package buildinfo exposes the immutable identity of the running LeapView
// binary. Release values are injected by the build pipeline. Uninjected and
// invalid builds are always identified as development builds.
package buildinfo

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	DevelopmentVersion = "development"
	UnknownValue       = "unknown"
)

// These values are intentionally unexported. The release pipeline sets them
// with -ldflags -X; application code consumes only the validated Identity.
var (
	version   string
	revision  string
	buildTime string
	dirty     string
	release   string
)

// Identity is the build metadata contract shared by binaries and APIs.
type Identity struct {
	Version     string `json:"version"`
	Revision    string `json:"revision"`
	BuildTime   string `json:"buildTime"`
	Dirty       bool   `json:"dirty"`
	Development bool   `json:"development"`
}

type injectedMetadata struct {
	version   string
	revision  string
	buildTime string
	dirty     string
	release   string
}

type vcsMetadata struct {
	revision string
	time     string
	modified bool
}

// Current returns the validated identity of this binary.
func Current() Identity {
	return resolve(injectedMetadata{
		version: version, revision: revision, buildTime: buildTime,
		dirty: dirty, release: release,
	}, readVCSMetadata())
}

func resolve(injected injectedMetadata, vcs vcsMetadata) Identity {
	resolvedRevision := firstKnown(injected.revision, vcs.revision)
	resolvedBuildTime := firstKnown(injected.buildTime, vcs.time)
	resolvedDirty := vcs.modified
	dirtyValid := true
	if value := strings.TrimSpace(injected.dirty); value != "" {
		var err error
		resolvedDirty, err = strconv.ParseBool(value)
		dirtyValid = err == nil
		if err != nil {
			resolvedDirty = true
		}
	}

	requestedRelease, releaseErr := strconv.ParseBool(strings.TrimSpace(injected.release))
	qualifiedDevelopment := releaseErr == nil && !requestedRelease && validDevelopmentVersion(injected.version)
	if releaseErr == nil && (requestedRelease || qualifiedDevelopment) &&
		dirtyValid && !resolvedDirty && validVersion(injected.version) &&
		validRevision(resolvedRevision) && validBuildTime(resolvedBuildTime) {
		return Identity{
			Version: strings.TrimSpace(injected.version), Revision: resolvedRevision,
			BuildTime: resolvedBuildTime, Development: !requestedRelease,
		}
	}

	return Identity{
		Version: DevelopmentVersion, Revision: resolvedRevision,
		BuildTime: resolvedBuildTime, Dirty: resolvedDirty, Development: true,
	}
}

func readVCSMetadata() vcsMetadata {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return vcsMetadata{}
	}
	var metadata vcsMetadata
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			metadata.revision = setting.Value
		case "vcs.time":
			metadata.time = setting.Value
		case "vcs.modified":
			metadata.modified, _ = strconv.ParseBool(setting.Value)
		}
	}
	return metadata
}

func validVersion(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.HasPrefix(value, "v") && semver.IsValid("v"+value)
}

func validDevelopmentVersion(value string) bool {
	value = strings.TrimSpace(value)
	return validVersion(value) && semver.Build("v"+value) != ""
}

func validRevision(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validBuildTime(value string) bool {
	_, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	return err == nil
}

func firstKnown(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && value != UnknownValue {
			return value
		}
	}
	return UnknownValue
}

// Write renders an identity for a version command.
func Write(w io.Writer, product string, identity Identity, machineReadable bool) error {
	if machineReadable {
		value := struct {
			Product string `json:"product"`
			Identity
		}{Product: product, Identity: identity}
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	buildState := "release"
	if identity.Development {
		buildState = "development"
	}
	treeState := "clean"
	if identity.Dirty {
		treeState = "dirty"
	}
	_, err := fmt.Fprintf(w, "%s %s\nrevision: %s\nbuild time: %s\nstate: %s, %s\n",
		product, identity.Version, identity.Revision, identity.BuildTime, buildState, treeState)
	return err
}
