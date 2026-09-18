package access

import (
	"bytes"
	"errors"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/permissions"
)

func TestPermissionPairsPreserveActionResourcePairing(t *testing.T) {
	readA := mustExactPermissionPair(t, ActionDashboardRead, "project_a", "dashboard_a", projectgraph.KindDashboard)
	updateB := mustExactPermissionPair(t, ActionDashboardUpdate, "project_a", "dashboard_b", projectgraph.KindDashboard)
	credential := []PermissionPair{readA, updateB}

	if got := IntersectPermissionPairs(credential, []PermissionPair{readA, updateB}); len(got) != 2 {
		t.Fatalf("valid intersection = %v", got)
	}
	updateA := mustExactPermissionPair(t, ActionDashboardUpdate, "project_a", "dashboard_a", projectgraph.KindDashboard)
	readB := mustExactPermissionPair(t, ActionDashboardRead, "project_a", "dashboard_b", projectgraph.KindDashboard)
	if got := IntersectPermissionPairs(credential, []PermissionPair{updateA, readB}); len(got) != 0 {
		t.Fatalf("Cartesian-product authority leaked: %v", got)
	}
}

func TestPermissionPairsRequireExplicitFutureResourceSelection(t *testing.T) {
	future, err := NewFutureProjectPermissionPair(ActionDashboardRead, "project_a", projectgraph.KindDashboard)
	if err != nil {
		t.Fatalf("NewFutureProjectPermissionPair() error = %v", err)
	}
	exact := mustExactPermissionPair(t, ActionDashboardRead, "project_a", "dashboard_new", projectgraph.KindDashboard)
	if !PermissionPairAllows(future, exact) {
		t.Fatal("explicit future Dashboard selection did not cover a new exact Dashboard")
	}
	otherKind := mustExactPermissionPair(t, ActionSemanticRead, "project_a", "semantic_new", projectgraph.KindSemanticModel)
	if PermissionPairAllows(future, otherKind) {
		t.Fatal("future Dashboard selection crossed into SemanticModel authority")
	}
	otherProject := mustExactPermissionPair(t, ActionDashboardRead, "project_b", "dashboard_new", projectgraph.KindDashboard)
	if PermissionPairAllows(future, otherProject) {
		t.Fatal("future Dashboard selection crossed the Project audience")
	}
}

func TestPermissionPairsRejectOmittedButAcceptExplicitEmptyTokenScope(t *testing.T) {
	if err := ValidatePermissionPairs(nil); !errors.Is(err, ErrTokenPermissionsNeeded) {
		t.Fatalf("nil permissions error = %v", err)
	}
	if err := ValidatePermissionPairs([]PermissionPair{}); err != nil {
		t.Fatalf("explicit empty permissions error = %v", err)
	}
	if got := IntersectPermissionPairs(nil, []PermissionPair{mustExactPermissionPair(t, ActionDashboardRead, "project_a", "dashboard_a", projectgraph.KindDashboard)}); len(got) != 0 {
		t.Fatalf("nil credential permissions inherited authority: %v", got)
	}
}

func TestPermissionPairsSeparateProjectCreationAndInstanceAudience(t *testing.T) {
	create, err := NewProjectPermissionPair(ActionDashboardCreate, "project_a")
	if err != nil {
		t.Fatalf("NewProjectPermissionPair(create) error = %v", err)
	}
	if create.Target.ResourceID != "" || create.Target.IncludeFuture {
		t.Fatalf("create target widened to resource authority: %#v", create.Target)
	}
	if _, err := NewProjectPermissionPair(ActionDashboardRead, "project_a"); err == nil {
		t.Fatal("resource read accepted as an implicit Project-wide permission")
	}
	platform, err := NewInstancePermissionPair(ActionPlatformAccessManage, "instance_a")
	if err != nil {
		t.Fatalf("NewInstancePermissionPair() error = %v", err)
	}
	if PermissionPairAllows(platform, create) {
		t.Fatal("instance authority crossed into Project creation")
	}
}

func TestPermissionPairsRejectDuplicateAndUnsupportedProfiles(t *testing.T) {
	pair := mustExactPermissionPair(t, ActionDashboardRead, "project_a", "dashboard_a", projectgraph.KindDashboard)
	if err := ValidatePermissionPairs([]PermissionPair{pair, pair}); !errors.Is(err, ErrInvalidPermissionPair) {
		t.Fatalf("duplicate pair error = %v", err)
	}
	pair.Profile = "leapview.permissions/v999"
	if err := pair.Validate(); !errors.Is(err, ErrInvalidPermissionPair) {
		t.Fatalf("unsupported profile error = %v", err)
	}
}

