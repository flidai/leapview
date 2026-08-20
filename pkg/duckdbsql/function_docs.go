package duckdbsql

// FunctionDocumentation is source-authored, user-facing documentation for a
// built-in DuckDB function. It deliberately remains separate from
// FunctionMetadata: documentation does not describe every runtime overload.
type FunctionDocumentation struct {
	Name        string
	Parameters  string
	Description string
	Example     string
	Examples    []string
	Aliases     []string
	Categories  []string
	Category    string
	// FunctionType preserves the exact upstream JSON type, such as
	// scalar_function_set. Kind is the broader scalar or aggregate family
	// derived from the source directory.
	FunctionType string
	Kind         string
	Variants     []FunctionDocumentationVariant
	SourcePath   string
}

// FunctionDocumentationSourceFile identifies one immutable source input.
type FunctionDocumentationSourceFile struct {
	Path   string
	SHA256 string
}

// GeneratedFunctionDocumentationSource returns provenance for the generated
// documentation overlay.
func GeneratedFunctionDocumentationSource() (string, string, []FunctionDocumentationSourceFile) {
	files := append([]FunctionDocumentationSourceFile(nil), generatedFunctionDocumentationSourceFiles...)
	return generatedFunctionDocumentationDuckDBVersion, generatedFunctionDocumentationDuckDBSourceCommit, files
}

// FunctionDocumentationVariant preserves a structured source variant without
// carrying C++ implementation-only fields such as struct or extra_functions.
type FunctionDocumentationVariant struct {
	Parameters  []FunctionDocumentationParameter
	Description string
	Example     string
	Examples    []string
	Categories  []string
}

// FunctionDocumentationParameter is the SQL-facing name of one variant
// parameter.
type FunctionDocumentationParameter struct {
	Name string
	Type string
}

// GeneratedFunctionDocumentationSnapshot returns a deep copy of the pinned
// source documentation inventory.
func GeneratedFunctionDocumentationSnapshot() []FunctionDocumentation {
	out := make([]FunctionDocumentation, len(generatedFunctionDocumentation))
	for i, doc := range generatedFunctionDocumentation {
		out[i] = doc
		out[i].Examples = append([]string(nil), doc.Examples...)
		out[i].Aliases = append([]string(nil), doc.Aliases...)
		out[i].Categories = append([]string(nil), doc.Categories...)
		out[i].Variants = append([]FunctionDocumentationVariant(nil), doc.Variants...)
		for j := range out[i].Variants {
			out[i].Variants[j].Parameters = append([]FunctionDocumentationParameter(nil), doc.Variants[j].Parameters...)
			out[i].Variants[j].Examples = append([]string(nil), doc.Variants[j].Examples...)
			out[i].Variants[j].Categories = append([]string(nil), doc.Variants[j].Categories...)
		}
	}
	return out
}
