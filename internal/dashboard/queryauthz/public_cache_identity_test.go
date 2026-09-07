package authz

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
)

func TestPublicCacheAudiencePolicyIdentity(t *testing.T) {
	public := dataquery.Query{
		ProjectID: canonicalProject, Surface: dataquery.SurfacePublicDashboard,
		PrincipalID: dashboardPublicationSubjectID(canonicalProject, "public-sales"),
	}
	fingerprint := func(q dataquery.Query) string {
		return effectivePolicyFingerprint(q, access.CapabilityResourceRead, nil, nil, effectivePolicyContext{})
	}
	warm := fingerprint(public)
	foreground := public
	foreground.RequestID = "different-request"
	if warm != fingerprint(foreground) {
		t.Fatal("public foreground and warm audience policy identities differ")
	}
	private := public
	private.Surface = dataquery.SurfaceDashboard
	private.PrincipalID = "authenticated-viewer"
	if warm == fingerprint(private) {
		t.Fatal("public publication and private principal policy identities collided")
	}
}
