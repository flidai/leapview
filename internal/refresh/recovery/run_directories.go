package recovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// prepareQualificationRunDirectory creates the fenced staging directory used
// by a scenario adapter. A newer fence supersedes any abandoned lower fence
// for the same occurrence; unrelated entries are never touched.
func prepareQualificationRunDirectory(root string, occurrence Occurrence) (string, error) {
	if occurrence.ID == "" || occurrence.Fence.Generation <= 0 {
		return "", fmt.Errorf("recovery qualification requires an occurrence-owned fenced run directory")
	}
	prefix := occurrence.ID + "-generation-"
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		generation, err := strconv.ParseInt(strings.TrimPrefix(entry.Name(), prefix), 10, 64)
		if err != nil || generation >= occurrence.Fence.Generation {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return "", err
		}
	}
	runDirectory := filepath.Join(root, fmt.Sprintf("%s%d", prefix, occurrence.Fence.Generation))
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		return "", err
	}
	return runDirectory, nil
}

// ReclaimQualificationRunDirectories removes only ledger-owned directories
// whose occurrence has no live execution lease. Unknown entries are retained
// so the sweep cannot delete operator or future-version data.
func ReclaimQualificationRunDirectories(root string, occurrences []Occurrence, now time.Time) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	owned := make(map[string]bool)
	for _, occurrence := range occurrences {
		if occurrence.ID == "" || occurrence.Fence.Generation <= 0 {
			continue
		}
		name := fmt.Sprintf("%s-generation-%d", occurrence.ID, occurrence.Fence.Generation)
		active := (occurrence.Status == StatusClaimed || occurrence.Status == StatusRunning) &&
			occurrence.LeaseExpiresAt.After(now)
		owned[name] = active
		prefix := occurrence.ID + "-generation-"
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			if _, err := strconv.ParseInt(strings.TrimPrefix(entry.Name(), prefix), 10, 64); err == nil {
				if entry.Name() != name {
					owned[entry.Name()] = false
				}
			}
		}
	}
	removed := false
	for _, entry := range entries {
		active, recognized := owned[entry.Name()]
		if !recognized || active || !entry.IsDir() {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
		removed = true
	}
	if !removed {
		return nil
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
