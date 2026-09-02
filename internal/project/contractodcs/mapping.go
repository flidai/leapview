package contractodcs

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

//go:embed mapping.json
var mappingSpecification []byte

var (
	mappingOnce sync.Once
	mappingByID map[string]MappingEntry
	mappingErr  error
)

func MappingSpecification() []byte { return append([]byte(nil), mappingSpecification...) }

func mappingEntries(selected map[string]struct{}) ([]MappingEntry, error) {
	mappingOnce.Do(func() {
		var manifest MappingReport
		if err := json.Unmarshal(mappingSpecification, &manifest); err != nil {
			mappingErr = fmt.Errorf("decode ODCS mapping specification: %w", err)
			return
		}
		if manifest.Profile != MappingProfile || len(manifest.Entries) == 0 {
			mappingErr = fmt.Errorf("invalid ODCS mapping specification profile")
			return
		}
		mappingByID = make(map[string]MappingEntry, len(manifest.Entries))
		for _, entry := range manifest.Entries {
			if entry.ID == "" || entry.SourceField == "" || entry.ODCSTargetField == "" || entry.Transformation == "" || entry.Limitations == "" || entry.CompatibilityImpact == "" {
				mappingErr = fmt.Errorf("incomplete ODCS mapping specification entry %q", entry.ID)
				return
			}
			if _, exists := mappingByID[entry.ID]; exists {
				mappingErr = fmt.Errorf("duplicate ODCS mapping specification entry %q", entry.ID)
				return
			}
			mappingByID[entry.ID] = entry
		}
	})
	if mappingErr != nil {
		return nil, mappingErr
	}
	result := make([]MappingEntry, 0, len(selected))
	for id := range selected {
		entry, exists := mappingByID[id]
		if !exists {
			return nil, fmt.Errorf("ODCS export used undocumented mapping %q", id)
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
