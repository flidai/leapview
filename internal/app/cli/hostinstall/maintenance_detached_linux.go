//go:build linux

package hostinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/buildinfo"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

func captureNativeRehearsal(ctx context.Context, c *Coordinator, e *NativeEffects, id Identity, out io.Writer) error {
	state, err := c.Journal.Load(ctx)
	if err != nil {
		return err
	}
	var digest string
	var saved DetachedRehearsalState
	if state.Phase == Recovered {
		// Interrupted after reopening but before publishing the ready receipt.
		saved, err = readDetachedState(e.operation)
		if err != nil || saved.Identity != id || (saved.Phase != DetachedCaptured && saved.Phase != DetachedReady) {
			return errors.New("capture already completed or has no matching receipt")
		}
		digest = saved.RecoveryDigest
	} else {
		digest, err = c.CaptureForRehearsal(ctx, id, func(ctx context.Context, id Identity) (string, error) {
			if err := e.stopped(ctx); err != nil {
				return "", err
			}
			credential, err := e.migratorURL(e.original.App)
			if err != nil {
				return "", err
			}
			if err = securefs.WritePrivateFileAtomic(filepath.Join(e.operation, "captured-migrator.url"), []byte(credential)); err != nil {
				return "", err
			}
			environment, err := e.candidateContainerEnvironment()
			if err != nil {
				return "", err
			}
			digest, err := CaptureStoppedDirectories(ctx, e.snapshot(), id.Target, e.original.Volumes)
			if err != nil {
				return "", err
			}
			saved = DetachedRehearsalState{
				Version: 1, Identity: id, RecoveryDigest: digest,
				CandidateEnvironmentDigest: candidateEnvironmentDigest(environment), Phase: DetachedCaptured,
			}
			err = writeDetachedState(e.operation, saved)
			return digest, err
		})
		if err != nil {
			recoveryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			return errors.Join(err, c.Recover(recoveryCtx, id))
		}
	}
	if saved.Identity != id || saved.RecoveryDigest != digest || !digestPattern.MatchString(saved.CandidateEnvironmentDigest) {
		return errors.New("capture receipt is missing its exact candidate environment binding")
	}
	if state.Phase != Recovered {
		// Capture's callback fsynced this receipt before the live predecessor was
		// reopened. Reload it after Recover so ready retains the bound environment.
		saved, err = readDetachedState(e.operation)
		if err != nil || saved.Identity != id || saved.RecoveryDigest != digest || saved.Phase != DetachedCaptured {
			return errors.New("captured recovery receipt changed before live reopen completed")
		}
	}
	// This ready receipt can only be written after durable predecessor recovery.
	// Future clone failures never call back into c or the live installation lock.
	saved.Phase = DetachedReady
	if err = writeDetachedState(e.operation, saved); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(saved)
}

type nativeDetachedEffects struct{ effects *NativeEffects }

func (e nativeDetachedEffects) Rehearse(ctx context.Context, digest string) error {
	_, err := e.effects.rehearseSnapshot(ctx, e.effects.id, digest)
	return err
}
func (e nativeDetachedEffects) Cleanup(ctx context.Context) error { return e.effects.cleanupClone(ctx) }

func runNativeDetached(ctx context.Context, r NativeRequest, stdin io.Reader, stdout io.Writer) error {
	// NewNativeEffects verifies local root/Docker/host/candidate identity. It
	// loads the captured configuration; it does not inspect or change live state.
	e, err := NewNativeEffects(r, stdin, stdout)
	if err != nil {
		return err
	}
	defer e.Close()
	e.detached = true
	if len(e.original.Volumes) == 0 {
		return errors.New("missing captured installation")
	}
	if err = runDetachedRehearsal(ctx, e.operation, e.id, nativeDetachedEffects{e}); err != nil {
		return err
	}
	state, err := readDetachedState(e.operation)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(state)
}

