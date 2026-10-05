package module

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/google/uuid"
)

const credentialProbeTimeout = 30 * time.Second

var (
	ErrCredentialProbeFailed        = errors.New("target credential probe failed")
	ErrCredentialProbeCleanupFailed = errors.New("target credential probe cleanup failed")
)

// CredentialProbePolicyIdentity identifies the fixed, secret-free policy used
// to validate a PostgreSQL target credential. It is suitable for inclusion in
// a validation receipt's configuration digest.
func (m *Module) CredentialProbePolicyIdentity() string {
	if m == nil || m.connectionBindings == nil || m.connectionFactory == nil {
		return ""
	}
	if m.targetClass != connectionbinding.TargetProduction && m.targetClass != connectionbinding.TargetDevelopment {
		return ""
	}
	egressPolicy := "unrestricted-target-egress-v1"
	if m.production {
		egressPolicy = "explicit-private-target-egress-v1"
	}
	return fmt.Sprintf(
		"target-postgres-password-read-only-probe-v1/target-%s/%s",
		m.targetClass, egressPolicy,
	)
}

// ProbeCredential validates a saved PostgreSQL password against the exact
// enabled target binding. It uses a transient isolated DuckDB candidate and
// never touches the shared pool directory or binding state. The caller owns
// and must clear fields after this call.
func (m *Module) ProbeCredential(
	ctx context.Context,
	binding connectionbinding.TargetBinding,
	credentialVersion string,
	fields map[string]string,
) error {
	if ctx == nil || m == nil || m.connectionBindings == nil || m.connectionFactory == nil ||
		m.CredentialProbePolicyIdentity() == "" {
		return connectionbinding.ErrProviderUnavailable
	}
	if err := validateCredentialProbeInput(m, binding, credentialVersion, fields); err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, credentialProbeTimeout)
	defer cancel()

	current, err := m.currentCredentialProbeBinding(probeCtx, binding)
	if err != nil {
		return err
	}
	if err := probeCtx.Err(); err != nil {
		return credentialProbeError(ErrCredentialProbeFailed, err)
	}
	snapshot, err := connectionbinding.NewCredentialSnapshot(
		fields, "credential-draft:"+credentialVersion, time.Now().UTC(), time.Time{},
	)
	if err != nil {
		return connectionbinding.ErrInvalidCredentialBundle
	}
	defer snapshot.Destroy()

	candidate, prepareErr := m.connectionFactory.Prepare(probeCtx, current, snapshot)
	if prepareErr != nil {
		if candidate != nil {
			if closeErr := closeCredentialProbe(candidate, probeCtx); closeErr != nil {
				return credentialProbeError(ErrCredentialProbeCleanupFailed, closeErr)
			}
		}
		return credentialProbeError(ErrCredentialProbeFailed, prepareErr)
	}
	if candidate == nil {
		return ErrCredentialProbeFailed
	}
	healthErr := candidate.HealthCheck(probeCtx)
	if closeErr := closeCredentialProbe(candidate, probeCtx); closeErr != nil {
		return credentialProbeError(ErrCredentialProbeCleanupFailed, closeErr)
	}
	if healthErr != nil {
		return credentialProbeError(ErrCredentialProbeFailed, healthErr)
	}
	if err := probeCtx.Err(); err != nil {
		return credentialProbeError(ErrCredentialProbeFailed, err)
	}
	if _, err := m.currentCredentialProbeBinding(probeCtx, current); err != nil {
		return err
	}
	if err := probeCtx.Err(); err != nil {
		return credentialProbeError(ErrCredentialProbeFailed, err)
	}
	return nil
}

func validateCredentialProbeInput(
	module *Module,
	binding connectionbinding.TargetBinding,
	credentialVersion string,
	fields map[string]string,
) error {
	if binding.Validate() != nil || !binding.Enabled ||
		binding.TargetID.String() != module.targetID || binding.Scope.Environment != module.targetEnvironment ||
		binding.ConnectorKind != "postgres" ||
		binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle {
		return connectionbinding.ErrIncompatibleBinding
	}
	versionID, err := uuid.Parse(credentialVersion)
	if err != nil || versionID.String() != credentialVersion {
		return connectionbinding.ErrInvalidCredentialBundle
	}
	password, ok := fields["password"]
	if len(fields) != 1 || !ok || password == "" || len(password) > 16<<10 {
		return connectionbinding.ErrInvalidCredentialBundle
	}
	return nil
}

func (m *Module) currentCredentialProbeBinding(
	ctx context.Context,
	expected connectionbinding.TargetBinding,
) (connectionbinding.TargetBinding, error) {
	current, err := m.connectionBindings.Binding(
		ctx, expected.Scope, expected.TargetID, expected.ConnectionID,
	)
	if err != nil {
		if errors.Is(err, connectionbinding.ErrBindingNotFound) {
			return connectionbinding.TargetBinding{}, connectionbinding.ErrBindingNotFound
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return connectionbinding.TargetBinding{}, err
		}
		return connectionbinding.TargetBinding{}, connectionbinding.ErrProviderUnavailable
	}
	if current.Validate() != nil || !current.Enabled || !sameCredentialProbeBinding(expected, current) {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrIncompatibleBinding
	}
	return current, nil
}

func sameCredentialProbeBinding(left, right connectionbinding.TargetBinding) bool {
	return left.ID == right.ID && left.TargetID == right.TargetID && left.ConnectionID == right.ConnectionID &&
		left.Scope == right.Scope && left.ConnectorKind == right.ConnectorKind &&
		left.AuthenticationMode == right.AuthenticationMode && left.Enabled == right.Enabled &&
		left.Revision == right.Revision && left.CredentialReference == right.CredentialReference &&
		reflect.DeepEqual(left.Endpoint, right.Endpoint) &&
		left.Evidence().EndpointConfigHash == right.Evidence().EndpointConfigHash
}

func closeCredentialProbe(pool connectionbinding.RuntimePool, ctx context.Context) error {
	if closer, ok := pool.(connectionbinding.ContextRuntimePool); ok {
		return closer.CloseContext(ctx)
	}
	return pool.Close()
}

func credentialProbeError(safe error, cause error) error {
	switch {
	case errors.Is(cause, context.Canceled):
		return errors.Join(safe, context.Canceled)
	case errors.Is(cause, context.DeadlineExceeded):
		return errors.Join(safe, context.DeadlineExceeded)
	default:
		return safe
	}
}
