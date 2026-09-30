package runtimehost

import (
	"context"
	"crypto/sha256"
	"errors"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// CandidateRetirementRequest selects registered runtime instances by their
// complete candidate identity and canonical compatibility evidence.
type CandidateRetirementRequest struct {
	CandidateID, OwnerID string
	Identity             projectgraph.ServingIdentity
	Compatibility        CandidateCompatibility
}

// CandidateRetirement is process-local evidence for the instances captured by
// RetireCandidate. Keep this handle to retry Wait after cancellation or after
// completed generations have been removed from the registry.
type CandidateRetirement struct {
	targets  []*candidateGeneration
	priorErr error
}

// RetireCandidate fences the exact matching current instance and captures all
// matching retired instances under the same lock. A missing match is an error,
// never evidence that cleanup succeeded. Earlier unattributed cleanup failures
// are captured on the handle without preventing the retirement fence.
//
// This does not fence preparation or registration. Before using it for a
// credential transition, the coordinator must pause and drain candidate
// preparation starting before connection acquisition, and prevent registration
// through completion. The existing source-work pause alone does not do that.
func (r *Registry) RetireCandidate(request CandidateRetirementRequest) (*CandidateRetirement, error) {
	if r == nil || r.candidates == nil || r.manager == nil {
		return nil, ErrCandidateRuntimeClosed
	}
	if request.Identity.Validate() != nil {
		return nil, ErrCandidateRuntimeInvalid
	}
	normalized, fingerprint, err := normalizeLeaseRequest(CandidateLeaseRequest{
		CandidateID: request.CandidateID, OwnerID: request.OwnerID,
		ProjectID: request.Identity.ProjectID, Compatibility: request.Compatibility,
	}, r.now())
	if err != nil {
		return nil, err
	}
	if request.Identity.ProjectID != r.ProjectID() || request.Identity.Environment != string(r.Environment()) {
		return nil, ErrCandidateRuntimeNotFound
	}
	handle, drained, err := r.candidates.retire(request.Identity, normalized, fingerprint)
	if err != nil {
		return nil, err
	}
	for _, g := range drained {
		r.cleanupCandidateGeneration(g)
	}
	return handle, nil
}

func (r *candidateRuntimeRegistry) retire(identity projectgraph.ServingIdentity, request CandidateLeaseRequest, fingerprint [sha256.Size]byte) (*CandidateRetirement, []*candidateGeneration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil, ErrCandidateRuntimeClosed
	}
	matches := func(g *candidateGeneration) bool {
		return g != nil && g.key.candidateID == request.CandidateID &&
			g.ownerID == request.OwnerID && g.projectID == identity.ProjectID &&
			g.managed.identity == identity && g.fingerprint == fingerprint
	}
	handle := &CandidateRetirement{priorErr: r.cleanupErr}
	var drained []*candidateGeneration
	for g := range r.retired {
		if matches(g) {
			handle.targets = append(handle.targets, g)
			if g.refs == 0 {
				drained = append(drained, g)
			}
		}
	}
	if g := r.current[candidateRuntimeKey{candidateID: request.CandidateID}]; matches(g) {
		handle.targets = append(handle.targets, g)
		if target := r.retireLocked(g); target != nil {
			drained = append(drained, target)
		}
	}
	if len(handle.targets) == 0 {
		return nil, nil, ErrCandidateRuntimeNotFound
	}
	return handle, drained, nil
}

// Wait waits for synchronous cleanup of the captured instances and returns its
// errors, including any failure already known when retirement began. Cancellation
// stops only this wait; it does not cancel cleanup or restore admission. Calls
// may be concurrent and repeated, including after registry shutdown. Later
// replacements and unrelated cleanup failures cannot change the captured set.
//
// Success proves neither shared-pool closure nor snapshot-release queue drain,
// live credential readiness, or old-credential revocation safety. Prepared but
// unregistered candidates are outside this handle's ownership.
func (h *CandidateRetirement) Wait(ctx context.Context) error {
	if h == nil || len(h.targets) == 0 || ctx == nil {
		return ErrCandidateRuntimeInvalid
	}
	errs := []error{h.priorErr}
	for _, g := range h.targets {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		select {
		case <-g.cleanupDone:
			errs = append(errs, g.cleanupErr)
		case <-ctx.Done():
			return errors.Join(append(errs, ctx.Err())...)
		}
	}
	return errors.Join(append(errs, ctx.Err())...)
}