func TestPermissionSetRequiresSemanticConsumptionWithoutImplyingIt(t *testing.T) {
	query := mustExactPermissionPair(t, ActionSemanticQuery, "project_a", "semantic_a", projectgraph.KindSemanticModel)
	consume := mustExactPermissionPair(t, ActionSemanticConsume, "project_a", "semantic_a", projectgraph.KindSemanticModel)
	if PermissionSetAllows([]PermissionPair{query}, query) {
		t.Fatal("semantic.query silently implied semantic.consume")
	}
	if !PermissionSetAllows([]PermissionPair{query, consume}, query) {
		t.Fatal("independently granted semantic.query and semantic.consume were denied")
	}
	required, err := RequiredPermissionPairs(query)
	if err != nil {
		t.Fatalf("RequiredPermissionPairs() error = %v", err)
	}
	if len(required) != 2 || required[0].Action != ActionSemanticQuery || required[1].Action != ActionSemanticConsume {
		t.Fatalf("semantic.query requirements = %#v", required)
	}
}

func TestTypedTokenPermissionAttenuationPreservesPairingAndPrerequisites(t *testing.T) {
	readA := mustExactPermissionPair(t, ActionDashboardRead, "project_a", "dashboard_a", projectgraph.KindDashboard)
	updateB := mustExactPermissionPair(t, ActionDashboardUpdate, "project_a", "dashboard_b", projectgraph.KindDashboard)
	updateA := mustExactPermissionPair(t, ActionDashboardUpdate, "project_a", "dashboard_a", projectgraph.KindDashboard)
	caller := APIToken{PermissionProfile: PermissionCatalogProfile, Permissions: []PermissionPair{readA, updateB}}
	if err := ValidateTokenPermissionAttenuation(caller, []PermissionPair{updateA}); !errors.Is(err, ErrTokenPermissionNotAllowed) {
		t.Fatalf("cross-paired issuance error = %v, want %v", err, ErrTokenPermissionNotAllowed)
	}

	query := mustExactPermissionPair(t, ActionSemanticQuery, "project_a", "semantic_a", projectgraph.KindSemanticModel)
	consume := mustExactPermissionPair(t, ActionSemanticConsume, "project_a", "semantic_a", projectgraph.KindSemanticModel)
	queryCaller := APIToken{PermissionProfile: PermissionCatalogProfile, Permissions: []PermissionPair{query, consume}}
	if err := ValidateTokenPermissionAttenuation(queryCaller, []PermissionPair{query}); err != nil {
		t.Fatalf("query issuance with prerequisite was denied: %v", err)
	}
	if err := ValidateTokenPermissionAttenuation(APIToken{PermissionProfile: PermissionCatalogProfile, Permissions: []PermissionPair{query}}, []PermissionPair{query}); !errors.Is(err, ErrTokenPermissionNotAllowed) {
		t.Fatalf("query issuance without consume error = %v, want %v", err, ErrTokenPermissionNotAllowed)
	}
}

func TestPermissionPairsAgainstAuthorityRequiresIndependentPrerequisites(t *testing.T) {
	query := mustExactPermissionPair(t, ActionSemanticQuery, "project_a", "semantic_a", projectgraph.KindSemanticModel)
	consume := mustExactPermissionPair(t, ActionSemanticConsume, "project_a", "semantic_a", projectgraph.KindSemanticModel)
	if err := ValidatePermissionPairsAgainstAuthority([]PermissionPair{query}, []PermissionPair{query}); !errors.Is(err, ErrTokenPermissionNotAllowed) {
		t.Fatalf("query issuance without durable consume error = %v, want %v", err, ErrTokenPermissionNotAllowed)
	}
	if err := ValidatePermissionPairsAgainstAuthority([]PermissionPair{query, consume}, []PermissionPair{query}); err != nil {
		t.Fatalf("query issuance with durable consume was denied: %v", err)
	}
}

