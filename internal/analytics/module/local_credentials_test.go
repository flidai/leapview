package module

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

type localTestAdmission struct {
	releases int
	denied   bool
}

func (a *localTestAdmission) Acquire(ctx context.Context) (context.Context, func(), error) {
	if a.denied {
		return nil, nil, errors.New("closed")
	}
	return ctx, func() { a.releases++ }, nil
}

type localTestPool struct {
	closeErr error
	closes   int
}

func (p *localTestPool) HealthCheck(context.Context) error { return nil }
func (p *localTestPool) Close() error                      { p.closes++; return p.closeErr }

type localTestFactory struct {
	pool     *localTestPool
	identity connectionbinding.CredentialIdentity
	secret   string
}

func (f *localTestFactory) Prepare(_ context.Context, _ connectionbinding.TargetBinding, snapshot connectionbinding.CredentialSnapshot) (connectionbinding.RuntimePool, error) {
	f.identity = snapshot.Identity()
	err := snapshot.Use(func(fields map[string]string) error { f.secret = fields["password"]; return nil })
	return f.pool, err
}
func TestLocalCredentialPoolExactVersionAndAcknowledgedCleanup(t *testing.T) {
	version := "11111111-1111-4111-8111-111111111111"
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "acknowledged", true: "uncertain"}[failure], func(t *testing.T) {
			gate := &localTestAdmission{}
			pool := &localTestPool{}
			if failure {
				pool.closeErr = errors.New("cleanup failed")
			}
			factory := &localTestFactory{pool: pool}
			m := &Module{connectionFactory: factory}
			var readVersion string
			err := m.ConfigureLocalCredentials(func(_ context.Context, _ connectionbinding.TargetBinding, exact string, consume func(map[string]string) error) error {
				readVersion = exact
				return consume(map[string]string{"username": "analyst", "password": "secret"})
			}, gate)
			if err != nil {
				t.Fatal(err)
			}
			binding := connectionbinding.TargetBinding{ConnectorKind: "postgres", Revision: 3, LastValidatedAt: time.Now()}
			lease, err := m.AcquireLocal(t.Context(), binding, version, "actor")
			if err != nil {
				t.Fatal(err)
			}
			if readVersion != version || factory.identity.CredentialVersionID != version || factory.identity.ProviderVersion != "" || factory.secret != "secret" {
				t.Fatal("pool lost exact local identity")
			}
			if lease.Evidence().CredentialVersionID != version || lease.Evidence().ValidatedVersion != "" {
				t.Fatal("pool evidence lost exact local pin")
			}
			if gate.releases != 0 {
				t.Fatal("provider released before cleanup")
			}
			lease.Release()
			lease.Release()
			want := 1
			if failure {
				want = 0
			}
			if pool.closes != 1 || gate.releases != want {
				t.Fatalf("closes=%d releases=%d", pool.closes, gate.releases)
			}
		})
	}
}
func TestLocalCredentialPoolClosedAdmissionDoesNotReadSecret(t *testing.T) {
	m := &Module{connectionFactory: &localTestFactory{pool: &localTestPool{}}}
	_ = m.ConfigureLocalCredentials(func(context.Context, connectionbinding.TargetBinding, string, func(map[string]string) error) error {
		t.Fatal("read while closed")
		return nil
	}, &localTestAdmission{denied: true})
	if _, err := m.AcquireLocal(t.Context(), connectionbinding.TargetBinding{ConnectorKind: "postgres"}, "11111111-1111-4111-8111-111111111111", ""); err == nil {
		t.Fatal("closed gate admitted pool")
	}
}

func TestActiveLocalCredentialPinRejectsDestinationDriftBeforeDecryption(t *testing.T) {
	binding := activeTestBinding(t)
	binding.ConnectorKind = "postgres"
	binding.Endpoint.Database = "warehouse"
	binding.Endpoint.SourceIdentity = "primary"
	binding.AuthenticationMode = connectionbinding.AuthenticationExternalBundle
	evidence := binding.Evidence()
	version := "11111111-1111-4111-8111-111111111111"
	for _, drift := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "changed destination"}[drift], func(t *testing.T) {
			current := binding
			if drift {
				current.Endpoint.Host = "changed.internal"
				current.Revision++
			}
			external := &activeVersionedResolver{}
			m := activeTestModule(current, external, ActiveRuntimeBindingEvidence{BindingID: evidence.BindingID, ConnectionID: evidence.ConnectionID, ConnectorKind: evidence.ConnectorKind, Revision: evidence.BindingRevision, CredentialVersionID: version, EndpointConfigHash: evidence.EndpointConfigHash})
			calls := 0
			_ = m.ConfigureLocalCredentials(func(_ context.Context, _ connectionbinding.TargetBinding, exact string, consume func(map[string]string) error) error {
				calls++
				if exact != version {
					t.Fatal("substituted version")
				}
				return consume(map[string]string{"token": "pinned"})
			}, &localTestAdmission{})
			resolver := &activeRuntimeConnectionResolver{module: m, servingStateID: "state_sales", projectID: "sales", environment: "prod"}
			_, err := resolver.Resolve(t.Context(), binding.ConnectionID.String(), semanticmodel.Connection{Kind: "postgres"})
			if drift {
				if !errors.Is(err, connectionbinding.ErrIncompatibleBinding) || calls != 0 {
					t.Fatalf("drift err=%v reads=%d", err, calls)
				}
			} else if err != nil || calls != 1 {
				t.Fatalf("exact err=%v reads=%d", err, calls)
			}
			if external.calls != 0 {
				t.Fatal("local pin fell back to external provider")
			}
		})
	}
}
