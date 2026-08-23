package page

import (
	"bytes"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/web/staticasset"
	g "maragu.dev/gomponents"
)

func TestRenderAppliesInjectedLayoutWithoutOwningItsSignal(t *testing.T) {
	layout := Layout{
		Presentation: Presentation{ProductName: "Product", FaviconPath: "/brand.svg"},
		Assets:       staticasset.New(staticasset.Config{Version: "release-123"}),
		Signal: struct {
			Name string `json:"name"`
		}{Name: "chrome"},
		Scripts: []string{"/shell.js"},
		Mount: func(content g.Node, attrs ...g.Node) g.Node {
			return g.El("product-shell", append(attrs, content)...)
		},
		ColorMode: "light_colorblind",
	}
	var output bytes.Buffer
	if err := Render(layout, Spec{
		UpdatesURL: "/updates", Scripts: []string{"/route.js"},
		Content: g.El("route-page"), ContentAttrs: []g.Node{g.Attr("slot", "page")},
	}).Render(&output); err != nil {
		t.Fatal(err)
	}
	body := output.String()
	for _, want := range []string{"<title>Product</title>", `data-color-mode="light"`, `data-light-theme="light_colorblind"`, `data-theme-preference="light_colorblind"`, `href="/brand.svg?v=release-123"`, `src="/shell.js?v=release-123"`, `src="/route.js?v=release-123"`, `<product-shell slot="page"><route-page>`} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered document missing %q", want)
		}
	}
	signals := WithSignal(layout, map[string]any{"page": "route"})
	if signals["chrome"] != layout.Signal {
		t.Fatal("layout signal was not merged")
	}
}

func TestDocumentTitleIncludesCustomProductIdentity(t *testing.T) {
	if got := documentTitle("Admin - General", "Northstar Analytics"); got != "Admin - General · Northstar Analytics" {
		t.Fatalf("document title = %q", got)
	}
	if got := documentTitle("LeapView Dashboards", "Northstar Analytics"); got != "Northstar Analytics Dashboards" {
		t.Fatalf("branded document title = %q", got)
	}
}

func TestRenderIncludesInlineInitialStreamRecovery(t *testing.T) {
	layout := Layout{Assets: staticasset.New(staticasset.Config{Version: "test"})}
	var output bytes.Buffer
	if err := Render(layout, Spec{UpdatesURL: "/updates?route=data", Content: g.El("route-page")}).Render(&output); err != nil {
		t.Fatal(err)
	}
	body := output.String()
	for _, want := range []string{
		`data-page-stream-recovery-root`,
		`data-on:datastar-fetch="evt.detail.el === el &amp;&amp; ($pageStreamRecovery =`,
		`evt.detail.type === &#39;started&#39; ? false`,
		`evt.detail.type === &#39;retrying&#39;`,
		`evt.detail.type === &#39;datastar-patch-signals&#39;`,
		`data-page-stream-recovery`,
		`data-show="$pageStreamRecovery"`,
		`style="display:none"`,
		"Unable to load this page",
		">Retry</button>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered page missing %q:\n%s", want, body)
		}
	}
}
