package hostinstall

import (
	"errors"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/platform/buildinfo"
)

// VerifyAccessTransitionFence is used only by the offline candidate command.
// A semantic intent alone is insufficient: the candidate binary and read-only
// parent journal must bind the exact request and recovery point. It does not
// change authorization for any HTTP request or infer replacement permissions.
func VerifyAccessTransitionFence(r NativeRequest, journal, digest, mode string, binary buildinfo.Identity) (admincli.AccessTransitionIntent, error) {
	var empty admincli.AccessTransitionIntent
	id, err := r.Identity()
	if err != nil {
		return empty, err
	}
	if r.AccessTransition == nil {
		return empty, errors.New("no admitted access transition intent")
	}
	if binary.Dirty || binary.Revision != r.CandidateRevision || !digestPattern.MatchString(digest) {
		return empty, errors.New("access transition requires the exact clean candidate and recovery point")
	}
	if mode == "detached" {
		state, err := readDetachedStateFile(journal)
		if err != nil {
			return empty, err
		}
		if state.Identity != id || state.Phase != DetachedRunning || state.RecoveryDigest != digest {
			return empty, errors.New("access transition lacks its running detached fence")
		}
	} else {
		state, err := readJournalFile(journal)
		if err != nil {
			return empty, err
		}
		if state.Identity != id {
			return empty, ErrIdentity
		}
		switch mode {
		case "live":
			if state.Phase != Migrating || !state.RestoreRequired || state.RecoveryDigest != digest {
				return empty, errors.New("access transition lacks its live migration fence")
			}
		case "rehearsal":
			if state.Phase != Capturing || state.RestoreRequired {
				return empty, errors.New("access transition lacks its capture fence")
			}
		default:
			return empty, errors.New("unknown access transition execution mode")
		}
	}
	return *r.AccessTransition, nil
}
