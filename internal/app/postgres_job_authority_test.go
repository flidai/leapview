package app

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	jobsmodule "github.com/flidai/leapview/internal/platform/jobs/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/permissions"
)

// Exercise the PostgreSQL composition's mode selection with persisted PAT
// evidence and an immutable serving authorization snapshot. Development may
// retain its zero-envelope compatibility, but real credentials must revalidate.
func TestPostgresJobAuthorityRevalidatesDevelopmentPAT(t *testing.T) {
	for _, environment := range []string{"dev", "prod"} {
		t.Run(environment, func(t *testing.T) {
			production := environment == "prod"
			identity, err := projectgraph.NewServingIdentity(postgresJourneyProject, environment, postgresRefreshJourneyGeneration)
			if err != nil {
				t.Fatal(err)
			}
			runtime, pair := newPostgresRefreshJourneyRuntime(t, identity)
			fixture := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{TargetID: postgresRefreshJourneyInstance, RuntimeHost: runtime, SkipRouteAssembly: true})
			if _, err := fixture.SeedPrincipal(t.Context(), access.PrincipalInput{ID: postgresRefreshJourneyPrincipal, Kind: access.PrincipalKindUser, Email: "authority@example.test", DisplayName: "Authority"}); err != nil {
				t.Fatal(err)
			}
			secret, _, err := fixture.Graph.Access.CreateScopedAPITokenWithMetadata(t.Context(), access.ScopedAPITokenInput{PrincipalID: postgresRefreshJourneyPrincipal, Name: "queued-refresh", Permissions: []access.PermissionPair{pair}, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			credential, err := fixture.Graph.Access.CredentialForAPIToken(t.Context(), secret)
			if err != nil {
				t.Fatal(err)
			}
			expiresAt, err := time.Parse(time.RFC3339Nano, credential.Token.ExpiresAt)
			if err != nil {
				t.Fatal(err)
			}
			contractPair, err := access.ToContractPermissionPair(pair)
			if err != nil {
				t.Fatal(err)
			}
			authority := jobs.AuthorityEnvelope{
				Profile: jobs.AuthorityEnvelopeProfile, Mode: jobs.CallerAuthorityMode,
				ActorPrincipalID: postgresRefreshJourneyPrincipal, ExecutionPrincipalID: postgresRefreshJourneyPrincipal,
				Credential:  &jobs.CredentialEvidence{Class: jobs.CredentialClassAPIToken, ID: credential.Token.ID, Fingerprint: credential.Token.TokenFingerprint, ExpiresAt: expiresAt},
				Target:      jobs.AuthorityTarget{ProjectID: identity.ProjectID.String(), Environment: environment, ResourceKind: string(projectgraph.KindPipeline), ResourceID: postgresRefreshJourneyPipeline},
				Permissions: []permissions.Pair{contractPair},
			}
			revalidator, required, err := postgresJobAuthority(production, fixture.Graph.Access, func(ctx context.Context, principalID string, permission access.PermissionPair, environment string) (bool, error) {
				return authorizeCurrentTypedPermission(ctx, fixture.AccessModule, runtime, principalID, permission, environment)
			}, postgresRefreshJourneyInstance, environment)
			if err != nil {
				t.Fatal(err)
			}
			module, err := jobsmodule.Build(t.Context(), jobsmodule.Config{Persistence: &fixture.JobsPersistence, Production: production, Admission: workloadmodule.JobAdmitter(fixture.Workload), AuthorityRevalidator: revalidator, RequiredAuthorityKinds: required})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = module.Stop(context.Background()) })
			if err := module.RevalidateAuthority(t.Context(), "refresh_pipeline", authority); err != nil {
				t.Fatalf("live %s PAT rejected: %v", environment, err)
			}
			if err := module.RevalidateAuthority(t.Context(), "refresh_pipeline", jobs.AuthorityEnvelope{}); (err != nil) != production {
				t.Fatalf("zero-envelope production invariant changed: %v", err)
			}
			invalid := authority
			invalid.Credential = &jobs.CredentialEvidence{Class: authority.Credential.Class, ID: authority.Credential.ID, Fingerprint: "wrong-fingerprint", ExpiresAt: expiresAt}
			if err := module.RevalidateAuthority(t.Context(), "refresh_pipeline", invalid); err == nil {
				t.Fatal("changed credential fingerprint accepted")
			}
			wrongTarget := authority
			wrongTarget.Target.Environment = "different"
			if err := module.RevalidateAuthority(t.Context(), "refresh_pipeline", wrongTarget); err == nil {
				t.Fatal("wrong current environment accepted")
			}
			if err := fixture.Graph.Access.RevokeAPIToken(t.Context(), credential.Token.ID); err != nil {
				t.Fatal(err)
			}
			if err := module.RevalidateAuthority(t.Context(), "refresh_pipeline", authority); err == nil {
				t.Fatal("revoked queued PAT accepted")
			}
		})
	}
}
