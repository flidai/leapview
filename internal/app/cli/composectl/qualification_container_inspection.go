package composectl

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	securejoin "github.com/cyphar/filepath-securejoin"
)

// qualificationCopyFromContainer streams the Docker filesystem archive and
// extracts it as the host user. It intentionally preserves file modes and
// hardlinks but not container ownership, so hardened runtime permissions cannot
// prevent host-side qualification inspection. Callers must invoke the returned
// cleanup function after inspecting the copied path.
func (c *Controller) qualificationCopyFromContainer(
	ctx context.Context,
	containerID string,
	containerPath string,
) (string, func(), error) {
	root, err := os.MkdirTemp("", "leapview-container-inspection-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { removeQualificationInspectionRoot(root) }
	archivePath := filepath.Join(root, "payload.tar")
	archiveFile, err := os.OpenFile(
		archivePath,
		os.O_CREATE|os.O_EXCL|os.O_RDWR,
		0o600,
	)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := c.qualificationDockerTo(
		ctx,
		archiveFile,
		"cp",
		containerID+":"+containerPath,
		"-",
	); err != nil {
		_ = archiveFile.Close()
		cleanup()
		return "", func() {}, err
	}
	if _, err := archiveFile.Seek(0, io.SeekStart); err != nil {
		_ = archiveFile.Close()
		cleanup()
		return "", func() {}, err
	}
	payload, extractErr := extractQualificationContainerArchive(
		archiveFile,
		filepath.Join(root, "payload"),
	)
	closeErr := archiveFile.Close()
	if extractErr != nil {
		cleanup()
		return "", func() {}, extractErr
	}
	if closeErr != nil {
		cleanup()
		return "", func() {}, closeErr
	}
	return payload, cleanup, nil
}

func removeQualificationInspectionRoot(root string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(root)
}

type qualificationArchiveHardlink struct {
	target string
	source string
}

type qualificationArchiveDirectory struct {
	path string
	mode os.FileMode
}

func extractQualificationContainerArchive(archive io.Reader, targetRoot string) (string, error) {
	if err := os.Mkdir(targetRoot, 0o700); err != nil {
		return "", err
	}
	reader := tar.NewReader(archive)
	rootName := ""
	hardlinks := make([]qualificationArchiveHardlink, 0)
	directories := make([]qualificationArchiveDirectory, 0)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read container inspection archive: %w", err)
		}
		relative, err := qualificationArchiveRelativePath(header.Name)
		if err != nil {
			return "", err
		}
		entryRoot := qualificationArchiveRoot(relative)
		if rootName == "" {
			rootName = entryRoot
		} else if rootName != "." && entryRoot != rootName {
			return "", fmt.Errorf("container inspection archive contains multiple roots")
		}
		target, err := securejoin.SecureJoin(targetRoot, relative)
		if err != nil {
			return "", fmt.Errorf("resolve container inspection path %q: %w", header.Name, err)
		}
		mode := os.FileMode(header.Mode).Perm()
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return "", err
			}
			directories = append(directories, qualificationArchiveDirectory{
				path: target, mode: mode,
			})
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return "", err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return "", err
			}
			_, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if copyErr != nil {
				return "", copyErr
			}
			if closeErr != nil {
				return "", closeErr
			}
			if err := os.Chmod(target, mode); err != nil {
				return "", err
			}
		case tar.TypeLink:
			linkSource, err := qualificationArchiveRelativePath(header.Linkname)
			if err != nil {
				return "", fmt.Errorf("unsafe container inspection hardlink %q: %w", header.Linkname, err)
			}
			if rootName != "." && qualificationArchiveRoot(linkSource) != rootName {
				return "", fmt.Errorf("container inspection hardlink escapes archive root")
			}
			source, err := securejoin.SecureJoin(targetRoot, linkSource)
			if err != nil {
				return "", fmt.Errorf("resolve container inspection hardlink %q: %w", header.Linkname, err)
			}
			hardlinks = append(hardlinks, qualificationArchiveHardlink{
				target: target, source: source,
			})
		case tar.TypeSymlink:
			linkname, err := qualificationArchiveSymlinkTarget(relative, header.Linkname)
			if err != nil {
				return "", fmt.Errorf(
					"unsafe container inspection symlink %q: %w",
					header.Linkname,
					err,
				)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return "", err
			}
			if err := os.Symlink(linkname, target); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf(
				"container inspection archive contains unsupported entry %q",
				header.Name,
			)
		}
	}
	if rootName == "" {
		return "", fmt.Errorf("container inspection archive is empty")
	}
	if err := createQualificationArchiveHardlinks(hardlinks); err != nil {
		return "", err
	}
	sort.Slice(directories, func(left, right int) bool {
		return len(directories[left].path) > len(directories[right].path)
	})
	for _, directory := range directories {
		if err := os.Chmod(directory.path, directory.mode); err != nil {
			return "", err
		}
	}
	if rootName == "." {
		return targetRoot, nil
	}
	return filepath.Join(targetRoot, filepath.FromSlash(rootName)), nil
}

func qualificationArchiveRelativePath(name string) (string, error) {
	clean := path.Clean(filepath.ToSlash(name))
	if clean == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("container inspection archive contains unsafe path %q", name)
	}
	return filepath.FromSlash(clean), nil
}

