//go:build linux

package demoupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

type snapshotEntry struct {
	Path      string `json:"path"`
	Mode      uint32 `json:"mode"`
	UID       int    `json:"uid"`
	GID       int    `json:"gid"`
	Directory bool   `json:"directory"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256,omitempty"`
}
type snapshotManifest struct {
	Version int                        `json:"version"`
	Target  string                     `json:"target"`
	Volumes map[string][]snapshotEntry `json:"volumes"`
}

// CaptureStoppedDirectories captures a complete provider-native cold recovery
// point. Its caller MUST have stopped PostgreSQL and every application writer,
// disabled restart, and retained the target lock. This primitive does not turn
// a hot directory copy into a PostgreSQL backup, nor prove it can be started.
// The returned digest is only a content identity: an isolated database/runtime
// restore rehearsal is still required before it becomes an admitted frontier.
func CaptureStoppedDirectories(ctx context.Context, root, target string, volumes map[string]string) (string, error) {
	if target == "" || len(volumes) == 0 {
		return "", errors.New("snapshot target and volumes required")
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("snapshot destination must not exist")
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("absolute snapshot root required")
	}
	for name, source := range volumes {
		if !snapshotName(name) || !filepath.IsAbs(source) || source == "/" {
			return "", errors.New("invalid snapshot volume")
		}
		if pathsOverlap(root, source) {
			return "", errors.New("snapshot and live volume overlap")
		}
	}
	parent := filepath.Dir(root)
	if err := securefs.EnsurePrivateDir(parent); err != nil {
		return "", err
	}
	temp, err := os.MkdirTemp(parent, ".capture-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temp)
	manifest := snapshotManifest{Version: 1, Target: target, Volumes: map[string][]snapshotEntry{}}
	for name, source := range volumes {
		entries, err := copySnapshotTree(ctx, source, filepath.Join(temp, name))
		if err != nil {
			return "", err
		}
		manifest.Volumes[name] = entries
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	digest := snapshotDigest(raw)
	if err = securefs.WritePrivateFileAtomic(filepath.Join(temp, "manifest.json"), raw); err != nil {
		return "", err
	}
	if err = os.Rename(temp, root); err != nil {
		return "", err
	}
	return digest, syncDirectory(parent)
}
func snapshotName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && name != "manifest.json" && !strings.ContainsAny(name, "\\\x00")
}
func pathsOverlap(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
func snapshotDigest(raw []byte) string {
	sum := sha256.Sum256(append([]byte("leapview/demo-cold-snapshot/v1\n"), raw...))
	return "sha256:" + hex.EncodeToString(sum[:])
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func snapshotTree(ctx context.Context, root string) ([]snapshotEntry, error) {
	var entries []snapshotEntry
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}

		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("cold snapshot refuses links/special files: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("missing filesystem ownership")
		}
		entry := snapshotEntry{Path: relative, Mode: uint32(info.Mode().Perm() | (info.Mode() & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky))), UID: int(owner.Uid), GID: int(owner.Gid), Directory: info.IsDir()}
		if !info.IsDir() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			hash := sha256.New()
			entry.Size, err = io.Copy(hash, file)
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
			entry.SHA256 = hex.EncodeToString(hash.Sum(nil))
		}
		entries = append(entries, entry)
		return nil
	})
	return entries, err
}
func copySnapshotTree(ctx context.Context, source, destination string) ([]snapshotEntry, error) {
	before, err := snapshotTree(ctx, source)
	if err != nil {
		return nil, err
	}
	if len(before) == 0 || !before[0].Directory {
		return nil, errors.New("snapshot volume must be a directory")
	}
	for _, entry := range before {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		dest := filepath.Join(destination, entry.Path)
		if entry.Directory {
			if err = os.Mkdir(dest, 0700); err != nil {
				return nil, err
			}
		} else {
			in, err := os.Open(filepath.Join(source, entry.Path))
			if err != nil {
				return nil, err
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				in.Close()
				return nil, err
			}
			_, copyErr := io.Copy(out, in)
			syncErr := out.Sync()
			closeErr := errors.Join(in.Close(), out.Close())
			if err = errors.Join(copyErr, syncErr, closeErr); err != nil {
				return nil, err
			}
		}
	}
	// Set directory ownership/modes last so read-only directories remain copyable.
	for index := len(before) - 1; index >= 0; index-- {
		entry := before[index]
		dest := filepath.Join(destination, entry.Path)
		if err = os.Chown(dest, entry.UID, entry.GID); err != nil {
			return nil, err
		}
		if err = os.Chmod(dest, fs.FileMode(entry.Mode)); err != nil {
			return nil, err
		}
		if err = syncDirectory(dest); err != nil {
			return nil, err
		}
	}
	after, err := snapshotTree(ctx, destination)
	if err != nil {
		return nil, err
	}
	if !equalEntries(before, after) {
		return nil, errors.New("copied recovery point differs from stopped source")
	}
	return after, nil
}
func equalEntries(a, b []snapshotEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func readSnapshot(ctx context.Context, root, target, digest string) (snapshotManifest, error) {
	raw, err := securefs.ReadPrivateFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return snapshotManifest{}, err
	}
	if len(raw) > 64<<20 || snapshotDigest(raw) != digest {
		return snapshotManifest{}, errors.New("snapshot manifest digest mismatch")
	}
	var manifest snapshotManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return manifest, err
	}
	if manifest.Version != 1 || manifest.Target != target || len(manifest.Volumes) == 0 {
		return manifest, errors.New("snapshot target mismatch")
	}
	for name, expected := range manifest.Volumes {
		if !snapshotName(name) {
			return manifest, errors.New("invalid snapshot volume name")
		}
		actual, err := snapshotTree(ctx, filepath.Join(root, name))
		if err != nil {
			return manifest, err
		}
		if !equalEntries(expected, actual) {
			return manifest, fmt.Errorf("snapshot volume %s is corrupt", name)
		}
	}
	return manifest, nil
}

