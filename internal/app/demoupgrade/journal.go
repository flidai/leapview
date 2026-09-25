package demoupgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
)

const JournalName = "upgrade-operation.json"
const LockName = "upgrade.lock"

// FileJournal holds its process-shared lock until Close. Root MUST reside outside
// all restored data volumes. Ordinary image rollouts must also take LockName and
// reject nonterminal upgrade state before inspecting or modifying the runtime.
type FileJournal struct {
	path string
	lock *instancelock.Lock
}

func OpenJournal(root string, id Identity) (*FileJournal, error) {
	if err := id.validate(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(root) {
		return nil, errors.New("absolute private journal root required")
	}
	if info, err := os.Lstat(root); err == nil && !info.IsDir() {
		return nil, errors.New("journal root must be a directory, not a symlink")
	}
	lock, err := instancelock.AcquireNamed(root, LockName)
	if err != nil {
		return nil, err
	}
	journal := &FileJournal{path: filepath.Join(root, JournalName), lock: lock}
	state, err := journal.Load(context.Background())
	if errors.Is(err, os.ErrNotExist) {
		err = journal.Save(context.Background(), State{Identity: id, Phase: Prepared})
	} else if err == nil && state.Identity != id {
		if state.Identity.Target != id.Target || (state.Phase != Succeeded && state.Phase != Recovered) {
			err = ErrIdentity
		} else {
			err = journal.next(id)
		}
	}
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	return journal, nil
}
func (j *FileJournal) Close() error {
	if j == nil || j.lock == nil {
		return nil
	}
	err := j.lock.Release()
	j.lock = nil
	return err
}
func (j *FileJournal) Load(ctx context.Context) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if j == nil || j.lock == nil {
		return State{}, errors.New("journal lock not held")
	}
	info, err := os.Lstat(j.path)
	if err != nil {
		return State{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 16384 {
		return State{}, errors.New("invalid private upgrade journal")
	}
	raw, err := securefs.ReadPrivateFile(j.path)
	if err != nil {
		return State{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope struct {
		Version int   `json:"version"`
		State   State `json:"state"`
	}
	if err = decoder.Decode(&envelope); err != nil {
		return State{}, err
	}
	if envelope.Version != 1 {
		return State{}, errors.New("unsupported upgrade journal version")
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return State{}, errors.New("trailing journal data")
	}
	return envelope.State, envelope.State.validate()
}
func (j *FileJournal) Save(ctx context.Context, s State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if j == nil || j.lock == nil {
		return errors.New("journal lock not held")
	}
	if err := s.validate(); err != nil {
		return err
	}
	old, err := j.Load(ctx)
	if errors.Is(err, os.ErrNotExist) {
		if s.Phase != Prepared {
			return errors.New("new operation must be prepared")
		}
	} else if err != nil {
		return err
	} else if old.Identity != s.Identity {
		return ErrIdentity
	} else if !allowedTransition(old, s) {
		return fmt.Errorf("invalid upgrade transition %s -> %s", old.Phase, s.Phase)
	}
	raw, err := json.Marshal(struct {
		Version int   `json:"version"`
		State   State `json:"state"`
	}{1, s})
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(j.path, append(raw, '\n'))
}
func allowedTransition(old, next State) bool {
	if old.RecoveryDigest != "" && next.RecoveryDigest != old.RecoveryDigest {
		return false
	}
	if old.RestoreRequired && !next.RestoreRequired {
		return false
	}
	if old == next {
		return true
	}
	if next.Phase == Restoring {
		switch old.Phase {
		case Quiescing, Capturing, Verified, Migrating, Starting, Validating, Restoring:
			return true
		}
		return false
	}
	if old.Phase == Prepared && next.Phase == Recovered {
		return true
	}
	transitions := map[Phase]Phase{Prepared: Quiescing, Quiescing: Capturing, Capturing: Verified, Verified: Migrating, Migrating: Starting, Starting: Validating, Validating: Committed, Committed: Succeeded, Restoring: Reopening, Reopening: Recovered}
	return transitions[old.Phase] == next.Phase
}

// next preserves terminal evidence before atomically preparing a new operation.
// The concrete admission effect must still compare the requested predecessor to
// the running image. An intervening image-only deployment is permitted.
func (j *FileJournal) next(id Identity) error {
	raw, err := securefs.ReadPrivateFile(j.path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	history := filepath.Join(filepath.Dir(j.path), "upgrade-history", fmt.Sprintf("%x.json", sum))
	if err = securefs.WritePrivateFileAtomic(history, raw); err != nil {
		return err
	}
	state := State{Identity: id, Phase: Prepared}
	encoded, err := json.Marshal(struct {
		Version int   `json:"version"`
		State   State `json:"state"`
	}{1, state})
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(j.path, append(encoded, '\n'))
}
