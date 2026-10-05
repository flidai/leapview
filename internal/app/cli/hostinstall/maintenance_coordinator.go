// This file coordinates operator-authorized single-host maintenance. SQL and
// schema versioning remain owned by Goose; this journal survives paired restore.
package hostinstall

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/flidai/leapview/internal/platform/ociref"
)

type Phase string

const (
	Prepared   Phase = "prepared"
	Quiescing  Phase = "quiescing"
	Capturing  Phase = "capturing"
	Verified   Phase = "verified"
	Migrating  Phase = "migrating"
	Starting   Phase = "starting"
	Validating Phase = "validating"
	Committed  Phase = "committed"
	Succeeded  Phase = "succeeded"
	Restoring  Phase = "restoring"
	Reopening  Phase = "reopening"
	Recovered  Phase = "recovered"
)

var (
	ErrRecoveryRequired = errors.New("unfinished upgrade requires explicit recovery")
	ErrCommitted        = errors.New("committed upgrade cannot restore predecessor state")
	ErrIdentity         = errors.New("upgrade identity mismatch")
	digestPattern       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Identity binds the local maintenance journal to the immutable candidate
// artifact admission and provider request. The migration execution boundary
// revalidates this identity against the newly verified recovery digest.
type Identity struct {
	Target                  string `json:"target"`
	Predecessor             string `json:"predecessor"`
	Candidate               string `json:"candidate"`
	ArtifactAdmissionDigest string `json:"artifactAdmissionDigest"`
}

func (i Identity) validate() error {
	if i.Target == "" || len(i.Target) > 255 || !digestPattern.MatchString(i.ArtifactAdmissionDigest) {
		return ErrIdentity
	}
	if _, err := ociref.ParseImmutable(i.Predecessor); err != nil {
		return ErrIdentity
	}
	if _, err := ociref.ParseImmutable(i.Candidate); err != nil {
		return ErrIdentity
	}
	if i.Candidate == i.Predecessor {
		return ErrIdentity
	}
	return nil
}

// State is saved outside the databases and state volumes being restored.
// RestoreRequired becomes true BEFORE migration can change durable state.
type State struct {
	Identity        Identity `json:"identity"`
	Phase           Phase    `json:"phase"`
	RecoveryDigest  string   `json:"recoveryDigest,omitempty"`
	RestoreRequired bool     `json:"restoreRequired"`
}

func (s State) validate() error {
	if err := s.Identity.validate(); err != nil {
		return err
	}
	switch s.Phase {
	case Prepared, Quiescing, Capturing:
		if s.RestoreRequired {
			return errors.New("premigration state requires restore")
		}
	case Verified:
		if s.RestoreRequired || !digestPattern.MatchString(s.RecoveryDigest) {
			return errors.New("invalid verified recovery state")
		}
	case Migrating, Starting, Validating, Committed, Succeeded:
		if !s.RestoreRequired {
			return errors.New("migration intent must require paired recovery")
		}
		if !digestPattern.MatchString(s.RecoveryDigest) {
			return errors.New("verified recovery digest required")
		}
	case Restoring, Reopening, Recovered:
		if s.RestoreRequired && !digestPattern.MatchString(s.RecoveryDigest) {
			return errors.New("paired recovery digest required")
		}
	default:
		return fmt.Errorf("unknown upgrade phase %q", s.Phase)
	}
	return nil
}

// Journal must fsync its state and directory before Save succeeds. The caller
// must hold the same exclusive host lock across Load, all effects and Save.
type Journal interface {
	Load(context.Context) (State, error)
	Save(context.Context, State) error
}

// Effects is the production composition boundary. All callbacks are idempotent.
// Quiesce must durably fence public traffic, background writers and automatic
// restart across host reboot. Candidate validation takes place behind that gate.
// CaptureAndVerify must physically restore the complete coordinated recovery
// point in isolation and return its verified, target-bound frontier digest.
// Restore restores BOTH databases, managed files and configuration; it must not
// expose either image. VerifyPredecessor checks restored state behind the gate.
// Migrate must revalidate the admitted provider request against this exact
// recovery digest and execute the candidate-owned migrations under its fence.
// Restore verification requires a real isolated restore and runtime validation,
// not a backup table of contents. Artifact evidence comes from the authenticated
// qualification/admission workflow, bound to the exact immutable image.
type Effects interface {
	Admit(context.Context, Identity) error
	Quiesce(context.Context) error
	CaptureAndVerify(context.Context, Identity) (string, error)
	Migrate(context.Context, Identity, string) error
	StartCandidateIsolated(context.Context, Identity) error
	ValidateCandidate(context.Context, Identity) error
	ExposeCandidate(context.Context, Identity) error
	StopCandidate(context.Context) error
	Restore(context.Context, Identity, string) error
	VerifyPredecessor(context.Context, Identity) error
	ExposePredecessor(context.Context, Identity) error
}
type Coordinator struct {
	Journal Journal
	Effects Effects
}

func (c *Coordinator) load(ctx context.Context, id Identity) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if c == nil || c.Journal == nil || c.Effects == nil {
		return State{}, errors.New("upgrade authorities are required")
	}
	if err := id.validate(); err != nil {
		return State{}, err
	}
	s, err := c.Journal.Load(ctx)
	if err != nil {
		return s, err
	}
	if err = s.validate(); err != nil {
		return s, err
	}
	if s.Identity != id {
		return s, ErrIdentity
	}
	return s, nil
}
func (c *Coordinator) save(ctx context.Context, s *State, p Phase) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	next := *s
	next.Phase = p
	if err := next.validate(); err != nil {
		return err
	}
	if err := c.Journal.Save(ctx, next); err != nil {
		return err
	}
	*s = next
	return nil
}

