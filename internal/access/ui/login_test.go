package ui

import (
	"html"
	"strings"
	"testing"
)

func TestLoginPageUsesProductBranding(t *testing.T) {
	var output strings.Builder
	if err := LoginPage().Render(&output); err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(output.String())
	for _, expected := range []string{
		"<title>LeapView Login</title>",
		`<link rel="icon" href="/static/favicon.svg?v=dev" type="image/svg+xml">`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("document does not contain %q", expected)
		}
	}
}

func TestLoginBootstrapUsesProductName(t *testing.T) {
	page := LoginBootstrapSignalsForOptions(LoginPageOptions{})["page"].(LoginPageSignal)
	if page.Title != "LeapView" || page.Kind != "login" {
		t.Fatalf("login page signal = %#v", page)
	}
}

func TestLoginPageCarriesOnlyClosedErrorCodesIntoUpdates(t *testing.T) {
	for _, test := range []struct {
		name      string
		errorCode string
		want      string
	}{
		{name: "invalid credentials", errorCode: "invalid_credentials", want: "error=invalid_credentials"},
		{name: "unknown", errorCode: "<script>alert(1)</script>"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			if err := LoginPage(LoginPageOptions{ErrorCode: test.errorCode}).Render(&output); err != nil {
				t.Fatal(err)
			}
			rendered := html.UnescapeString(output.String())
			if test.want != "" && !strings.Contains(rendered, test.want) {
				t.Fatalf("document does not contain %q", test.want)
			}
			if strings.Contains(rendered, "script%3Ealert") || strings.Contains(rendered, "<script>alert") {
				t.Fatalf("document propagated an unknown login error: %s", rendered)
			}
		})
	}
}
