package managedmaintenance

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Effects implements a request-bound adapter. Every mutating method must be
// idempotent and reject unrelated containers/configuration. CloseAdmission must
// durably close startup/work admission before acknowledging its RPC. DrainAndStop
// proves graceful completion, process exit, and home-lock release. StartPrepared
// starts with work and product HTTP closed. VerifyPrepared checks read-only
// runtime, worker dependency/role/configuration health and credential key coverage.
// OpenWork is the explicit work authorization boundary: it starts workers and
// keeps product HTTP closed on a partial startup failure. OpenIngress follows
// successful running-worker health. Neither forward nor recovery restores data.
type Effects interface {
	Preflight(context.Context) error
	CloseAdmission(context.Context) error
	CloseIngress(context.Context) error
	DrainAndStop(context.Context) error
	StartPrepared(context.Context, Release) error
	VerifyPrepared(context.Context, Release) error
	OpenWork(context.Context, Release) error
	OpenIngress(context.Context, Release) error
	FinalizeWork(context.Context, Release) error
}

type Journal interface {
	Load(context.Context) (State, error)
	Save(context.Context, State) error
}
type Coordinator struct {
	Request Request
	Journal Journal
	Effects Effects
}

type Phase string

const (
	Prepared       Phase = "prepared"
	ClosingWork    Phase = "closing-work"
	ClosingIngress Phase = "closing-ingress"
	Stopping       Phase = "stopping"
	Starting       Phase = "starting"
	Verifying      Phase = "verifying"
	OpeningWork    Phase = "opening-work"
	OpeningIngress Phase = "opening-ingress"
	Committed      Phase = "committed"
	Succeeded      Phase = "succeeded"
	Recovered      Phase = "recovered"
)

var ErrRecoveryRequired = errors.New("unfinished managed maintenance requires explicit recovery")
var ErrNoOperation = errors.New("no managed maintenance operation")

type State struct {
	Version           int       `json:"version"`
	RequestDigest     string    `json:"requestDigest"`
	Target            string    `json:"target"`
	Phase             Phase     `json:"phase"`
	Recovering        bool      `json:"recovering"`
	CommitEstablished bool      `json:"commitEstablished"`
	StartedAt         time.Time `json:"startedAt"`
	Deadline          time.Time `json:"deadline"`
}

