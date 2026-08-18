// Package document contains the generated canonical Dashboard resource DTOs.
//
// LEA-426 is responsible for wiring configschema.DecodeResource, project
// authoring, builder, agent, revision storage, and export to DashboardDocument.
// LEA-424/430 must enforce visual/query/presentation compatibility when they
// consume these DTOs; the generated contract never drops incompatible fields.
// This package intentionally contains no YAML union unmarshallers, aliases,
// translators, or runtime/compiler resolution fields.
package document

import (
	"encoding/json"
	"fmt"
)

// Clone returns a lossless deep copy of the generated canonical DTO. JSON is
// the generated contract's own tagged-union representation, so this copy path
// cannot reintroduce the legacy authoring model or silently drop variants.
func (value DashboardDocument) Clone() (DashboardDocument, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return DashboardDocument{}, fmt.Errorf("clone dashboard document: %w", err)
	}
	var clone DashboardDocument
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return DashboardDocument{}, fmt.Errorf("decode cloned dashboard document: %w", err)
	}
	return clone, nil
}
