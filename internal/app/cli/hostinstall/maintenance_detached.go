package hostinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"time"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
)

// CaptureForRehearsal finishes its LIVE transaction before returning. The copy
// is not migration admission: apply must start a new operation and capture its
// own current recovery point. Errors leave the normal recovery journal intact.
func (c *Coordinator) CaptureForRehearsal(ctx context.Context, id Identity, capture func(context.Context, Identity) (string, error)) (string, error) {
	s, err := c.load(ctx, id)
	if err != nil {
		return "", err
	}
	if s.Phase != Prepared || capture == nil {
		return "", ErrRecoveryRequired
	}
	if err = c.Effects.Admit(ctx, id); err != nil {
		return "", err
	}
	if err = c.save(ctx, &s, Quiescing); err != nil {
		return "", err
	}
	if err = c.Effects.Quiesce(ctx); err != nil {
		return "", err
	}
	if err = c.save(ctx, &s, Capturing); err != nil {
		return "", err
	}
	digest, err := capture(ctx, id)
	if err != nil {
		return "", err
	}
	if !digestPattern.MatchString(digest) {
		return "", errors.New("invalid captured snapshot digest")
	}
	// No migration occurred. Recovery only verifies and reopens the unchanged
	// predecessor; it must not restore volumes over writes accepted later.
	if err = c.Recover(ctx, id); err != nil {
		return "", err
	}
	return digest, nil
}

type DetachedPhase string

const (
	DetachedCaptured DetachedPhase = "captured"
	DetachedReady    DetachedPhase = "ready"
	DetachedRunning  DetachedPhase = "running"
	DetachedPassed   DetachedPhase = "passed"
	DetachedFailed   DetachedPhase = "failed"
)

// DetachedRehearsalState is NOT a live maintenance journal. It is kept inside
// the private operation directory and has no authority to restore live volumes.
type DetachedRehearsalState struct {
	Version                    int           `json:"version"`
	Identity                   Identity      `json:"identity"`
	RecoveryDigest             string        `json:"recoveryDigest"`
	CandidateEnvironmentDigest string        `json:"candidateEnvironmentDigest,omitempty"`
	Phase                      DetachedPhase `json:"phase"`
}

const detachedStateName = "detached-rehearsal.json"

func (s DetachedRehearsalState) validate() error {
	if s.Version != 1 || !digestPattern.MatchString(s.RecoveryDigest) || (s.CandidateEnvironmentDigest != "" && !digestPattern.MatchString(s.CandidateEnvironmentDigest)) {
		return errors.New("invalid detached rehearsal receipt")
	}
	if err := s.Identity.validate(); err != nil {
		return err
	}
	switch s.Phase {
	case DetachedCaptured, DetachedReady, DetachedRunning, DetachedPassed, DetachedFailed:
		return nil
	}
	return errors.New("unknown detached rehearsal phase")
}
func readDetachedState(root string) (DetachedRehearsalState, error) {
	return readDetachedStateFile(filepath.Join(root, detachedStateName))
}
func readDetachedStateFile(path string) (DetachedRehearsalState, error) {
	var state DetachedRehearsalState
	raw, err := securefs.ReadPrivateFile(path)
	if err != nil {
		return state, err
	}
	if len(raw) > 16384 {
		return state, errors.New("oversized detached rehearsal receipt")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&state); err != nil {
		return state, err
	}
	if err = d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return state, errors.New("trailing detached receipt data")
	}
	return state, state.validate()
}
func writeDetachedState(root string, state DetachedRehearsalState) error {
	if err := state.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(filepath.Join(root, detachedStateName), raw)
}

// The detached coordinator intentionally cannot call Quiesce, StopCandidate,
// Restore, Expose or mutate installation configuration, even on cancellation.
type detachedRehearsalEffects interface {
	Rehearse(context.Context, string) error
	Cleanup(context.Context) error
}

func runDetachedRehearsal(ctx context.Context, root string, id Identity, e detachedRehearsalEffects) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e == nil {
		return errors.New("clone effects are required")
	}
	lock, err := instancelock.AcquireNamed(root, ".detached-rehearsal.lock")
	if err != nil {
		return err
	}
	defer lock.Release()
	s, err := readDetachedState(root)
	if err != nil {
		return err
	}
	if s.Identity != id {
		return ErrIdentity
	}
	if s.Phase == DetachedCaptured {
		return errors.New("live capture has not been durably released")
	}
	// A prior process may have died with clone containers still present. This
	// cleanup is limited to operation-owned clone names, never live services.
	if err = e.Cleanup(ctx); err != nil {
		return err
	}
	s.Phase = DetachedRunning
	if err = writeDetachedState(root, s); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		result = errors.Join(result, e.Cleanup(cleanupCtx))
		s.Phase = DetachedPassed
		if result != nil {
			s.Phase = DetachedFailed
		}
		result = errors.Join(result, writeDetachedState(root, s))
	}()
	return e.Rehearse(ctx, s.RecoveryDigest)
}
