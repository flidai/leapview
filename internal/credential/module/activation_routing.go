package module

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/platform/typednil"
)

// ActivationRouting keeps both source and agent changes behind one durable
// installation journal and one reopenable provider-work admission barrier.
type ActivationRouting struct {
	services                    *Services
	instanceID                  string
	source, agent               credential.ActivationAuthority
	sourceRuntime, agentRuntime credential.ActivationRuntime
}

func NewActivationRouting(services *Services, instanceID string, source, agent credential.ActivationAuthority, sourceRuntime, agentRuntime credential.ActivationRuntime) (*ActivationRouting, error) {
	if services == nil || services.repository == nil || !canonicalCredentialValue(instanceID) || typednil.IsNil(source) || typednil.IsNil(agent) || typednil.IsNil(sourceRuntime) || typednil.IsNil(agentRuntime) {
		return nil, credential.ErrUnavailable
	}
	return &ActivationRouting{services: services, instanceID: instanceID, source: source, agent: agent, sourceRuntime: sourceRuntime, agentRuntime: agentRuntime}, nil
}
func (r *ActivationRouting) authority(resource credential.Resource) (credential.ActivationAuthority, error) {
	if resource.Validate() != nil {
		return nil, credential.ErrNotFound
	}
	switch resource.ScopeKind {
	case "connection":
		return r.source, nil
	case "agent":
		return r.agent, nil
	default:
		return nil, credential.ErrNotFound
	}
}
func (r *ActivationRouting) AuthorizeMutation(ctx context.Context, actor string, resource credential.Resource) error {
	a, err := r.authority(resource)
	if err != nil {
		return err
	}
	return a.AuthorizeMutation(ctx, actor, resource)
}
func (r *ActivationRouting) Prepare(ctx context.Context, actor string, resource credential.Resource, request credential.ActivationRequest) (credential.ActivationRecord, error) {
	a, err := r.authority(resource)
	if err != nil {
		return credential.ActivationRecord{}, err
	}
	return a.Prepare(ctx, actor, resource, request)
}
func (r *ActivationRouting) Read(ctx context.Context, actor string, resource credential.Resource, id string) (credential.ActivationRecord, error) {
	a, err := r.authority(resource)
	if err != nil {
		return credential.ActivationRecord{}, err
	}
	return a.Read(ctx, actor, resource, id)
}
func (r *ActivationRouting) Pending(ctx context.Context) (credential.ActivationRecord, error) {
	record, err := r.services.repository.GetPendingActivationRequest(ctx, r.instanceID)
	if errors.Is(err, credential.ErrNotFound) {
		// Historical phase journals remain a fence. Never reopen merely because
		// an older operation predates the request journal.
		_, previousErr := r.services.repository.GetPendingActivation(ctx, r.instanceID)
		if previousErr == nil {
			return credential.ActivationRecord{}, credential.ErrConflict
		}
		if !errors.Is(previousErr, credential.ErrNotFound) {
			return credential.ActivationRecord{}, previousErr
		}
	}
	if err != nil {
		return credential.ActivationRecord{}, err
	}
	if _, err = r.authority(record.Resource()); err != nil {
		return credential.ActivationRecord{}, err
	}
	return record.ActivationRecord(), nil
}
func (r *ActivationRouting) Switch(ctx context.Context, actor string, record credential.ActivationRecord, receipt string) (credential.ActivationRecord, error) {
	a, err := r.authority(record.Resource)
	if err != nil {
		return credential.ActivationRecord{}, err
	}
	return a.Switch(ctx, actor, record, receipt)
}
func (r *ActivationRouting) Commit(ctx context.Context, actor string, record credential.ActivationRecord) (credential.ActivationRecord, error) {
	a, err := r.authority(record.Resource)
	if err != nil {
		return credential.ActivationRecord{}, err
	}
	return a.Commit(ctx, actor, record)
}
func (r *ActivationRouting) Complete(ctx context.Context, record credential.ActivationRecord) (credential.ActivationRecord, error) {
	a, err := r.authority(record.Resource)
	if err != nil {
		return credential.ActivationRecord{}, err
	}
	return a.Complete(ctx, record)
}
func (r *ActivationRouting) Abort(ctx context.Context, actor string, record credential.ActivationRecord) (credential.ActivationRecord, error) {
	a, err := r.authority(record.Resource)
	if err != nil {
		return credential.ActivationRecord{}, err
	}
	return a.Abort(ctx, actor, record)
}
func (r *ActivationRouting) InstallCommitted(ctx context.Context, record credential.ActivationRecord) error {
	if _, err := r.authority(record.Resource); err != nil {
		return err
	}
	if record.Resource.ScopeKind == "agent" {
		if err := r.sourceRuntime.RestoreCurrent(ctx); err != nil {
			return err
		}
		return r.agentRuntime.InstallCommitted(ctx, record)
	}
	if err := r.agentRuntime.RestoreCurrent(ctx); err != nil {
		return err
	}
	return r.sourceRuntime.InstallCommitted(ctx, record)
}
func (r *ActivationRouting) RestoreCurrent(ctx context.Context) error {
	return errors.Join(r.sourceRuntime.RestoreCurrent(ctx), r.agentRuntime.RestoreCurrent(ctx))
}