func TestTypedTokenPermissionAttenuationRejectsFutureWideningAndAmbiguousScope(t *testing.T) {
	exact := mustExactPermissionPair(t, ActionDashboardRead, "project_a", "dashboard_a", projectgraph.KindDashboard)
	future, err := NewFutureProjectPermissionPair(ActionDashboardRead, "project_a", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	caller := APIToken{PermissionProfile: PermissionCatalogProfile, Permissions: []PermissionPair{exact}}
	if err := ValidateTokenPermissionAttenuation(caller, []PermissionPair{future}); !errors.Is(err, ErrTokenPermissionNotAllowed) {
		t.Fatalf("exact credential future widening error = %v, want %v", err, ErrTokenPermissionNotAllowed)
	}
	if err := ValidateTokenPermissionAttenuation(APIToken{PermissionProfile: PermissionCatalogProfile, Permissions: []PermissionPair{future}}, []PermissionPair{exact}); err != nil {
		t.Fatalf("future credential exact attenuation was denied: %v", err)
	}

	for _, ambiguous := range []APIToken{
		{Capabilities: LegacyProjectCapabilities()},
		{PermissionProfile: PermissionCatalogProfile},
		{PermissionProfile: "leapview.permissions/v999", Permissions: []PermissionPair{exact}},
	} {
		if err := ValidateTokenPermissionAttenuation(ambiguous, []PermissionPair{exact}); !errors.Is(err, ErrTokenPermissionAttenuationNeeded) {
			t.Fatalf("ambiguous credential error = %v, want %v", err, ErrTokenPermissionAttenuationNeeded)
		}
	}
}

func TestPermissionPairEncodingIsStrictAndRoundTrips(t *testing.T) {
	pair := mustExactPermissionPair(t, ActionDashboardRead, "project_a", "dashboard_a", projectgraph.KindDashboard)
	encoded, err := EncodePermissionPairs([]PermissionPair{pair})
	if err != nil {
		t.Fatalf("EncodePermissionPairs() error = %v", err)
	}
	decoded, err := DecodePermissionPairs(encoded)
	if err != nil || len(decoded) != 1 || permissionPairKey(decoded[0]) != permissionPairKey(pair) {
		t.Fatalf("DecodePermissionPairs() = %#v, %v", decoded, err)
	}
	forged := bytes.Replace(encoded, []byte(`"target":{`), []byte(`"unknown":true,"target":{`), 1)
	if _, err := DecodePermissionPairs(forged); err == nil {
		t.Fatal("unknown permission-pair field was accepted")
	}
	if _, err := DecodePermissionPairs([]byte("null")); !errors.Is(err, ErrTokenPermissionsNeeded) {
		t.Fatalf("null permission pairs error = %v", err)
	}
}

func TestPermissionContractAdapterRoundTripsAndRejectsRelabeling(t *testing.T) {
	pair := mustExactPermissionPair(t, ActionDashboardRead, "project_a", "dashboard_a", projectgraph.KindDashboard)
	contractPair, err := ToContractPermissionPair(pair)
	if err != nil {
		t.Fatalf("ToContractPermissionPair() error = %v", err)
	}
	roundTrip, err := FromContractPermissionPair(contractPair)
	if err != nil || permissionPairKey(roundTrip) != permissionPairKey(pair) {
		t.Fatalf("FromContractPermissionPair() = %#v, %v", roundTrip, err)
	}

	forged := contractPair
	forged.Target.ResourceKind = permissions.Kind(projectgraph.KindPipeline)
	if err := forged.ValidateShape(); err != nil {
		t.Fatalf("generic contract rejected canonical opaque kind: %v", err)
	}
	if _, err := FromContractPermissionPair(forged); !errors.Is(err, ErrInvalidPermissionPair) {
		t.Fatalf("product adapter relabeling error = %v, want ErrInvalidPermissionPair", err)
	}

	unknown := contractPair
	unknown.Action = permissions.Action("vendor.read")
	if err := unknown.ValidateShape(); err != nil {
		t.Fatalf("generic contract rejected canonical opaque action: %v", err)
	}
	if _, err := FromContractPermissionPair(unknown); !errors.Is(err, ErrUnknownPermissionAction) {
		t.Fatalf("product adapter unknown-action error = %v, want ErrUnknownPermissionAction", err)
	}
}

func mustExactPermissionPair(t *testing.T, action Action, projectID, resourceID string, kind projectgraph.Kind) PermissionPair {
	t.Helper()
	resource, err := NewResourceRef(projectgraph.ResourceID(resourceID), kind)
	if err != nil {
		t.Fatalf("NewResourceRef() error = %v", err)
	}
	pair, err := NewExactPermissionPair(action, projectgraph.ResourceID(projectID), resource)
	if err != nil {
		t.Fatalf("NewExactPermissionPair() error = %v", err)
	}
	return pair
}
