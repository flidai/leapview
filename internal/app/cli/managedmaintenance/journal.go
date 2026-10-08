package managedmaintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/platform/hostmaintenance"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
)

const JournalName = hostmaintenance.ManagedJournalName

type FileJournal struct {
	path          string
	lock          *instancelock.Lock
	requestDigest string
	target        string
	budget        time.Duration
}

// InheritedLockFile borrows the controller lock for a child command's ExtraFiles.
// The caller must not close this file or close the journal before the child exits.
func (j *FileJournal) InheritedLockFile() *os.File {
	if j == nil || j.lock == nil {
		return nil
	}
	return j.lock.InheritedFile()
}

// OpenJournal holds the shared host-controller lock until Close. Root must be
// private durable host storage outside application/database state and restores.
func OpenJournal(root string, request Request) (*FileJournal, error) {
	digest, err := request.Digest()
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(root) {
		return nil, errors.New("absolute managed journal root required")
	}
	info, err := os.Lstat(root)
	if err == nil && !info.IsDir() {
		return nil, errors.New("managed journal root is not a directory")
	}
	lock, err := instancelock.AcquireNamed(root, hostmaintenance.LockName)
	if err != nil {
		return nil, err
	}
	if err = hostmaintenance.CheckUpgrade(root); err != nil {
		_ = lock.Release()
		return nil, err
	}
	j := &FileJournal{path: filepath.Join(root, JournalName), lock: lock, requestDigest: digest, target: request.Target, budget: request.Budgets.Total}
	state, err := j.Load(context.Background())
	if err == nil && state.RequestDigest != digest {
		if state.Target != request.Target || (state.Phase != Succeeded && state.Phase != Recovered) {
			err = ErrRecoveryRequired
		} else if request.Operation == "enroll" {
			// Once this host has a managed operation, a new release must use
			// the old/new compatibility contract, even if its process is stopped.
			err = errors.New("managed host is already enrolled; use a compatible handoff")
		} else {
			raw, readErr := securefs.ReadPrivateFile(j.path)
			err = readErr
			if err == nil {
				err = securefs.WritePrivateFileAtomic(filepath.Join(root, "managed-image-history", state.RequestDigest[7:]+".json"), raw)
			}
			if err == nil {
				now := time.Now().UTC()
				err = j.Save(context.Background(), State{Version: 1, RequestDigest: digest, Target: request.Target, Phase: Prepared, StartedAt: now, Deadline: now.Add(request.Budgets.Total)})
			}
		}
	}
	if err != nil && !errors.Is(err, ErrNoOperation) {
		_ = j.Close()
		return nil, err
	}
	return j, nil
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
		return State{}, errors.New("managed journal lock not held")
	}
	info, err := os.Lstat(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, ErrNoOperation
	}
	if err != nil {
		return State{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return State{}, errors.New("invalid private managed journal")
	}
	raw, err := securefs.ReadPrivateFile(j.path)
	if err != nil {
		return State{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var state State
	if err = decoder.Decode(&state); err != nil {
		return State{}, err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return State{}, errors.New("trailing managed journal data")
	}
	return state, state.Validate()
}
func (j *FileJournal) Save(ctx context.Context, state State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if j == nil || j.lock == nil {
		return errors.New("managed journal lock not held")
	}
	if err := state.Validate(); err != nil {
		return err
	}
	if state.RequestDigest != j.requestDigest || state.Target != j.target || state.Deadline.Sub(state.StartedAt) != j.budget {
		return errors.New("managed journal write differs from admitted request")
	}
	old, err := j.Load(ctx)
	if errors.Is(err, ErrNoOperation) {
		if state.Phase != Prepared && state.Phase != ClosingWork {
			return errors.New("new managed operation must begin closed")
		}
	} else if err != nil {
		return err
	} else if !validTransition(old, state) {
		return errors.New("invalid managed maintenance transition")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(j.path, append(raw, '\n'))
}

func validTransition(old, next State) bool {
	if old.Target != next.Target {
		return false
	}
	if old.RequestDigest != next.RequestDigest {
		return (old.Phase == Succeeded || old.Phase == Recovered) && next.Phase == Prepared && !next.CommitEstablished && !next.Recovering
	}
	if old == next {
		return true
	}
	if old.CommitEstablished && !next.CommitEstablished {
		return false
	}
	if !old.CommitEstablished && next.CommitEstablished && next.Phase != Committed {
		return false
	}
	if old.Recovering && !next.Recovering {
		return false
	}
	if next.Phase == ClosingWork {
		if old.Phase == Succeeded || old.Phase == Recovered {
			return false
		}
		if old.CommitEstablished && old.Recovering != next.Recovering {
			return false
		}
		return !next.StartedAt.Before(old.StartedAt)
	}
	if old.StartedAt != next.StartedAt || old.Deadline != next.Deadline || old.Recovering != next.Recovering {
		return false
	}
	transitions := map[Phase]Phase{Prepared: ClosingWork, ClosingWork: ClosingIngress, ClosingIngress: Stopping, Stopping: Starting, Starting: Verifying, Verifying: OpeningWork, OpeningWork: OpeningIngress, OpeningIngress: Committed, Committed: Succeeded}
	if old.Phase == Committed && next.Recovering {
		return next.Phase == Recovered
	}
	return transitions[old.Phase] == next.Phase
}
