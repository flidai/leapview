package pagestream

import (
	"fmt"
	"net/url"
	"strings"

	g "maragu.dev/gomponents"
	dsattr "maragu.dev/gomponents-datastar"
	c "maragu.dev/gomponents/components"
	h "maragu.dev/gomponents/html"
)

type PageSpec struct {
	Title             string
	Language          string
	HTMLAttrs         []g.Node
	Head              []g.Node
	MainAttrs         []g.Node
	DatastarScriptURL string
	UpdatesURL        string
	Body              []g.Node
}

func RenderPage(spec PageSpec) g.Node {
	updatesURL := validateUpdatesURL(spec.UpdatesURL)
	datastarScriptURL := validateDatastarScriptURL(spec.DatastarScriptURL)
	language := spec.Language
	if language == "" {
		language = "en"
	}
	head := append([]g.Node{datastarScript(datastarScriptURL)}, spec.Head...)
	main := append([]g.Node{}, spec.MainAttrs...)
	// Keeping the canonical update stream open in background tabs is an
	// intentional framework invariant for server-owned page state.
	main = append(main, dsattr.Init(openUpdatesAction(updatesURL)))
	main = append(main, spec.Body...)
	return c.HTML5(c.HTML5Props{
		Title:     spec.Title,
		Language:  language,
		HTMLAttrs: spec.HTMLAttrs,
		Head:      head,
		Body:      []g.Node{h.Main(main...)},
	})
}

func openUpdatesAction(updatesURL string) string {
	return "@get('" + jsSingleQuoted(updatesURL) + "', {openWhenHidden: true})"
}

func jsSingleQuoted(value string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`).Replace(value)
}

func datastarScript(scriptURL string) g.Node {
	return h.Script(h.Type("module"), h.Src(scriptURL))
}

func validateUpdatesURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		panic("pagestream: UpdatesURL is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || parsed.Fragment != "" {
		panic(fmt.Sprintf("pagestream: UpdatesURL must be a same-origin absolute path, got %q", raw))
	}
	return trimmed
}

func validateDatastarScriptURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		panic("pagestream: DatastarScriptURL is required")
	}
	return trimmed
}
