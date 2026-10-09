package managedrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/flidai/leapview/internal/platform/objectstore"
	"github.com/flidai/leapview/internal/recoveryset"
)

type ResticConfig struct {
	TargetID         string
	RecoverySetID    string
	Root             recoveryset.ObjectRoot
	StorageRoot      string
	Restic           string
	Repository       string
	PasswordFile     string
	Destination      string
	Manifest         FileManifest
	ManifestDigest   string
	ArtifactMetadata *objectstore.ObjectMetadata
}

// Restic restores one explicit snapshot's local subtree to an absent target.
// Calling composition must hold host exclusion and the original-writer fence.
// The operation does not select latest, delete existing data or reopen traffic.
type Restic struct {
	config  ResticConfig
	source  string
	execute func(context.Context, string, []string, *os.File) error
}

func NewRestic(config ResticConfig) (*Restic, error) {
	source, err := providerrestore.ManagedLocalRootPath(config.Root, config.StorageRoot)
	if err != nil || config.Root.Validate() != nil || config.TargetID == "" || config.RecoverySetID == "" {
		return nil, errors.New("exact managed local recovery root required")
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(config.Root.VersionID) || config.Root.ProviderRecoveryFrontier != "restic:"+config.Root.VersionID {
		return nil, errors.New("full immutable Restic snapshot identity required")
	}
	for _, program := range []string{config.Restic} {
		if !filepath.IsAbs(program) || filepath.Clean(program) != program || !strings.HasPrefix(program, "/nix/store/") {
			return nil, errors.New("pinned Restic executable required")
		}
	}
	if config.Repository == "" || strings.ContainsAny(config.Repository, "\r\n\x00") {
		return nil, errors.New("explicit Restic repository required")
	}
	if !filepath.IsAbs(config.PasswordFile) || !filepath.IsAbs(config.Destination) || filepath.Clean(config.Destination) != config.Destination || config.Destination == "/" {
		return nil, errors.New("absolute private password and canonical restore destination required")
	}
	if value, err := securefs.ReadPrivateFile(config.PasswordFile); err != nil || len(value) == 0 {
		return nil, errors.New("private Restic password file unavailable")
	}
	digest, err := config.Manifest.Digest()
	if err != nil || digest != config.ManifestDigest {
		return nil, errors.New("retained managed content manifest digest mismatch")
	}
	// Roots identify the location separately from the retained content proof.
	if config.Root.Kind == recoveryset.ObjectRootDuckLake && config.Root.Digest != digestBytes([]byte(source)) {
		return nil, errors.New("native local root-path digest mismatch")
	}
	if strings.HasPrefix(config.Root.URI, "serving-artifacts/") {
		if len(config.Manifest.Files) != 1 || config.Manifest.Files[0].Path != filepath.Base(source) || config.ArtifactMetadata == nil || config.ArtifactMetadata.Digest != config.Root.Digest || objectstore.ValidateFilesystemBackupMetadata(*config.ArtifactMetadata) != nil {
			return nil, errors.New("native artifact requires exact envelope content and payload metadata proofs")
		}
		metadata := *config.ArtifactMetadata
		config.ArtifactMetadata = &metadata
	} else if config.ArtifactMetadata != nil {
		return nil, errors.New("non-artifact root rejects envelope metadata")
	}

	config.Manifest.Files = append([]FileContent(nil), config.Manifest.Files...)
	return &Restic{config: config, source: source, execute: runPinnedRestore}, nil
}

func runPinnedRestore(ctx context.Context, program string, args []string, lockFile *os.File) error {
	command := exec.CommandContext(ctx, program, args...)
	command.Env = []string{"PATH=/nonexistent", "LANG=C", "TZ=UTC"}
	if lockFile != nil {
		command.ExtraFiles = []*os.File{lockFile}
	}
	if err := configureRestoreProcess(command); err != nil {
		return err
	}
	// No inherited stdin/terminal, repository override or output carrying secrets.
	if err := command.Run(); err != nil {
		return errors.New("pinned managed backup restore command failed")
	}
	return nil
}

func (restorer *Restic) RestoreObject(ctx context.Context, request providerrestore.ObjectRequest) (providerrestore.ObjectResult, error) {
	if restorer == nil || request.TargetID != restorer.config.TargetID || request.RecoverySetID != restorer.config.RecoverySetID || request.Root != restorer.config.Root || request.IdempotencyKey == "" {
		return providerrestore.ObjectResult{}, errors.New("managed Restic request differs from retained frontier")
	}
	if err := ctx.Err(); err != nil {
		return providerrestore.ObjectResult{}, err
	}
	parent := filepath.Dir(restorer.config.Destination)
	lock, err := instancelock.AcquireNamed(parent, ".managed-restic.lock")
	if err != nil {
		return providerrestore.ObjectResult{}, errors.New("private managed restore parent required")
	}
	defer lock.Release()
	intentPath := filepath.Join(parent, ".managed-restic-"+filepath.Base(restorer.config.Destination)+".json")
	intent, err := json.Marshal(struct {
		Request          providerrestore.ObjectRequest `json:"request"`
		ContentDigest    string                        `json:"contentDigest"`
		Repository       string                        `json:"repository"`
		Destination      string                        `json:"destination"`
		ArtifactMetadata *objectstore.ObjectMetadata   `json:"artifactMetadata,omitempty"`
	}{request, restorer.config.ManifestDigest, restorer.config.Repository, restorer.config.Destination, restorer.config.ArtifactMetadata})
	if err != nil {
		return providerrestore.ObjectResult{}, err
	}
	if existing, err := securefs.ReadPrivateFile(intentPath); err == nil {
		if string(existing) != string(intent) {
			return providerrestore.ObjectResult{}, errors.New("managed restore destination belongs to another immutable operation")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return providerrestore.ObjectResult{}, errors.New("managed restore intent is unavailable")
	} else {
		if _, err := os.Lstat(restorer.config.Destination); !errors.Is(err, os.ErrNotExist) {
			return providerrestore.ObjectResult{}, errors.New("unowned managed restore destination exists or is inaccessible")
		}
		if err := securefs.WritePrivateFileAtomicOnce(intentPath, intent, 0600); err != nil {
			return providerrestore.ObjectResult{}, err
		}
	}
	started := time.Now().UTC()
	result := providerrestore.ObjectResult{Provider: "restic-managed-local", OperationID: request.IdempotencyKey, Kind: request.Root.Kind, URI: request.Root.URI, RequiredVersionID: request.Root.VersionID, ObservedVersionID: request.Root.VersionID, Digest: request.Root.Digest, StartedAt: started}
	if _, err := os.Lstat(restorer.config.Destination); err == nil {
		if err := restorer.verifyDestination(ctx); err != nil {
			return providerrestore.ObjectResult{}, err
		}
		result.CompletedAt = time.Now().UTC()
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return providerrestore.ObjectResult{}, errors.New("managed restore destination inaccessible")
	}
	stage, err := os.MkdirTemp(parent, ".managed-restic-*")
	if err != nil {
		return providerrestore.ObjectResult{}, err
	}
	defer os.RemoveAll(stage)
	source := restorer.source
	artifactFile := strings.HasPrefix(request.Root.URI, "serving-artifacts/")
	if artifactFile {
		source = filepath.Dir(source)
	}
	args := []string{"--no-cache", "--repo", restorer.config.Repository, "--password-file", restorer.config.PasswordFile, "restore", restorer.config.Root.VersionID + ":" + source, "--target", stage, "--verify"}
	if artifactFile {
		args = append(args, "--include", "/"+filepath.Base(restorer.source))
	}
	if err := restorer.execute(ctx, restorer.config.Restic, args, lock.InheritedFile()); err != nil {
		return providerrestore.ObjectResult{}, err
	}
	if err := restorer.config.Manifest.Verify(stage); err != nil {
		return providerrestore.ObjectResult{}, fmt.Errorf("verify exact managed snapshot content: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return providerrestore.ObjectResult{}, err
	}
	// Same-filesystem rename exposes only the completely verified tree.
	expose := stage
	if artifactFile {
		expose = filepath.Join(stage, filepath.Base(restorer.source))
		if _, err := objectstore.VerifyFilesystemBackupFile(ctx, expose, restorer.config.Root.URI, *restorer.config.ArtifactMetadata); err != nil {
			return providerrestore.ObjectResult{}, err
		}
	}
	if err := os.Rename(expose, restorer.config.Destination); err != nil {
		return providerrestore.ObjectResult{}, err
	}
	directory, err := os.Open(parent)
	if err != nil {
		return providerrestore.ObjectResult{}, err
	}
	err = errors.Join(directory.Sync(), directory.Close())
	if err != nil {
		return providerrestore.ObjectResult{}, err
	}
	result.CompletedAt = time.Now().UTC()
	return result, nil
}

func (restorer *Restic) verifyDestination(ctx context.Context) error {
	if !strings.HasPrefix(restorer.config.Root.URI, "serving-artifacts/") {
		return restorer.config.Manifest.Verify(restorer.config.Destination)
	}
	// Artifact manifests bind one immutable file. The containing directory may
	// already retain other independently addressed serving generations.
	file, err := os.OpenFile(restorer.config.Destination, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	pathInfo, pathErr := os.Lstat(restorer.config.Destination)
	if err != nil || pathErr != nil || !pathInfo.Mode().IsRegular() || !info.Mode().IsRegular() || !os.SameFile(info, pathInfo) {
		return errors.New("managed serving artifact must be an immutable regular file")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	proof := restorer.config.Manifest.Files[0]
	if err != nil || size != proof.Size || hex.EncodeToString(hash.Sum(nil)) != proof.SHA256 {
		return errors.New("managed restored artifact differs from retained frontier")
	}
	_, err = objectstore.VerifyFilesystemBackupFile(ctx, restorer.config.Destination, restorer.config.Root.URI, *restorer.config.ArtifactMetadata)
	return err
}
