package ui

import (
	lucide "github.com/eduardolat/gomponents-lucide"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

type assetIconFactory func(children ...g.Node) g.Node

type assetPresentation struct {
	Icon        assetIconFactory
	Background  string
	Accent      string
	BorderColor string
}

var assetPresentationByType = map[string]assetPresentation{
	"catalog":        assetPresentationFor(lucide.BookOpen, "catalog"),
	"connection":     assetPresentationFor(lucide.Plug, "connection"),
	"dashboard":      assetPresentationFor(lucide.LayoutDashboard, "dashboard"),
	"field":          assetPresentationFor(lucide.Ruler, "dimension"),
	"filter":         assetPresentationFor(lucide.ListFilter, "filter"),
	"measure":        assetPresentationFor(lucide.Sigma, "measure"),
	"model_table":    assetPresentationFor(lucide.TableProperties, "model-table"),
	"page":           assetPresentationFor(lucide.PanelTop, "page"),
	"page_item":      assetPresentationFor(lucide.Component, "page"),
	"relationship":   assetPresentationFor(lucide.Workflow, "dimension"),
	"semantic_model": assetPresentationFor(lucide.Waypoints, "semantic-model"),
	"semantic_table": assetPresentationFor(lucide.TableProperties, "model-table"),
	"source":         assetPresentationFor(lucide.Cable, "source"),
	"table":          assetPresentationFor(lucide.Table2, "table"),
	"visual":         assetPresentationFor(lucide.ChartColumn, "visual"),
	"visual_element": assetPresentationFor(lucide.SquareDashedMousePointer, "visual"),
	"project":        assetPresentationFor(lucide.Boxes, "catalog"),
}

func assetTypeIcon(typ string) g.Node {
	presentation := assetPresentationForType(typ)
	return h.Span(
		h.Class("inline-flex size-8 shrink-0 items-center justify-center rounded-small border"),
		h.Style(assetPresentationStyle(presentation)),
		g.Attr("aria-hidden", "true"),
		presentation.Icon(assetIconAttrs()...),
	)
}

func assetTypeInlineIcon(typ string) g.Node {
	presentation := assetPresentationForType(typ)
	return h.Span(
		h.Class("inline-flex size-5 shrink-0 items-center justify-center rounded-small border"),
		h.Style(assetPresentationStyle(presentation)),
		g.Attr("aria-hidden", "true"),
		presentation.Icon(assetInlineIconAttrs()...),
	)
}

func assetPresentationFor(icon assetIconFactory, tokenName string) assetPresentation {
	return assetPresentation{
		Icon:        icon,
		Background:  "--lv-asset-" + tokenName + "-bg",
		Accent:      "--lv-asset-" + tokenName + "-accent",
		BorderColor: "--lv-asset-" + tokenName + "-border",
	}
}

func assetPresentationForType(typ string) assetPresentation {
	presentation, ok := assetPresentationByType[typ]
	if ok {
		return presentation
	}
	return assetPresentation{
		Icon:        lucide.Component,
		Background:  "--lv-bg-panel-muted",
		Accent:      "--lv-fg-muted",
		BorderColor: "--lv-line-muted",
	}
}

func assetPresentationStyle(presentation assetPresentation) string {
	return "background-color: var(" + presentation.Background + "); border-color: var(" + presentation.BorderColor + "); color: var(" + presentation.Accent + ")"
}

func assetIconAttrs() []g.Node {
	return []g.Node{h.Class("size-4 shrink-0"), h.Style("stroke-width: 1.75")}
}

func assetInlineIconAttrs() []g.Node {
	return []g.Node{h.Class("size-3.5 shrink-0"), h.Style("stroke-width: 1.75")}
}
