package module

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/publication"
)

func TestPublicationExecutionContextUsesPublicationPrincipal(t *testing.T) {
	row := publication.Publication{
		ProjectID: "project_1",
		Name:      "website-showcase",
		Dashboard: "visual-showcase",
	}

	metadata := dataquery.MetadataFromContext(PublicationExecutionContext(context.Background(), row, ""))
	want := "dashboard_publication:project_1.website-showcase"
	if metadata.PrincipalID != want {
		t.Fatalf("public principal id = %q, want %q", metadata.PrincipalID, want)
	}
	if metadata.Surface != dataquery.SurfacePublicDashboard || metadata.ObjectType != "dashboard_publication" || metadata.ObjectID != "website-showcase" {
		t.Fatalf("public metadata = %#v", metadata)
	}
}

func TestEmbedWithNoAllowedOriginsDeniesFraming(t *testing.T) {
	header := http.Header{}
	SetPublicDashboardSecurityHeaders(header, "embed", nil)
	if got := header.Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options = %q, want omitted", got)
	}
	if got := header.Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("Content-Security-Policy = %q", got)
	}
}
