package http

import (
	"github.com/flidai/leapview/internal/app/site/visualdocs"
	"github.com/flidai/leapview/pkg/pagestream"
)

type visualShowcaseDocument struct {
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	VisualID string `json:"visualID"`
}

func visualShowcasePatch() pagestream.SignalPatch {
	visuals := make([]visualdocs.Payload, 0, len(visualDocuments))
	documents := make([]visualShowcaseDocument, 0, len(visualDocuments))
	for _, document := range visualDocuments {
		examples := visualDocumentation.Documents[document.slug]
		if len(examples) == 0 {
			panic("visual showcase requires a compiled example for " + document.slug)
		}
		first := examples[0]
		visuals = append(visuals, first)
		documents = append(documents, visualShowcaseDocument{
			Slug:     document.slug,
			Title:    document.title,
			VisualID: first.VisualID,
		})
	}
	return pagestream.SignalPatch{"visuals": visuals, "visualDocuments": documents}
}
