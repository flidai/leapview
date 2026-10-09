package app

import (
	"context"

	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

// The approved publication worker already owns a bounded control admission.
// Reuse that lease only inside this source's live first-publication invocation;
// never erase its accounting or acquire a conflicting nested control request.
func firstSourcePublicationRuntimeAdmitted(ctx context.Context, source *sourceCredentialActivation) bool {
	i, ok := ctx.Value(firstSourcePublicationKey{}).(*firstSourcePublicationInvocation)
	if !ok || i == nil || i.owner == nil || i.owner.source != source || !i.active.Load() {
		return false
	}
	request, admitted := workloadmodule.CurrentRequest(ctx)
	return admitted && request.Class == workloadmodule.ControlClass && request.PrincipalID != "" && request.Operation == "delivery.approval.activate" && request.EstimatedMemoryBytes >= 16<<20
}