func (s State) Validate() error {
	if s.Version != 1 || !digestPattern.MatchString(s.RequestDigest) || !targetPattern.MatchString(s.Target) || s.StartedAt.IsZero() || !s.Deadline.After(s.StartedAt) {
		return errors.New("invalid managed maintenance journal identity/budget")
	}
	switch s.Phase {
	case Committed, Succeeded, Recovered:
		if !s.CommitEstablished || (s.Phase == Succeeded && s.Recovering) || (s.Phase == Recovered && !s.Recovering) {
			return errors.New("invalid managed commit state")
		}
		return nil
	case Prepared, ClosingWork, ClosingIngress, Stopping, Starting, Verifying, OpeningWork, OpeningIngress:
		return nil
	}
	return errors.New("unknown managed maintenance phase")
}
func (c *Coordinator) load(ctx context.Context) (State, error) {
	if c == nil || c.Journal == nil || c.Effects == nil {
		return State{}, errors.New("managed maintenance owners are required")
	}
	digest, err := c.Request.Digest()
	if err != nil {
		return State{}, err
	}
	s, err := c.Journal.Load(ctx)
	if errors.Is(err, ErrNoOperation) {
		now := time.Now().UTC()
		return State{Version: 1, RequestDigest: digest, Target: c.Request.Target, Phase: Prepared, StartedAt: now, Deadline: now.Add(c.Request.Budgets.Total)}, nil
	}
	if err != nil {
		return s, err
	}
	if err = s.Validate(); err != nil {
		return s, err
	}
	if s.RequestDigest != digest || s.Target != c.Request.Target || s.Deadline.Sub(s.StartedAt) != c.Request.Budgets.Total {
		return s, errors.New("managed maintenance request differs from durable operation")
	}
	return s, nil
}
func (c *Coordinator) Run(ctx context.Context) error {
	s, err := c.load(ctx)
	if err != nil {
		return err
	}
	if s.Phase == Succeeded || s.Phase == Recovered {
		return nil
	}
	if s.Phase != Prepared {
		return ErrRecoveryRequired
	}
	return c.run(ctx, s, false)
}
func (c *Coordinator) Recover(ctx context.Context) error {
	s, err := c.load(ctx)
	if err != nil {
		return err
	}
	if s.Phase == Succeeded || s.Phase == Recovered {
		return nil
	}
	if s.Phase == Prepared {
		return errors.New("handoff has not begun")
	}
	if s.CommitEstablished {
		// Acknowledged publication committed. Reconcile this selected release;
		// do not reinterpret lost finalization as an authorization to roll back.
		return c.run(ctx, s, s.Recovering)
	}
	return c.run(ctx, s, true)
}
func (c *Coordinator) run(ctx context.Context, s State, recovering bool) (result error) {
	// Preflight is the only effect permitted before the durable closure intent.
	preflightCtx, cancel := context.WithTimeout(ctx, c.Request.Budgets.Phase)
	err := c.Effects.Preflight(preflightCtx)
	if err == nil {
		err = preflightCtx.Err()
	}
	cancel()
	if err != nil {
		return fmt.Errorf("managed preflight: %w", err)
	}
	if recovering || s.CommitEstablished {
		s.StartedAt = time.Now().UTC()
		s.Deadline = s.StartedAt.Add(c.Request.Budgets.Total)
	}
	s.Recovering = recovering
	operationCtx, stop := context.WithDeadline(ctx, s.Deadline)
	defer stop()
	if err = operationCtx.Err(); err != nil {
		return err
	}
	selected := c.Request.Candidate
	if recovering {
		selected = c.Request.Predecessor
	}
	began := false
	defer func() {
		if result == nil || !began {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), c.Request.Budgets.Phase)
		defer cleanupCancel()
		result = errors.Join(result, c.Effects.CloseAdmission(cleanupCtx), c.Effects.CloseIngress(cleanupCtx))
	}()
	steps := []struct {
		phase Phase
		run   func(context.Context) error
	}{
		{ClosingWork, c.Effects.CloseAdmission},
		{ClosingIngress, c.Effects.CloseIngress},
		{Stopping, c.Effects.DrainAndStop},
		{Starting, func(ctx context.Context) error { return c.Effects.StartPrepared(ctx, selected) }},
		{Verifying, func(ctx context.Context) error { return c.Effects.VerifyPrepared(ctx, selected) }},
		{OpeningWork, func(ctx context.Context) error { return c.Effects.OpenWork(ctx, selected) }},
		{OpeningIngress, func(ctx context.Context) error { return c.Effects.OpenIngress(ctx, selected) }},
	}
	for _, step := range steps {
		if err = operationCtx.Err(); err != nil {
			return err
		}
		s.Phase = step.phase
		if err = c.Journal.Save(operationCtx, s); err != nil {
			return err
		}
		began = true
		phaseCtx, phaseCancel := context.WithTimeout(operationCtx, c.Request.Budgets.Phase)
		err = step.run(phaseCtx)
		if err == nil {
			err = phaseCtx.Err()
		}
		phaseCancel()
		if err != nil {
			return fmt.Errorf("managed %s: %w", step.phase, err)
		}
	}
	s.Phase = Committed
	s.CommitEstablished = true
	if err = c.Journal.Save(operationCtx, s); err != nil {
		return err
	}
	phaseCtx, phaseCancel := context.WithTimeout(operationCtx, c.Request.Budgets.Phase)
	err = c.Effects.FinalizeWork(phaseCtx, selected)
	if err == nil {
		err = phaseCtx.Err()
	}
	phaseCancel()
	if err != nil {
		return err
	}
	s.Phase = Succeeded
	if recovering {
		s.Phase = Recovered
	}
	return c.Journal.Save(operationCtx, s)
}
