package composectl

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// qualificationCopyFromContainer uses the Docker filesystem API rather than
// requiring inspection utilities in the candidate image. Callers must invoke
// the returned cleanup function after inspecting the copied path.
func (c *Controller) qualificationCopyFromContainer(
	ctx context.Context,
	containerID string,
	containerPath string,
) (string, func(), error) {
	root, err := os.MkdirTemp("", "leapview-container-inspection-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	target := filepath.Join(root, "payload")
	if _, err := c.qualificationDocker(
		ctx,
		nil,
		"cp",
		containerID+":"+containerPath,
		target,
	); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return target, cleanup, nil
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
	arguments := []string{
		"run", "--rm",
		"--user", "0:0",
		"--volumes-from", containerID,
		"--entrypoint", "/bin/rm",
		qualificationBrowserImage,
		"-f",
	}
	arguments = append(arguments, paths...)
	_, err := c.qualificationDocker(ctx, nil, arguments...)
	return err
}