// RestoreStoppedDirectories restores the complete pair before any process may
// be restarted. Sources are verified before destination mutation; replaced
// directories are retained for diagnosis. After interruption, rerun against
// the same immutable snapshot with writers still stopped. It is intentionally
// not a standalone CLI and does not reopen public traffic.
func RestoreStoppedDirectories(ctx context.Context, root, target, digest string, volumes map[string]string) error {
	manifest, err := readSnapshot(ctx, root, target, digest)
	if err != nil {
		return err
	}
	if len(volumes) != len(manifest.Volumes) {
		return errors.New("recovery volume set mismatch")
	}
	names := make([]string, 0, len(volumes))
	for name, destination := range volumes {
		if _, ok := manifest.Volumes[name]; !ok || !filepath.IsAbs(destination) || destination == "/" || pathsOverlap(root, destination) {
			return errors.New("invalid recovery destination")
		}
		if info, err := os.Lstat(destination); err == nil && !info.IsDir() {
			return errors.New("recovery destination must not be a symlink or file")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for other, otherPath := range volumes {
			if other != name && pathsOverlap(destination, otherPath) {
				return errors.New("recovery destinations overlap")
			}
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		destination := volumes[name]
		parent := filepath.Dir(destination)
		staging, err := os.MkdirTemp(parent, ".restore-*")
		if err != nil {
			return err
		}
		candidate := filepath.Join(staging, "data")
		copied, copyErr := copySnapshotTree(ctx, filepath.Join(root, name), candidate)
		err = copyErr
		if err == nil && !equalEntries(manifest.Volumes[name], copied) {
			err = errors.New("snapshot changed during restore")
		}
		if err != nil {
			os.RemoveAll(staging)
			return err
		}
		// Keep changed/failed state. Never erase the only remaining old directory
		// before the restored directory has been fully copied and verified.
		if _, err = os.Lstat(destination); err == nil {
			if err = os.Rename(destination, filepath.Join(staging, "replaced")); err != nil {
				return err
			}
			if err = syncDirectory(parent); err != nil {
				return err
			}
			if err = syncDirectory(staging); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = os.Rename(candidate, destination); err != nil {
			return err
		}
		if err = syncDirectory(parent); err != nil {
			return err
		}
		if err = syncDirectory(staging); err != nil {
			return err
		}
	}
	return nil
}
