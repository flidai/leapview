package app

import (
	"context"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

// candidatePreparationAdmitter charges standalone candidate preparation to
// the control workload class. A refresh dispatcher has already admitted the
// complete refresh operation, including its canonical candidate build, so
// that path reuses the outer refresh lease instead of attempting a conflicting
// nested control admission.
func candidatePreparationAdmitter(admitter workloadmodule.Admitter, request workloadmodule.Request) deploymentmodule.CandidatePreparationAdmitter {
	return deploymentmodule.CandidatePreparationAdmitterFunc(func(ctx context.Context) (deploymentmodule.CandidatePreparationLease, error) {
		if class, _, admitted := workloadmodule.Current(ctx); admitted && class == workloadmodule.RefreshClass {
			return inheritedCandidatePreparationLease{ctx: ctx}, nil
		}
		return admitter.Acquire(ctx, request)
	})
}

type inheritedCandidatePreparationLease struct {
	ctx context.Context
}

func (lease inheritedCandidatePreparationLease) Context() context.Context { return lease.ctx }
func (inheritedCandidatePreparationLease) Release()                       {}
