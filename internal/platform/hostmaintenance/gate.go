// Package hostmaintenance defines the exclusion boundary shared by ordinary
// host lifecycle commands and operator maintenance. It contains no executor.
package hostmaintenance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

const LockName = ".leapviewctl.lock"
const JournalName = "upgrade-operation.json"

// Check must run while holding LockName, before any ordinary mutation. A
// durable nonterminal or unreadable journal fails closed, including after reboot.
func Check(root string) error {
	path := filepath.Join(root, JournalName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return errors.New("invalid host maintenance journal")
	}
	raw, err := securefs.ReadPrivateFile(path)
	if err != nil {
		return err
	}
	var v struct {
		Version int `json:"version"`
		State   struct {
			Phase string `json:"phase"`
		} `json:"state"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Version != 1 || (v.State.Phase != "succeeded" && v.State.Phase != "recovered") {
		return errors.New("unfinished host maintenance: use host upgrade recover before starting or deploying")
	}
	return nil
}
