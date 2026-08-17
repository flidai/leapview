// Command apigenpatch applies small transport policies not expressible in the
// TypeSpec surface. It is intentionally deterministic and runs after APIGen.
package main

import (
	"fmt"
	"go/format"
	"os"
	"strings"
)

func main() {
	patches := []struct {
		path  string
		patch func([]byte) ([]byte, error)
	}{
		{path: "internal/project/api/gen/client.apigen.gen.go", patch: applySearchPolicy},
		{path: "internal/access/api/gen/server.apigen.gen.go", patch: applyAccessResourceKindPolicy},
	}
	for _, entry := range patches {
		source, err := os.ReadFile(entry.path)
		if err != nil {
			panic(err)
		}
		formatted, err := entry.patch(source)
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(entry.path, formatted, 0o644); err != nil {
			panic(err)
		}
	}
}

func applySearchPolicy(source []byte) ([]byte, error) {
	text := string(source)
	const searchStart = "func (client *GenClient) Search(ctx context.Context, request GenSearchClientRequest) (GenSearchClientResponse, error) {"
	if !strings.Contains(text, "\t\"fmt\"\n") {
		const importAnchor = "\t\"context\"\n"
		if strings.Count(text, importAnchor) != 1 {
			return nil, fmt.Errorf("generated client import anchor changed")
		}
		text = strings.Replace(text, importAnchor, importAnchor+"\t\"fmt\"\n", 1)
	}
	if strings.Count(text, searchStart) != 1 {
		return nil, fmt.Errorf("generated Search function anchor changed")
	}
	start := strings.Index(text, searchStart)
	relativeEnd := strings.Index(text[start:], "\n}\n")
	if relativeEnd < 0 {
		return nil, fmt.Errorf("generated Search function end anchor changed")
	}
	end := start + relativeEnd + len("\n}")
	search := text[start:end]
	const functionAnchor = searchStart + "\n\tvar body GenSchemaSearchResponse"
	const guardedFunction = searchStart + "\n\tif request.Params.Limit != nil && (*request.Params.Limit < 1 || *request.Params.Limit > 200) {\n\t\treturn GenSearchClientResponse{}, fmt.Errorf(\"limit must be between 1 and 200\")\n\t}\n\tvar body GenSchemaSearchResponse"
	if !strings.Contains(search, guardedFunction) {
		if strings.Count(search, functionAnchor) != 1 {
			return nil, fmt.Errorf("generated Search function anchor changed")
		}
		search = strings.Replace(search, functionAnchor, guardedFunction, 1)
	}
	var err error
	search, err = replaceGeneratedPolicy(search,
		"AddQuery(query, \"kind\", request.Params.Kind, false)",
		"AddQuery(query, \"kind\", request.Params.Kind, true)")
	if err != nil {
		return nil, err
	}
	text = text[:start] + search + text[end:]
	formatted, err := format.Source([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("format generated client: %w", err)
	}
	return formatted, nil
}

func replaceGeneratedPolicy(source, old, desired string) (string, error) {
	if strings.Count(source, desired) == 1 {
		return source, nil
	}
	if strings.Count(source, old) != 1 {
		return "", fmt.Errorf("generated client policy anchor changed: %q", old)
	}
	return strings.Replace(source, old, desired, 1), nil
}

// applyAccessResourceKindPolicy qualifies ResourceKind in APIGen's generated
// request parameter structs. The schema alias is generated in request_models,
// while the server generator currently emits the unqualified identifier.
func applyAccessResourceKindPolicy(source []byte) ([]byte, error) {
	text := string(source)
	const old = "*ResourceKind"
	const desired = "*GenSchemaResourceKind"
	oldCount := strings.Count(text, old)
	desiredCount := strings.Count(text, desired)
	switch {
	case oldCount == 0 && desiredCount == 3:
		// Already patched; keep the operation idempotent.
	case oldCount == 3 && desiredCount == 0:
		text = strings.ReplaceAll(text, old, desired)
	default:
		return nil, fmt.Errorf("generated access ResourceKind policy anchor changed: old=%d desired=%d", oldCount, desiredCount)
	}
	formatted, err := format.Source([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("format generated access server: %w", err)
	}
	return formatted, nil
}
