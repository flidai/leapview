package app

import (
	"context"
	"sync"

	"github.com/flidai/leapview/internal/analytics/sourcework"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/platform/typednil"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

// candidatePreparationAdmission owns a separate lifecycle gate for the
// deployment module's preparation entry points. Never share this gate with
// analytics source work: an admitted preparation may acquire source work while
// opening a connection pool. A coordinator must pause and drain this gate before
// pausing source work, and retain both pauses through the credential transition.
type candidatePreparationAdmission struct {
	gate     sourcework.Gate
	admitter workloadmodule.Admitter
	request  workloadmodule.Request
}

func candidatePreparationAdmitter(admitter workloadmodule.Admitter, request workloadmodule.Request) *candidatePreparationAdmission {
	return &candidatePreparationAdmission{admitter: admitter, request: request}
}

// Pause fences new preparations. Draining proves admitted calls have returned,
// not that registered runtimes or shared pools have been retired successfully.
// Cancellation leaves the pause in place; only its owner may explicitly resume.
func (admission *candidatePreparationAdmission) Pause() (*sourcework.Pause, error) {
	if admission == nil {
		return nil, sourcework.ErrClosed
	}
	return admission.gate.Pause()
}

func (admission *candidatePreparationAdmission) AcquireCandidatePreparation(ctx context.Context) (deploymentmodule.CandidatePreparationLease, error) {
	if admission == nil || ctx == nil || typednil.IsNil(admission.admitter) {
		return nil, deployment.ErrCandidateUnavailable
	}
	lifecycle, err := admission.gate.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	// An inherited refresh lease replaces only workload charging. It never
	// bypasses the preparation lifecycle fence or releases its outer lease.
	if class, _, admitted := workloadmodule.Current(ctx); admitted && class == workloadmodule.RefreshClass {
		return &candidatePreparationLease{ctx: ctx, lifecycle: lifecycle}, nil
	}
	workload, err := admission.admitter.Acquire(ctx, admission.request)
	if err != nil || typednil.IsNil(workload) {
		if !typednil.IsNil(workload) {
			workload.Release()
		}
		lifecycle.Release()
		if err == nil {
			err = deployment.ErrCandidateUnavailable
		}
		return nil, err
	}
	workContext := workload.Context()
	if workContext == nil {
		workload.Release()
		lifecycle.Release()
		return nil, deployment.ErrCandidateUnavailable
	}
	return &candidatePreparationLease{ctx: workContext, workload: workload, lifecycle: lifecycle}, nil
}

type candidatePreparationLease struct {
	ctx       context.Context
	workload  workloadmodule.Lease
	lifecycle *sourcework.Lease
	once      sync.Once
}

func (lease *candidatePreparationLease) Context() context.Context { return lease.ctx }

// Release is called after registration or synchronous failure cleanup returns.
// Context cancellation alone never releases preparation admission.
func (lease *candidatePreparationLease) Release() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		if lease.workload != nil {
			lease.workload.Release()
		}
		lease.lifecycle.Release()
	})
}
