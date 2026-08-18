package query

import (
	"encoding/json"
	"sort"

	"github.com/flidai/leapview/internal/analytics/masking"
)

// columnMaskFingerprint compiles authored masks into the closed masking.Kind
// set before canonicalizing. Unknown operations therefore fail closed rather
// than becoming an accidental Go-struct equality contract.
func columnMaskFingerprint(masks []ColumnMask) (string, error) {
	type entry struct {
		Field string       `json:"field"`
		Kind  masking.Kind `json:"kind"`
	}
	entries := make([]entry, 0, len(masks))
	for _, mask := range masks {
		kind, err := masking.Compile(mask.Mask)
		if err != nil {
			return "", err
		}
		entries = append(entries, entry{Field: mask.Field, Kind: kind})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Field != entries[j].Field {
			return entries[i].Field < entries[j].Field
		}
		return entries[i].Kind < entries[j].Kind
	})
	data, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
