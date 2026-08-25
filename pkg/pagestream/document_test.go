package pagestream

import (
	"bytes"
	"strings"
	"testing"

	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

func TestRenderPageIncludesLiteralUpdatesInitMainAttrsAndBody(t *testing.T) {
	var body bytes.Buffer
	err := RenderPage(PageSpec{
		Title:             "Test Page",
		HTMLAttrs:         []g.Node{g.Attr("data-color-mode", "auto")},
		Head:              []g.Node{h.Meta(g.Attr("name", "test-head")), h.Script(h.Type("module"), h.Src("/static/route.js?v=dev"))},
		MainAttrs:         []g.Node{h.ID("root"), h.Class("app-shell")},
		DatastarScriptURL: "/assets/datastar.js?v=test",
		UpdatesURL:        "/updates?route=test",
		Body:              []g.Node{h.Div(h.ID("content"), g.Text("Hello"))},
	}).Render(&body)
	if err != nil {
		t.Fatalf("render document: %v", err)
	}
	html := body.String()
	if strings.Contains(html, "cdn.jsdelivr") {
		t.Fatalf("rendered document referenced CDN Datastar asset:\n%s", html)
	}
	for _, want := range []string{
		"<title>Test Page</title>",
		`data-color-mode="auto"`,
		`<main id="root" class="app-shell"`,
		`/updates?route=test`,
		`/assets/datastar.js?v=test`,
		`data-init="@get(&#39;/updates?route=test&#39;, {openWhenHidden: true})"`,
		`<div id="content">Hello</div>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered document missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "data-signals=") || strings.Contains(html, "updatesUrl") {
		t.Fatalf("rendered document included initial signals:\n%s", html)
	}
	if strings.Index(html, "/assets/datastar.js?v=test") > strings.Index(html, "/static/route.js?v=dev") {
		t.Fatalf("rendered document loaded Datastar after route modules:\n%s", html)
	}
}

func TestRenderPageRequiresUpdatesURL(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("RenderPage did not panic for missing UpdatesURL")
		}
	}()
	_ = RenderPage(PageSpec{})
}

func TestRenderPageAcceptsValidatedSameOriginAbsoluteUpdatesPath(t *testing.T) {
	for _, updatesURL := range []string{"/updates?route=test", "/public/dashboards/opaque/updates?page=overview"} {
		t.Run(updatesURL, func(t *testing.T) {
			_ = RenderPage(PageSpec{DatastarScriptURL: "/datastar.js", UpdatesURL: updatesURL})
		})
	}
}

func TestRenderPageRejectsNonSameOriginOrNonAbsoluteUpdatesPath(t *testing.T) {
	for _, updatesURL := range []string{"updates", "//example.com/updates", "https://example.com/updates", "/updates#fragment"} {
		t.Run(updatesURL, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("RenderPage did not panic for %q", updatesURL)
				}
			}()
			_ = RenderPage(PageSpec{DatastarScriptURL: "/datastar.js", UpdatesURL: updatesURL})
		})
	}
}

func TestRenderPageRequiresDatastarScriptURL(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("RenderPage did not panic for missing DatastarScriptURL")
		}
	}()
	_ = RenderPage(PageSpec{UpdatesURL: "/updates?route=test"})
}

func TestRenderPageEscapesUpdatesURLInInitExpression(t *testing.T) {
	var body bytes.Buffer
	err := RenderPage(PageSpec{
		Title:             "Escaped URL",
		DatastarScriptURL: "/datastar.js",
		UpdatesURL:        "/updates?route=test&q=%22quoted%22",
	}).Render(&body)
	if err != nil {
		t.Fatalf("render page: %v", err)
	}
	html := body.String()
	if !strings.Contains(html, `@get(&#39;/updates?route=test&amp;q=%22quoted%22&#39;`) {
		t.Fatalf("rendered page did not escape literal updates URL:\n%s", html)
	}
}
