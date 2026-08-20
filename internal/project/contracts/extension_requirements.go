package contracts

import "sort"

// RequiredExtensionNames is the closed runtime/package supply set derived
// from generated connector and path-format profiles. Spatial is the sole
// runtime-only extension used by visualization preparation; DuckDB's Iceberg
// extension also has an official Avro dependency, which must be supplied
// explicitly so offline loading never falls back to implicit installation.
func RequiredExtensionNames() []string {
	seen := map[string]struct{}{"avro": {}, "spatial": {}}
	for _, profile := range ConnectorRegistry {
		for _, name := range profile.ApprovedExtensions {
			if name != "" {
				seen[name] = struct{}{}
			}
		}
	}
	for _, profile := range FormatRegistry {
		if name := profile.RequiredExtension; name != "" {
			seen[name] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
