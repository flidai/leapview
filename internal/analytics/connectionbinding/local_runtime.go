package connectionbinding

import "context"

// LocalCredentialPins resolves only server-owned candidate-operation or
// committed-generation pins. Returning empty selects the existing provider.
type LocalCredentialPins func(context.Context, RuntimeBindingRequest, TargetBinding) (string, error)
type LocalCredentialPools interface {
	AcquireLocal(context.Context, TargetBinding, string, string) (ValidatedPoolLease, error)
}

// ConfigureLocalCredentials is a one-time startup composition seam. It must
// run before the leaser is exposed to workers; no runtime resolver is replaced.
func (leaser *RuntimeBindingLeaser) ConfigureLocalCredentials(pins LocalCredentialPins, pools LocalCredentialPools) error {
	if leaser == nil || pins == nil || pools == nil || leaser.localPins != nil || leaser.localPools != nil {
		return ErrProviderUnavailable
	}
	leaser.localPins, leaser.localPools = pins, pools
	return nil
}

func (leaser *RuntimeBindingLeaser) localVersion(ctx context.Context, request RuntimeBindingRequest, binding TargetBinding) (string, error) {
	if leaser.localPins == nil {
		return "", nil
	}
	version, err := leaser.localPins(ctx, request, binding)
	if err != nil {
		return "", err
	}
	if version != "" && ((CredentialIdentity{CredentialVersionID: version}).Validate() != nil || binding.AuthenticationMode == AuthenticationNone) {
		return "", ErrIncompatibleBinding
	}
	return version, nil
}

func (leaser *RuntimeBindingLeaser) acquireRuntimePool(ctx context.Context, request RuntimeBindingRequest, binding TargetBinding) (ValidatedPoolLease, error) {
	version, err := leaser.localVersion(ctx, request, binding)
	if err != nil {
		return nil, err
	}
	if version == "" {
		return leaser.pools.AcquireValidated(ctx, binding, request.Actor)
	}
	if leaser.localPools == nil {
		return nil, ErrProviderUnavailable
	}
	lease, err := leaser.localPools.AcquireLocal(ctx, binding, version, request.Actor)
	if err != nil {
		return nil, err
	}
	if lease == nil {
		return nil, ErrProviderUnavailable
	}
	evidence := lease.Evidence()
	if evidence.CredentialVersionID != version || evidence.ValidatedVersion != "" || evidence.BindingRevision != binding.Revision || evidence.EndpointConfigHash != binding.Evidence().EndpointConfigHash {
		lease.Release()
		return nil, ErrIncompatibleBinding
	}
	return lease, nil
}

func (leaser *RuntimeBindingLeaser) inspectRuntimeBinding(ctx context.Context, request RuntimeBindingRequest, binding TargetBinding) (BindingEvidence, error) {
	version, err := leaser.localVersion(ctx, request, binding)
	if err != nil {
		return BindingEvidence{}, err
	}
	evidence := binding.Evidence()
	if version != "" {
		evidence.ValidatedVersion = ""
		evidence.CredentialVersionID = version
		return evidence, nil
	}
	if binding.Health != HealthHealthy || binding.ValidatedVersion == "" || binding.LastValidatedAt.IsZero() {
		return BindingEvidence{}, ErrIncompatibleBinding
	}
	return evidence, nil
}
