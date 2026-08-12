package sqlite

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestPrincipalThemePreferencePersistsAndAudits(t *testing.T) {
	_, repository := openAccessRepo(t, t.Context())
	principal, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "principal-theme", Kind: access.PrincipalKindUser, Email: "theme@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SetPrincipalThemeAudited(t.Context(), principal.ID, access.ThemeDarkDimmed); err != nil {
		t.Fatal(err)
	}
	preferences, err := repository.PrincipalPreferences(t.Context(), principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preferences.Theme != access.ThemeDarkDimmed {
		t.Fatalf("theme = %q, want dark_dimmed", preferences.Theme)
	}
	events, err := repository.ListAuditEvents(t.Context(), access.AuditEventFilter{PrincipalID: principal.ID, Action: "principal.theme.updated"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].TargetID != principal.ID {
		t.Fatalf("audit events = %#v", events)
	}
}