func qualificationArchiveRoot(relative string) string {
	clean := filepath.ToSlash(relative)
	if clean == "." {
		return clean
	}
	root, _, _ := strings.Cut(clean, "/")
	return root
}

func qualificationArchiveSymlinkTarget(relative string, linkname string) (string, error) {
	linkname = filepath.ToSlash(linkname)
	if linkname == "" || path.IsAbs(linkname) {
		return "", fmt.Errorf("target is empty or absolute")
	}
	resolved := path.Clean(path.Join(path.Dir(filepath.ToSlash(relative)), linkname))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return "", fmt.Errorf("target escapes archive root")
	}
	return filepath.FromSlash(linkname), nil
}

func createQualificationArchiveHardlinks(links []qualificationArchiveHardlink) error {
	pending := append([]qualificationArchiveHardlink(nil), links...)
	for len(pending) > 0 {
		remaining := pending[:0]
		progress := false
		for _, link := range pending {
			info, err := os.Lstat(link.source)
			if os.IsNotExist(err) {
				remaining = append(remaining, link)
				continue
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("container inspection hardlink source is not a regular file")
			}
			if err := os.MkdirAll(filepath.Dir(link.target), 0o700); err != nil {
				return err
			}
			if err := os.Link(link.source, link.target); err != nil {
				return err
			}
			progress = true
		}
		if !progress {
			return fmt.Errorf("container inspection archive contains unresolved hardlink")
		}
		pending = remaining
	}
	return nil
}

func (c *Controller) qualificationContainerPathExists(
	ctx context.Context,
	containerID string,
	containerPath string,
) (bool, error) {
	_, cleanup, err := c.qualificationCopyFromContainer(ctx, containerID, containerPath)
	if err == nil {
		cleanup()
		return true, nil
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"could not find the file",
		"no such file or directory",
		"not found in container",
	} {
		if strings.Contains(message, fragment) {
			return false, nil
		}
	}
	return false, err
}

func (c *Controller) qualificationContainerTreeBytes(
	ctx context.Context,
	containerID string,
	containerPath string,
	excludedSuffixes ...string,
) (int64, error) {
	root, cleanup, err := c.qualificationCopyFromContainer(ctx, containerID, containerPath)
	if err != nil {
		return 0, err
	}
	defer cleanup()

	var total int64
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("container inspection path contains symlink %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("container inspection path contains non-regular file %s", path)
		}
		for _, suffix := range excludedSuffixes {
			if strings.HasSuffix(entry.Name(), suffix) {
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

func (c *Controller) qualificationContainerLineCount(
	ctx context.Context,
	containerID string,
	containerPath string,
) (int64, error) {
	path, cleanup, err := c.qualificationCopyFromContainer(ctx, containerID, containerPath)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var lines int64
	for scanner.Scan() {
		lines++
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return lines, nil
}

func (c *Controller) countQualificationContainerPaths(
	ctx context.Context,
	containerID string,
	containerRoot string,
	pattern string,
) (int64, error) {
	if _, err := filepath.Match(pattern, "qualification-pattern-check"); err != nil {
		return 0, fmt.Errorf("invalid qualification path pattern %q: %w", pattern, err)
	}
	root, cleanup, err := c.qualificationCopyFromContainer(ctx, containerID, containerRoot)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, entry := range entries {
		matched, err := filepath.Match(pattern, entry.Name())
		if err != nil {
			return 0, err
		}
		if matched {
			count++
		}
	}
	return count, nil
}

func (c *Controller) removeQualificationContainerPathsWithTooling(
	ctx context.Context,
	containerID string,
	paths ...string,
) error {
	stateVolume, err := c.qualificationContainerNamedVolume(
		ctx,
		containerID,
		"/var/lib/leapview",
	)
	if err != nil {
		return err
	}
	arguments := []string{
		"run", "--rm",
		"--user", "0:0",
		"--mount", "type=volume,src=" + stateVolume + ",dst=/var/lib/leapview",
		"--entrypoint", "/bin/rm",
		qualificationBrowserImage,
		"-f",
	}
	arguments = append(arguments, paths...)
	_, err = c.qualificationDocker(ctx, nil, arguments...)
	return err
}

func (c *Controller) qualificationContainerNamedVolume(
	ctx context.Context,
	containerID string,
	destination string,
) (string, error) {
	type containerMount struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Destination string `json:"Destination"`
	}
	output, err := c.qualificationContainers.Existing(containerID).Inspect(
		ctx,
		"{{json .Mounts}}",
	)
	if err != nil {
		return "", fmt.Errorf("inspect qualification container mounts: %w", err)
	}
	var mounts []containerMount
	if err := json.Unmarshal(output, &mounts); err != nil {
		return "", fmt.Errorf("decode qualification container mounts: %w", err)
	}
	for _, mount := range mounts {
		if mount.Destination != destination {
			continue
		}
		if mount.Type != "volume" || strings.TrimSpace(mount.Name) == "" {
			return "", fmt.Errorf(
				"qualification container path %q is not backed by a named volume",
				destination,
			)
		}
		return strings.TrimSpace(mount.Name), nil
	}
	return "", fmt.Errorf(
		"qualification container has no mount at %q",
		destination,
	)
}
