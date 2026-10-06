package localruntime

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/buildinfo"
)

const inspectionProbeTimeout = 5 * time.Second

const inspectionChecksumLimit = 1 << 20

// InspectionOptions selects the existing local runtime state to inspect. It
// intentionally has no session or project-identity authority.
type InspectionOptions struct {
	CheckoutRoot   string
	RuntimePackage string
	StateRoot      string
	DockerBin      string
	Endpoint       Endpoint
	BuildIdentity  buildinfo.Identity
	Runner         Runner
}

// RuntimePackageInfo is the non-secret identity needed for local runtime
// preflight checks.
type RuntimePackageInfo struct {
	Version               string
	Revision              string
	ComposeMinimumVersion string
	Digest                string
}

// InspectRuntimePackage validates the installed runtime files and their
// relationship to the running LeapView binary without changing them.
func InspectRuntimePackage(root string, identity buildinfo.Identity) (RuntimePackageInfo, error) {
	if root == "" {
		var err error
		root, err = defaultRuntimePackageRoot()
		if err != nil {
			return RuntimePackageInfo{}, err
		}
	}
	manifest, digest, err := loadManifest(root, identity)
	if err != nil {
		return RuntimePackageInfo{}, err
	}
	if err := verifyRuntimePackageChecksums(root); err != nil {
		return RuntimePackageInfo{}, err
	}
	return RuntimePackageInfo{
		Version: manifest.LeapView.Version, Revision: manifest.LeapView.Revision,
		ComposeMinimumVersion: manifest.ComposeMinimumVersion, Digest: digest,
	}, nil
}

func verifyRuntimePackageChecksums(root string) error {
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		return err
	}
	checksumRoot := filepath.Dir(canonicalRoot)
	checksumPath := filepath.Join(checksumRoot, "SHA256SUMS")
	info, err := os.Lstat(checksumPath)
	if err != nil {
		return fmt.Errorf("inspect installed runtime checksums: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return errors.New("installed runtime checksum manifest must be a regular file without group/other write access")
	}
	file, err := os.Open(checksumPath)
	if err != nil {
		return fmt.Errorf("read installed runtime checksums: %w", err)
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, inspectionChecksumLimit+1))
	closeErr := file.Close()
	if readErr != nil {
		return fmt.Errorf("read installed runtime checksums: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close installed runtime checksums: %w", closeErr)
	}
	if len(contents) > inspectionChecksumLimit {
		return errors.New("installed runtime checksum manifest is too large")
	}

	relativeRoot, err := filepath.Rel(checksumRoot, canonicalRoot)
	if err != nil || filepath.IsAbs(relativeRoot) || relativeRoot == "." || relativeRoot == ".." || strings.HasPrefix(relativeRoot, ".."+string(filepath.Separator)) {
		return errors.New("installed runtime package is not beneath its checksum manifest")
	}
	expected := make(map[string]string, 4)
	for _, name := range []string{manifestFileName, manifestSchemaName, composeFileName, postgresInitName} {
		expected[filepath.ToSlash(filepath.Join(relativeRoot, name))] = filepath.Join(canonicalRoot, name)
	}
	seen := make(map[string]struct{}, len(expected))
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			return fmt.Errorf("installed runtime checksum line %d is empty", lineNumber)
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 || strings.ToLower(fields[0]) != fields[0] || line != fields[0]+"  "+fields[1] {
			return fmt.Errorf("installed runtime checksum line %d is invalid", lineNumber)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return fmt.Errorf("installed runtime checksum line %d has an invalid digest", lineNumber)
		}
		if !strings.HasPrefix(fields[1], "./") || strings.Contains(fields[1], "\\") {
			return fmt.Errorf("installed runtime checksum line %d has an invalid path", lineNumber)
		}
		relative := strings.TrimPrefix(fields[1], "./")
		cleaned := filepath.Clean(filepath.FromSlash(relative))
		if cleaned == "." || filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || filepath.ToSlash(cleaned) != relative {
			return fmt.Errorf("installed runtime checksum line %d escapes or aliases the package root", lineNumber)
		}
		if _, duplicate := seen[filepath.ToSlash(cleaned)]; duplicate {
			return fmt.Errorf("installed runtime checksum path %q is duplicated", fields[1])
		}
		seen[filepath.ToSlash(cleaned)] = struct{}{}
		target := filepath.Join(checksumRoot, cleaned)
		targetInfo, err := os.Lstat(target)
		if err != nil {
			return fmt.Errorf("inspect installed checksum target %q: %w", fields[1], err)
		}
		if !targetInfo.Mode().IsRegular() {
			return fmt.Errorf("installed checksum target %q is not a regular file", fields[1])
		}
		asset, isRuntimeAsset := expected[filepath.ToSlash(cleaned)]
		if !isRuntimeAsset {
			continue
		}
		actual, err := checksumFile(asset)
		if err != nil {
			return fmt.Errorf("hash installed runtime asset %q: %w", fields[1], err)
		}
		if actual != fields[0] {
			return fmt.Errorf("installed runtime checksum mismatch for %q", fields[1])
		}
		delete(expected, filepath.ToSlash(cleaned))
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read installed runtime checksums: %w", err)
	}
	if len(seen) == 0 {
		return errors.New("installed runtime checksum manifest is empty")
	}
	if len(expected) != 0 {
		return errors.New("installed runtime checksum manifest does not cover every runtime asset")
	}
	return nil
}

func checksumFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Inspect reads local state and runs only Docker inspection commands. It does
// not acquire lifecycle or attachment locks, write state, prune attachments,
// or establish a CLI/browser session.
func Inspect(ctx context.Context, options InspectionOptions) (LifecycleStatus, error) {
	if options.Endpoint == nil {
		return LifecycleStatus{}, errors.New("verified local Docker endpoint is required")
	}
	controller, err := newController(Options{
		CheckoutRoot: options.CheckoutRoot, RuntimePackage: options.RuntimePackage,
		StateRoot: options.StateRoot, DockerBin: options.DockerBin,
		Endpoint:      inspectionEndpoint{Endpoint: options.Endpoint},
		BuildIdentity: options.BuildIdentity, Runner: options.Runner,
	}, false)
	if err != nil {
		return LifecycleStatus{}, err
	}
	controller.runner = inspectionRunner{Runner: controller.runner}
	return controller.inspect(ctx)
}

func (controller *Controller) inspect(ctx context.Context) (LifecycleStatus, error) {
	runtime, err := controller.lifecycleRuntime(ctx)
	if errors.Is(err, ErrRuntimeNotFound) {
		return LifecycleStatus{
			CheckoutRoot: runtime.state.Checkout.CanonicalRoot,
			CheckoutID:   runtime.state.Checkout.ID,
			Attachments:  []AttachmentStatus{},
		}, nil
	}
	if err != nil {
		return LifecycleStatus{}, err
	}
	if runtime.state.Reset != nil {
		return LifecycleStatus{
			Exists: true, RuntimeStatus: runtime.state.Status, Phase: runtime.state.Phase,
			CheckoutRoot: runtime.state.Checkout.CanonicalRoot, CheckoutID: runtime.state.Checkout.ID,
			StateRoot: runtime.root, ComposeProject: runtime.state.Runtime.ComposeProject,
			OwnerID: runtime.state.Runtime.OwnerID, URL: runtime.state.Network.URL,
			TargetName: runtime.state.Session.TargetName, TargetID: runtime.state.Authority.InstanceID,
			ProjectID: runtime.state.Authority.ProjectUID, Attachments: []AttachmentStatus{},
		}, nil
	}
	if err := controller.verifyResourceOwnership(ctx, runtime.state, false); err != nil {
		return LifecycleStatus{}, err
	}
	registry, err := loadAttachmentRegistry(
		filepath.Join(runtime.root, attachmentsFileName),
		attachmentBindingFor(runtime.state), false,
	)
	if err != nil {
		return LifecycleStatus{}, fmt.Errorf("local attachment ownership is uncertain: %w", err)
	}
	live, _, err := classifyAttachments(registry, controller.now(), controller.attachmentStaleAfter)
	if err != nil {
		return LifecycleStatus{}, fmt.Errorf("local attachment ownership is uncertain: %w", err)
	}
	services, err := controller.serviceStatus(ctx, runtime.envPath)
	if err != nil {
		return LifecycleStatus{}, err
	}
	return LifecycleStatus{
		Exists: true, RuntimeStatus: runtime.state.Status, Phase: runtime.state.Phase,
		CheckoutRoot: runtime.state.Checkout.CanonicalRoot, CheckoutID: runtime.state.Checkout.ID,
		StateRoot: runtime.root, ComposeProject: runtime.state.Runtime.ComposeProject,
		OwnerID: runtime.state.Runtime.OwnerID, URL: runtime.state.Network.URL,
		TargetName: runtime.state.Session.TargetName, TargetID: runtime.state.Authority.InstanceID,
		ProjectID: runtime.state.Authority.ProjectUID,
		Services:  services, Attachments: attachmentStatuses(live),
	}, nil
}

type inspectionRunner struct{ Runner }

func (runner inspectionRunner) Run(ctx context.Context, environment []string, arguments ...string) ([]byte, error) {
	if runner.Runner == nil {
		return nil, errors.New("local Docker inspection runner is unavailable")
	}
	probe, cancel := context.WithTimeout(ctx, inspectionProbeTimeout)
	defer cancel()
	return runner.Runner.Run(probe, environment, arguments...)
}

type inspectionEndpoint struct{ Endpoint }

func (endpoint inspectionEndpoint) Verify(ctx context.Context) error {
	if endpoint.Endpoint == nil {
		return errors.New("verified local Docker endpoint is required")
	}
	probe, cancel := context.WithTimeout(ctx, inspectionProbeTimeout)
	defer cancel()
	return endpoint.Endpoint.Verify(probe)
}
