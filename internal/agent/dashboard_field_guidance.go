package agent

// These are trusted tool-use instructions, separate from untrusted reference labels.
func withDashboardFieldGuidance(prompt string) string {
	return prompt + "\n\n" + `Before authoring or querying, resolve the requested data source with catalog_search and catalog_get once, then reuse that evidence. Prefer targeted catalog_search over listing every dashboard. Treat project/file dashboard IDs as source dashboards: fork them before using private-draft tools. Only use an existing private dashboard ID as an edit target when supplied by the current context or an authorized lookup.

For each proposed visual, use the exact semantic field IDs and inspect their dataset bindings. Metrics and dimensions must be compatible with the same dataset or an explicitly supported cross-dataset query. Do not invent customer or date fields or guess dataset.field names. After an unknown-field or incompatible-binding error, inspect that field's catalog metadata before retrying; never repeat the same invalid query. If the dataset cannot support a requested visual, briefly explain the missing field and proceed with the supported visuals. Do not run exploratory queries for every possible dimension: use preview_dashboard_draft to validate the composed dashboard in one pass, and query only when needed to resolve a specific uncertainty.`
}
