package http

import (
	"encoding/json"
	"fmt"
	"strings"

	content "github.com/flidai/leapview/docs"
	"github.com/flidai/leapview/internal/app/site/visualdocs"
	"github.com/flidai/leapview/pkg/pagestream"
)

type visualDocumentationArtifact = visualdocs.Artifact

var visualDocumentation = loadVisualDocumentation()

func loadVisualDocumentation() visualDocumentationArtifact {
	contents, err := content.Files.ReadFile("visuals/examples.gen.json")
	if err != nil {
		panic(fmt.Sprintf("read generated visual documentation: %v", err))
	}
	var artifact visualDocumentationArtifact
	if err := json.Unmarshal(contents, &artifact); err != nil {
		panic(fmt.Sprintf("decode generated visual documentation: %v", err))
	}
	if artifact.Version != visualdocs.ArtifactVersion {
		panic(fmt.Sprintf("generated visual documentation version = %d, want %d", artifact.Version, visualdocs.ArtifactVersion))
	}
	if len(artifact.Documents) == 0 || len(artifact.References) == 0 || len(artifact.Showcase) == 0 {
		panic("generated visual documentation is empty")
	}
	seen := map[string]string{}
	for slug, examples := range artifact.Documents {
		if len(examples) == 0 {
			panic(fmt.Sprintf("generated visual documentation %q has no examples", slug))
		}
		for _, example := range examples {
			if example.VisualID == "" {
				panic(fmt.Sprintf("generated visual documentation %q has an example without an id", slug))
			}
			if previous := seen[example.VisualID]; previous != "" {
				panic(fmt.Sprintf("generated visual example %q belongs to both %q and %q", example.VisualID, previous, slug))
			}
			seen[example.VisualID] = slug
			if len(artifact.References[slug].Examples[example.VisualID].KeyFields) == 0 {
				panic(fmt.Sprintf("generated visual example %q has no key fields", example.VisualID))
			}
		}
		reference, ok := artifact.References[slug]
		if !ok || reference.Kind == "" || reference.Renderer == "" || len(reference.Shapes) == 0 || len(reference.QueryFields) == 0 || len(reference.Fields) == 0 || reference.Accessibility == "" {
			panic(fmt.Sprintf("generated visual documentation %q has an incomplete API reference", slug))
		}
	}
	for slug := range artifact.References {
		if _, ok := artifact.Documents[slug]; !ok {
			panic(fmt.Sprintf("generated visual documentation contains a reference for unknown slug %q", slug))
		}
	}
	return artifact
}

func visualReferenceForDocument(slug string) (visualdocs.DocumentReference, bool) {
	reference, ok := visualDocumentation.References[slug]
	return reference, ok
}

func visualExampleReferenceForDocument(slug, id string) (visualdocs.ExampleReference, bool) {
	reference, ok := visualDocumentation.References[slug]
	if !ok {
		return visualdocs.ExampleReference{}, false
	}
	example, ok := reference.Examples[id]
	return example, ok
}

func visualExamplesForDocument(slug string) ([]visualdocs.Payload, bool) {
	examples, ok := visualDocumentation.Documents[slug]
	return examples, ok
}

func responsiveWidgetReferencePatch() pagestream.SignalPatch {
	examples, ok := visualDocumentation.Documents["visuals/kpi"]
	if !ok || len(examples) == 0 {
		panic("responsive widget reference requires compiled KPI examples")
	}
	return pagestream.SignalPatch{"visuals": examples}
}

func documentHasVisualExample(slug, id string) bool {
	for _, example := range visualDocumentation.Documents[slug] {
		if example.VisualID == id {
			return true
		}
	}
	return false
}

func visualExampleForDocument(slug, id string) (visualdocs.Payload, bool) {
	for _, example := range visualDocumentation.Documents[slug] {
		if example.VisualID == id {
			return example, true
		}
	}
	return visualdocs.Payload{}, false
}

func validateVisualDocumentationCatalog() bool {
	documented := make(map[string]struct{}, len(visualDocuments))
	for _, document := range visualDocuments {
		documented[document.slug] = struct{}{}
		examples, ok := visualDocumentation.Documents[document.slug]
		if !ok {
			panic(fmt.Sprintf("visual documentation %q has no generated examples", document.slug))
		}
		shortcodes := docsVisualShortcode.FindAllStringSubmatch(document.markdown, -1)
		if len(shortcodes) != len(examples) {
			panic(fmt.Sprintf("visual documentation %q has %d shortcodes and %d generated examples", document.slug, len(shortcodes), len(examples)))
		}
		for index, shortcode := range shortcodes {
			if shortcode[1] != examples[index].VisualID {
				panic(fmt.Sprintf("visual documentation %q shortcode %d = %q, generated id = %q", document.slug, index, shortcode[1], examples[index].VisualID))
			}
		}
		if strings.Contains(docsVisualShortcode.ReplaceAllString(document.markdown, ""), "{{< visual") {
			panic(fmt.Sprintf("visual documentation %q has an invalid visual shortcode", document.slug))
		}
	}
	for slug := range visualDocumentation.Documents {
		if _, ok := documented[slug]; !ok {
			panic(fmt.Sprintf("generated visual documentation contains unknown slug %q", slug))
		}
	}
	return true
}
