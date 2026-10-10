package ir

import (
	"fmt"
	"strings"
)

func validateInlineSemantics(spec VisualizationSpec, state InlineVisualizationDataState) error {
	if point, ok := spec.Value.(*PointVisualizationSpec); ok {
		return validatePointRows(*point, state)
	}
	hierarchy, ok := spec.Value.(*HierarchyVisualizationSpec)
	if !ok {
		return nil
	}
	if hierarchy.Mark == VisualizationHierarchyMarkGraph || hierarchy.Mark == VisualizationHierarchyMarkSankey {
		return validateNetworkRows(*hierarchy, state)
	}
	return validateHierarchyRows(*hierarchy, state)
}

func validatePointRows(spec PointVisualizationSpec, state InlineVisualizationDataState) error {
	if len(spec.Identity) == 0 {
		return fmt.Errorf("point visualization requires identity fields")
	}
	dataset, ok := inlineDataset(state, spec.X.Dataset)
	if !ok {
		return fmt.Errorf("point dataset %q is missing", spec.X.Dataset)
	}
	xIndex, yIndex := columnIndex(dataset.Columns, spec.X.Field), columnIndex(dataset.Columns, spec.Y.Field)
	if xIndex < 0 || yIndex < 0 {
		return fmt.Errorf("point x or y column is missing")
	}
	identityIndexes := make([]int, len(spec.Identity))
	for index, identity := range spec.Identity {
		if identity.Dataset != dataset.ID {
			return fmt.Errorf("point identity fields must share the x/y dataset")
		}
		identityIndexes[index] = columnIndex(dataset.Columns, identity.Field)
		if identityIndexes[index] < 0 {
			return fmt.Errorf("point identity column %q is missing", identity.Field)
		}
	}
	seen := make(map[string]struct{}, len(dataset.Rows))
	for rowIndex, row := range dataset.Rows {
		if row[xIndex] == nil || row[yIndex] == nil {
			return fmt.Errorf("point row %d has a null x or y value", rowIndex)
		}
		parts := make([]string, len(identityIndexes))
		for index, column := range identityIndexes {
			if row[column] == nil {
				return fmt.Errorf("point row %d has a null identity value", rowIndex)
			}
			parts[index] = fmt.Sprintf("%T:%v", row[column], row[column])
		}
		key := strings.Join(parts, "\x1f")
		if _, exists := seen[key]; exists {
			return fmt.Errorf("point row %d has duplicate identity", rowIndex)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateHierarchyRows(spec HierarchyVisualizationSpec, state InlineVisualizationDataState) error {
	if spec.Parent == nil || spec.Parent.Dataset != spec.Node.Dataset {
		return fmt.Errorf("hierarchy node and parent fields must share a dataset")
	}
	dataset, ok := inlineDataset(state, spec.Node.Dataset)
	if !ok {
		return fmt.Errorf("hierarchy dataset %q is missing", spec.Node.Dataset)
	}
	nodeIndex, parentIndex := columnIndex(dataset.Columns, spec.Node.Field), columnIndex(dataset.Columns, spec.Parent.Field)
	if nodeIndex < 0 || parentIndex < 0 {
		return fmt.Errorf("hierarchy node or parent column is missing")
	}
	parents := make(map[string]string, len(dataset.Rows))
	for rowIndex, row := range dataset.Rows {
		node, ok := row[nodeIndex].(string)
		if !ok || strings.TrimSpace(node) == "" {
			return fmt.Errorf("hierarchy row %d has an empty node", rowIndex)
		}
		parent := ""
		if row[parentIndex] != nil {
			var parentOK bool
			parent, parentOK = row[parentIndex].(string)
			if !parentOK || strings.TrimSpace(parent) == "" {
				return fmt.Errorf("hierarchy row %d has an invalid parent", rowIndex)
			}
		}
		id := HierarchyNodeIdentity(parent, node)
		if _, exists := parents[id]; exists {
			return fmt.Errorf("duplicate hierarchy node identity %q", id)
		}
		parents[id] = parent
	}
	for id, parent := range parents {
		if parent != "" {
			if _, exists := parents[parent]; !exists {
				return fmt.Errorf("hierarchy node %q references missing parent %q", id, parent)
			}
		}
	}
	for id := range parents {
		seen := map[string]struct{}{id: {}}
		for parent := parents[id]; parent != ""; parent = parents[parent] {
			if _, exists := seen[parent]; exists {
				return fmt.Errorf("hierarchy contains a cycle at %q", parent)
			}
			seen[parent] = struct{}{}
		}
	}
	return nil
}

func validateNetworkRows(spec HierarchyVisualizationSpec, state InlineVisualizationDataState) error {
	if spec.Source == nil || spec.Target == nil || spec.Source.Dataset != spec.Target.Dataset {
		return fmt.Errorf("network source and target fields must share a dataset")
	}
	dataset, ok := inlineDataset(state, spec.Source.Dataset)
	if !ok {
		return fmt.Errorf("network dataset %q is missing", spec.Source.Dataset)
	}
	sourceIndex, targetIndex := columnIndex(dataset.Columns, spec.Source.Field), columnIndex(dataset.Columns, spec.Target.Field)
	if sourceIndex < 0 || targetIndex < 0 {
		return fmt.Errorf("network source or target column is missing")
	}
	for rowIndex, row := range dataset.Rows {
		for _, endpoint := range []struct {
			name  string
			value any
		}{{"source", row[sourceIndex]}, {"target", row[targetIndex]}} {
			value, ok := endpoint.value.(string)
			if !ok || strings.TrimSpace(value) == "" {
				return fmt.Errorf("network row %d has an invalid %s endpoint", rowIndex, endpoint.name)
			}
		}
	}
	return nil
}

func inlineDataset(state InlineVisualizationDataState, id string) (VisualizationInlineDataset, bool) {
	for _, dataset := range state.Datasets {
		if dataset.ID == id {
			return dataset, true
		}
	}
	return VisualizationInlineDataset{}, false
}

func columnIndex(columns []string, id string) int {
	for index, column := range columns {
		if column == id {
			return index
		}
	}
	return -1
}

// HierarchyNodeIdentity returns the canonical identity used by frame builders,
// validators, and renderer adapters. Parent is already a canonical identity;
// node remains the author-facing display label.
func HierarchyNodeIdentity(parent, node string) string {
	escaped := strings.ReplaceAll(node, "\x1f", "\x1f\x1f")
	if parent == "" {
		return escaped
	}
	return parent + "\x1f" + escaped
}