func validateDetachedMigration(r NativeRequest, state DetachedRehearsalState, digest string, binary buildinfo.Identity) error {
	id, err := r.Identity()
	if err != nil {
		return err
	}
	if err = state.validate(); err != nil {
		return err
	}
	if state.Identity != id || state.Phase != DetachedRunning || state.RecoveryDigest != digest || binary.Dirty || binary.Revision != r.CandidateRevision {
		return errors.New("clone migration requires its exact detached running receipt")
	}
	return nil
}

// A detached pass binds the exact source, host layout and transition intent.
// It never authorizes reusing the old recovery snapshot for live migration.
func validatePreparedRehearsal(r NativeRequest) error {
	if !digestPattern.MatchString(r.PreparationDigest) {
		return errors.New("a passed detached rehearsal is required before live upgrade")
	}
	root := filepath.Join(r.Profile.StateRoot, "upgrade-operations", strings.TrimPrefix(r.PreparationDigest, "sha256:"))
	state, err := readDetachedState(root)
	if err != nil {
		return err
	}
	prior, err := ReadNativeRequest(filepath.Join(root, "request.json"))
	if err != nil {
		return err
	}
	id, err := prior.Identity()
	if err != nil {
		return err
	}
	if state.Phase != DetachedPassed || state.Identity != id || id.ArtifactAdmissionDigest != r.PreparationDigest {
		return errors.New("prepared rehearsal is incomplete or belongs to another operation")
	}
	if !digestPattern.MatchString(state.CandidateEnvironmentDigest) {
		return errors.New("prepared rehearsal has no candidate environment binding")
	}
	if prior.CandidateImage != r.CandidateImage || prior.CandidateRevision != r.CandidateRevision || prior.attestationRevision() != r.attestationRevision() || prior.PredecessorImage != r.PredecessorImage || prior.PredecessorRevision != r.PredecessorRevision || !reflect.DeepEqual(prior.Profile, r.Profile) || !reflect.DeepEqual(prior.Plan, r.Plan) || !reflect.DeepEqual(prior.AccessTransition, r.AccessTransition) || !reflect.DeepEqual(prior.AgentCredentialTransition, r.AgentCredentialTransition) {
		return errors.New("prepared rehearsal does not bind this source and installation transition")
	}
	var capturedEnvironment []byte
	for _, name := range []string{"deployment.env", "leapview.env", ".host-install.json"} {
		captured, err := securefs.ReadPrivateFile(filepath.Join(root, "original-config", name))
		if err != nil {
			return err
		}
		actual, err := securefs.ReadPrivateFile(filepath.Join(r.Profile.Root, name))
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, captured) {
			return errors.New("installation configuration changed after preparation; rehearse again")
		}
		if name == "leapview.env" {
			capturedEnvironment = captured
		}
	}
	if _, configured, err := agentCredentialKeyFromEnvironment(capturedEnvironment); err != nil {
		return err
	} else if !configured {
		if _, err = readAgentCredentialKeyFile(filepath.Join(root, "agent-credential-key")); err != nil {
			return fmt.Errorf("prepared generated agent credential key is unavailable: %w", err)
		}
	}
	originalRaw, err := securefs.ReadPrivateFile(filepath.Join(root, "original.json"))
	if err != nil {
		return err
	}
	if len(originalRaw) > 1<<20 {
		return errors.New("prepared runtime inspection exceeds size bound")
	}
	var original nativeOriginal
	if err = json.Unmarshal(originalRaw, &original); err != nil {
		return err
	}
	preparedEffects := &NativeEffects{
		root: filepath.Join(root, "original-config"), provider: r.Profile.StateRoot,
		operation: root, request: prior, id: id, original: original,
	}
	preparedEnvironment, err := preparedEffects.candidateContainerEnvironment()
	if err != nil {
		return err
	}
	if candidateEnvironmentDigest(preparedEnvironment) != state.CandidateEnvironmentDigest {
		return errors.New("prepared candidate environment or agent credential key changed; rehearse again")
	}
	return nil
}
