package model

import (
	"fmt"
	"sort"
	"strings"
)

// SafeRelationshipPath resolves joins that preserve the base table's grain.
// A many-to-one relationship is traversable only from its many side, while a
// one-to-one relationship is traversable in either direction. Model validation
// proves every "one" endpoint with the table's declared primary key; reverse
// many-to-one, one-to-many, and many-to-many paths are deliberately unavailable
// because they can multiply metrics.
func (m *Model) SafeRelationshipPath(base, target string) ([]Relationship, error) {
	if base == target {
		return nil, nil
	}
	matches := m.safeRelationshipPaths(base, target)
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no safe relationship path from %q to %q", base, target)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("ambiguous relationship path from %q to %q", base, target)
	}
}

func (m *Model) CanReachField(fact, field string) error {
	dimension, err := m.ResolveDimension(field)
	if err != nil {
		return err
	}
	_, err = m.SafeRelationshipPath(fact, dimension.Table)
	return err
}

func (m *Model) safeEdgesFrom(table string) []relationshipEdge {
	edges := []relationshipEdge{}
	for _, relationship := range m.Relationships {
		edge, ok := semanticSafeEdgeFrom(table, relationship)
		if ok {
			edges = append(edges, edge)
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Table != edges[j].Table {
			return edges[i].Table < edges[j].Table
		}
		if edges[i].Relationship.ID != edges[j].Relationship.ID {
			return edges[i].Relationship.ID < edges[j].Relationship.ID
		}
		if edges[i].Relationship.FromDataset != edges[j].Relationship.FromDataset {
			return edges[i].Relationship.FromDataset < edges[j].Relationship.FromDataset
		}
		if strings.Join(edges[i].Relationship.FromFields, "\x00") != strings.Join(edges[j].Relationship.FromFields, "\x00") {
			return strings.Join(edges[i].Relationship.FromFields, "\x00") < strings.Join(edges[j].Relationship.FromFields, "\x00")
		}
		if edges[i].Relationship.ToDataset != edges[j].Relationship.ToDataset {
			return edges[i].Relationship.ToDataset < edges[j].Relationship.ToDataset
		}
		return strings.Join(edges[i].Relationship.ToFields, "\x00") < strings.Join(edges[j].Relationship.ToFields, "\x00")
	})
	return edges
}

func (m *Model) safeRelationshipPaths(base, target string) [][]Relationship {
	matches := [][]Relationship{}
	var walk func(candidate relationshipPathCandidate)
	walk = func(candidate relationshipPathCandidate) {
		if len(matches) > 1 {
			return
		}
		for _, edge := range m.safeEdgesFrom(candidate.Table) {
			if len(matches) > 1 {
				return
			}
			if candidate.Visited[edge.Table] {
				continue
			}
			path := append(append([]Relationship{}, candidate.Path...), edge.Relationship)
			if edge.Table == target {
				matches = append(matches, path)
				continue
			}
			visited := map[string]bool{}
			for table, value := range candidate.Visited {
				visited[table] = value
			}
			visited[edge.Table] = true
			walk(relationshipPathCandidate{Table: edge.Table, Path: path, Visited: visited})
		}
	}
	walk(relationshipPathCandidate{Table: base, Visited: map[string]bool{base: true}})
	return matches
}

func semanticSafeEdgeFrom(table string, relationship Relationship) (relationshipEdge, bool) {
	fromTable, _, err := relationshipEndpoint(relationship, true)
	if err != nil {
		return relationshipEdge{}, false
	}
	toTable, _, err := relationshipEndpoint(relationship, false)
	if err != nil {
		return relationshipEdge{}, false
	}
	if fromTable == table && semanticSafeCardinality(relationship.Cardinality) {
		return relationshipEdge{Table: toTable, Relationship: relationship}, true
	}
	if relationship.Cardinality == "one_to_one" && toTable == table {
		return relationshipEdge{Table: fromTable, Relationship: relationship}, true
	}
	return relationshipEdge{}, false
}

func semanticSafeCardinality(cardinality string) bool {
	return cardinality == "many_to_one" || cardinality == "one_to_one"
}

type relationshipPathCandidate struct {
	Table   string
	Path    []Relationship
	Visited map[string]bool
}

type relationshipEdge struct {
	Table        string
	Relationship Relationship
}