// Run never guesses whether an interrupted migration committed. Any incomplete
// effect requires Recover; it cannot be skipped or blindly replayed. Once
// committed, retry only reopens the candidate, never restores acknowledged data.
func (c *Coordinator) Run(ctx context.Context, id Identity) error {
	s, err := c.load(ctx, id)
	if err != nil {
		return err
	}
	switch s.Phase {
	case Succeeded:
		return nil
	case Committed:
		return c.expose(ctx, &s)
	case Prepared:
	default:
		return ErrRecoveryRequired
	}
	if err = c.Effects.Admit(ctx, id); err != nil {
		return err
	}
	if err = c.save(ctx, &s, Quiescing); err != nil {
		return err
	}
	if err = c.Effects.Quiesce(ctx); err != nil {
		return err
	}
	if err = c.save(ctx, &s, Capturing); err != nil {
		return err
	}
	s.RecoveryDigest, err = c.Effects.CaptureAndVerify(ctx, id)
	if err != nil {
		return err
	}
	if err = c.save(ctx, &s, Verified); err != nil {
		return err
	}
	s.RestoreRequired = true
	if err = c.save(ctx, &s, Migrating); err != nil {
		return err
	}
	if err = c.Effects.Migrate(ctx, id, s.RecoveryDigest); err != nil {
		return err
	}
	if err = c.save(ctx, &s, Starting); err != nil {
		return err
	}
	if err = c.Effects.StartCandidateIsolated(ctx, id); err != nil {
		return err
	}
	if err = c.save(ctx, &s, Validating); err != nil {
		return err
	}
	if err = c.Effects.ValidateCandidate(ctx, id); err != nil {
		return err
	}
	if err = c.save(ctx, &s, Committed); err != nil {
		return err
	}
	return c.expose(ctx, &s)
}
func (c *Coordinator) expose(ctx context.Context, s *State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.Effects.ExposeCandidate(ctx, s.Identity); err != nil {
		return err
	}
	return c.save(ctx, s, Succeeded)
}

// Recover requires a fresh, bounded context after caller cancellation. Failure
// leaves traffic fenced and a retryable durable recovery intent. No down SQL is
// executed. Reopening intent is durable before public traffic can be accepted.
func (c *Coordinator) Recover(ctx context.Context, id Identity) error {
	s, err := c.load(ctx, id)
	if err != nil {
		return err
	}
	switch s.Phase {
	case Committed, Succeeded:
		return ErrCommitted
	case Recovered:
		return nil
	case Reopening:
		if err = c.Effects.ExposePredecessor(ctx, id); err != nil {
			return err
		}
		return c.save(ctx, &s, Recovered)
	case Prepared:
		return c.save(ctx, &s, Recovered)
	case Migrating, Starting, Validating:
		s.RestoreRequired = true
	}
	if err = c.save(ctx, &s, Restoring); err != nil {
		return err
	}
	if err = c.Effects.StopCandidate(ctx); err != nil {
		return err
	}
	if s.RestoreRequired {
		if err = c.Effects.Restore(ctx, id, s.RecoveryDigest); err != nil {
			return err
		}
	}
	if err = c.Effects.VerifyPredecessor(ctx, id); err != nil {
		return err
	}
	if err = c.save(ctx, &s, Reopening); err != nil {
		return err
	}
	if err = c.Effects.ExposePredecessor(ctx, id); err != nil {
		return err
	}
	return c.save(ctx, &s, Recovered)
}
