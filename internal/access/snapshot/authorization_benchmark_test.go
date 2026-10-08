package snapshot

import (
	"errors"
	"fmt"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
	"github.com/flidai/leapview/internal/project/graph"
)

type authorizationBenchmarkSize struct {
	name                                  string
	resources, grants, bindings, policies int
}

// Vary one dimension at a time, retaining one shared all-1 baseline. These
// cardinalities describe fixture size, not concurrent users or capacity.
func authorizationBenchmarkSizes() []authorizationBenchmarkSize {
	sizes := []authorizationBenchmarkSize{{"base", 1, 1, 1, 1}}
	for _, count := range []int{10, 20, 100} {
		sizes = append(sizes,
			authorizationBenchmarkSize{fmt.Sprintf("resources=%d", count), count, 1, 1, 1},
			authorizationBenchmarkSize{fmt.Sprintf("grants=%d", count), 1, count, 1, 1},
			authorizationBenchmarkSize{fmt.Sprintf("bindings=%d", count), 1, 1, count, 1},
			authorizationBenchmarkSize{fmt.Sprintf("policies=%d", count), 1, 1, 1, count},
		)
	}
	return sizes
}

type authorizationBenchmarkFixture struct {
	snapshot        AuthorizationSnapshot
	subject         access.SubjectRef
	allowed, denied access.PermissionPair
}

func newAuthorizationBenchmarkFixture(tb testing.TB, size authorizationBenchmarkSize) authorizationBenchmarkFixture {
	tb.Helper()
	check := func(err error) {
		tb.Helper()
		if err != nil {
			tb.Fatal(err)
		}
	}
	identity := graph.ServingIdentity{ProjectID: "project_benchmark", Environment: "production", GenerationID: "generation_benchmark"}
	resources := make([]graph.Resource, size.resources)
	for i := range resources {
		resources[i] = graph.Resource{ID: graph.ResourceID(fmt.Sprintf("model_%03d", i)), Kind: graph.KindModel, Name: fmt.Sprintf("model_%03d", i)}
	}
	project, err := graph.NewProjectGraph(resources, nil)
	check(err)
	resource, err := access.NewResourceRef(resources[0].ID, graph.KindModel)
	check(err)
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "allowed_principal")
	check(err)
	allowed, err := access.NewExactPermissionPair(access.ActionModelRead, identity.ProjectID, resource)
	check(err)
	denied, err := access.NewExactPermissionPair(access.ActionModelUpdate, identity.ProjectID, resource)
	check(err)
	grants := make([]Grant, size.grants)
	for i := range grants {
		grantSubject := subject
		if i > 0 {
			grantSubject, err = access.NewSubjectRef(access.SubjectKindPrincipal, fmt.Sprintf("grant_subject_%03d", i))
			check(err)
		}
		grants[i], err = NewTypedGrant(fmt.Sprintf("grant_%03d", i), "", grantSubject, []access.PermissionPair{allowed})
		check(err)
	}
	bindings := make([]RoleBinding, size.bindings)
	for i := range bindings {
		bindingSubject, err := access.NewSubjectRef(access.SubjectKindPrincipal, fmt.Sprintf("binding_subject_%03d", i))
		check(err)
		bindings[i], err = access.NewTypedRoleBinding(fmt.Sprintf("binding_%03d", i), "", bindingSubject, access.PermissionRoleViewer, identity.ProjectID)
		check(err)
	}
	policies := make([]DataPolicy, size.policies)
	for i := range policies {
		policies[i] = DataPolicy{
			ID: fmt.Sprintf("policy_%03d", i), Resource: resource, PolicyType: accesspolicy.TypeRowFilter,
			ExpressionJSON: `{"field":"tenant_id","operator":"in","values":["tenant_benchmark"]}`,
		}
	}
	snapshot, err := NewAuthorizationSnapshotWithRoleBindings(identity, project, bindings, grants, policies)
	check(err)
	return authorizationBenchmarkFixture{snapshot: snapshot, subject: subject, allowed: allowed, denied: denied}
}

// BenchmarkAuthorizationSnapshotAllowsTyped measures one production typed
// decision, including all bound-graph validation and reconstruction performed
// by AllowsTyped and EffectiveTypedPermissions. Setup is outside the timer;
// each timed call includes its outcome/error check. The allowed case uses a
// direct grant; bindings belong to other subjects. Data policies contribute
// snapshot validation costs here, not governed row-filter execution costs.
func BenchmarkAuthorizationSnapshotAllowsTyped(b *testing.B) {
	for _, size := range authorizationBenchmarkSizes() {
		b.Run(size.name, func(b *testing.B) {
			fixture := newAuthorizationBenchmarkFixture(b, size)
			for _, decision := range []struct {
				name string
				pair access.PermissionPair
				want bool
			}{{"allowed", fixture.allowed, true}, {"denied", fixture.denied, false}} {
				b.Run(decision.name, func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						allowed, err := fixture.snapshot.AllowsTyped(fixture.subject, decision.pair)
						if err != nil || allowed != decision.want {
							b.Fatalf("AllowsTyped = (%v, %v), want (%v, nil)", allowed, err, decision.want)
						}
					}
				})
			}
		})
	}
}

// BenchmarkAuthorizationSnapshotValidateBound measures the production
// revalidation/reconstruction boundary separately for attribution. It does
// not remove that work from the AllowsTyped benchmark above.
func BenchmarkAuthorizationSnapshotValidateBound(b *testing.B) {
	for _, size := range authorizationBenchmarkSizes() {
		b.Run(size.name, func(b *testing.B) {
			fixture := newAuthorizationBenchmarkFixture(b, size)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := fixture.snapshot.ValidateBound(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Keep scaling fixtures honest: exact cardinalities must remain independently
// variable, and unrelated subjects and project namespaces must not acquire
// authority while the allowed/denied benchmark outcomes remain meaningful.
func TestAuthorizationSnapshotBenchmarkFixtures(t *testing.T) {
	for _, size := range authorizationBenchmarkSizes() {
		t.Run(size.name, func(t *testing.T) {
			fixture := newAuthorizationBenchmarkFixture(t, size)
			actual := []int{len(fixture.snapshot.Project().Resources()), len(fixture.snapshot.Grants()), len(fixture.snapshot.RoleBindings()), len(fixture.snapshot.DataPolicies())}
			want := []int{size.resources, size.grants, size.bindings, size.policies}
			for i := range want {
				if actual[i] != want[i] {
					t.Fatalf("fixture cardinalities = %v, want %v", actual, want)
				}
			}
			for _, decision := range []struct {
				subject access.SubjectRef
				pair    access.PermissionPair
				want    bool
			}{
				{fixture.subject, fixture.allowed, true},
				{fixture.subject, fixture.denied, false},
				{access.SubjectRef{Kind: access.SubjectKindGroup, ID: fixture.subject.ID}, fixture.allowed, false},
			} {
				allowed, err := fixture.snapshot.AllowsTyped(decision.subject, decision.pair)
				if err != nil || allowed != decision.want {
					t.Fatalf("AllowsTyped(%v, %v) = (%v, %v), want (%v, nil)", decision.subject, decision.pair, allowed, err, decision.want)
				}
			}
			otherProject := fixture.allowed
			otherProject.Target.ProjectID = "project_other"
			allowed, err := fixture.snapshot.AllowsTyped(fixture.subject, otherProject)
			if allowed || !errors.Is(err, ErrTypedSnapshotProjectMismatch) {
				t.Fatalf("cross-project decision = (%v, %v), want namespace rejection", allowed, err)
			}
		})
	}
}
