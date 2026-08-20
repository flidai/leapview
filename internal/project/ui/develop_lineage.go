package ui

import (
	"fmt"
	"sort"
	"strings"

	projectview "github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/project/assetnav"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func assetLineage(projectID string, selected projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) assetLineageModel {
	byID := assetsByID(assets)
	outgoing := edgesByFromAsset(edges)
	incoming := edgesByToAsset(edges)
	graph := assetLineageGraph{
		Nodes: []assetLineageNode{lineageNode(projectID, selected, 0, true, edges)},
	}
	nodeIndex := map[string]int{selected.ID: 0}
	seenEdges := map[string]struct{}{}

	addNode := func(asset projectview.DevelopAssetView, rank int, selected bool) {
		if asset.ID == "" {
			return
		}
		if !selected && isLineageHiddenContextAsset(asset) {
			return
		}
		if existing, ok := nodeIndex[asset.ID]; ok {
			node := graph.Nodes[existing]
			if !uisignals.ValueOrZero(node.Selected) && absInt(rank) < absInt(int(node.Rank)) {
				node.Rank = int64(rank)
				node.Side = lineageSideForRank(rank)
				graph.Nodes[existing] = node
			}
			return
		}
		nodeIndex[asset.ID] = len(graph.Nodes)
		graph.Nodes = append(graph.Nodes, lineageNode(projectID, asset, rank, selected, edges))
	}
	addEdge := func(edge projectview.DevelopEdgeView) {
		if edge.FromAssetID == "" || edge.ToAssetID == "" {
			return
		}
		if _, ok := nodeIndex[edge.FromAssetID]; !ok {
			return
		}
		if _, ok := nodeIndex[edge.ToAssetID]; !ok {
			return
		}
		key := lineageEdgeKey(edge)
		if _, ok := seenEdges[key]; ok {
			return
		}
		graph.Edges = append(graph.Edges, assetLineageEdge{
			ID:     key,
			Source: edge.FromAssetID,
			Target: edge.ToAssetID,
			Label:  uisignals.Optional(labelFromKey(edge.Type)),
			Kind:   edge.Type,
		})
		seenEdges[key] = struct{}{}
	}

	type lineageWalkConfig struct {
		edges  func(string) []projectview.DevelopEdgeView
		peerID func(projectview.DevelopEdgeView) string
		rank   func(int) int
	}
	var walkDependencyEdges func(assetID string, depth int, visiting map[string]struct{}, config lineageWalkConfig)
	walkDependencyEdges = func(assetID string, depth int, visiting map[string]struct{}, config lineageWalkConfig) {
		if _, ok := visiting[assetID]; ok {
			return
		}
		visiting[assetID] = struct{}{}
		defer delete(visiting, assetID)
		for _, edge := range sortedLineageEdges(config.edges(assetID), byID) {
			if !isLineageDependencyEdge(edge) {
				continue
			}
			asset, ok := byID[config.peerID(edge)]
			if !ok {
				continue
			}
			addNode(asset, config.rank(depth), false)
			addEdge(edge)
			walkDependencyEdges(asset.ID, depth+1, visiting, config)
		}
	}

	upstreamWalk := lineageWalkConfig{
		edges: func(assetID string) []projectview.DevelopEdgeView {
			return outgoing[assetID]
		},
		peerID: func(edge projectview.DevelopEdgeView) string {
			return edge.ToAssetID
		},
		rank: func(depth int) int {
			return -depth
		},
	}
	downstreamWalk := lineageWalkConfig{
		edges: func(assetID string) []projectview.DevelopEdgeView {
			return incoming[assetID]
		},
		peerID: func(edge projectview.DevelopEdgeView) string {
			return edge.FromAssetID
		},
		rank: func(depth int) int {
			return depth
		},
	}

	for _, rootID := range lineageDependencyRootIDs(selected, outgoing, byID) {
		if rootID != selected.ID {
			addNode(byID[rootID], 1, false)
		}
		walkDependencyEdges(rootID, 1, map[string]struct{}{}, upstreamWalk)
		if rootID != selected.ID {
			walkDependencyEdges(rootID, 1, map[string]struct{}{}, downstreamWalk)
		}
	}
	walkDependencyEdges(selected.ID, 1, map[string]struct{}{}, downstreamWalk)
	addContainsContext(selected.ID, &graph, nodeIndex, byID, edges, addNode, addEdge)

	sortLineageNodes(graph.Nodes)
	sortLineageGraphEdges(graph.Edges)
	collapsedGraph := collapsedAssetLineageGraph(projectID, selected, graph, byID, edges)
	enrichAssetLineageGraph(collapsedGraph, byID, edges)
	usesRows, usedByRows := lineageTablesFromGraph(projectID, selected, collapsedGraph, byID, edges)
	return assetLineageModel{
		Count:  len(usesRows) + len(usedByRows),
		Graph:  collapsedGraph,
		Uses:   lineageTable(usesRows, "This asset does not reference other assets."),
		UsedBy: lineageTable(usedByRows, "No assets reference this asset."),
	}
}

func enrichAssetLineageGraph(graph assetLineageGraph, assets map[string]projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) {
	nodeIndex := map[string]int{}
	for index, node := range graph.Nodes {
		nodeIndex[node.ID] = index
	}
	for _, edge := range graph.Edges {
		if sourceIndex, ok := nodeIndex[edge.Source]; ok {
			graph.Nodes[sourceIndex].VisibleDownstreamCount = incrementOptionalInt64(graph.Nodes[sourceIndex].VisibleDownstreamCount)
		}
		if targetIndex, ok := nodeIndex[edge.Target]; ok {
			graph.Nodes[targetIndex].VisibleUpstreamCount = incrementOptionalInt64(graph.Nodes[targetIndex].VisibleUpstreamCount)
		}
	}

	containsByParent := map[string]map[string]int{}
	for _, edge := range edges {
		if isLineageDependencyEdge(edge) {
			if index, ok := nodeIndex[edge.FromAssetID]; ok {
				graph.Nodes[index].UsesCount = incrementOptionalInt64(graph.Nodes[index].UsesCount)
			}
			if index, ok := nodeIndex[edge.ToAssetID]; ok {
				graph.Nodes[index].UsedByCount = incrementOptionalInt64(graph.Nodes[index].UsedByCount)
			}
			continue
		}
		if !isContainsEdge(edge) {
			continue
		}
		child, ok := assets[edge.ToAssetID]
		if !ok {
			continue
		}
		if _, ok := containsByParent[edge.FromAssetID]; !ok {
			containsByParent[edge.FromAssetID] = map[string]int{}
		}
		containsByParent[edge.FromAssetID][child.Type]++
	}
	for index, node := range graph.Nodes {
		contains := containsByParent[node.ID]
		if len(contains) == 0 {
			continue
		}
		count := 0
		for _, value := range contains {
			count += value
		}
		graph.Nodes[index].ContainedCount = uisignals.Pointer(int64(count))
		graph.Nodes[index].ContainedSummary = uisignals.Optional(lineageContainedSummary(contains))
	}
}

func lineageContainedSummary(counts map[string]int) string {
	types := make([]string, 0, len(counts))
	for typ := range counts {
		types = append(types, typ)
	}
	sort.Slice(types, func(i, j int) bool {
		return assetTypeLabel(types[i]) < assetTypeLabel(types[j])
	})
	parts := make([]string, 0, len(types))
	for _, typ := range types {
		parts = append(parts, fmt.Sprintf("%d %s", counts[typ], pluralAssetTypeLabel(typ, counts[typ])))
	}
	return strings.Join(parts, ", ")
}

func incrementOptionalInt64(value *int64) *int64 {
	next := uisignals.ValueOrZero(value) + 1
	return &next
}

func pluralAssetTypeLabel(typ string, count int) string {
	label := strings.ToLower(assetTypeLabel(typ))
	if count == 1 {
		return label
	}
	if strings.HasSuffix(label, "y") && len(label) > 1 {
		return strings.TrimSuffix(label, "y") + "ies"
	}
	return label + "s"
}

func collapsedAssetLineageGraph(projectID string, selected projectview.DevelopAssetView, graph assetLineageGraph, assets map[string]projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) assetLineageGraph {
	if selected.Type == "catalog" {
		return graph
	}
	selectedAnchor, selectedAnchorOK := lineageVisibleAnchor(selected, assets)
	out := assetLineageGraph{}
	nodeIndex := map[string]int{}
	addNode := func(asset projectview.DevelopAssetView) {
		if asset.ID == "" {
			return
		}
		if _, ok := nodeIndex[asset.ID]; ok {
			return
		}
		selectedNode := selectedAnchorOK && asset.ID == selectedAnchor.ID
		nodeIndex[asset.ID] = len(out.Nodes)
		out.Nodes = append(out.Nodes, lineageNode(projectID, asset, lineageVisualLayer(asset.Type), selectedNode, edges))
	}

	type collapsedEdge struct {
		source string
		target string
		kind   string
		label  string
	}
	candidates := []collapsedEdge{}
	for _, node := range graph.Nodes {
		asset, ok := assets[node.ID]
		if !ok {
			continue
		}
		if anchor, ok := lineageVisibleAnchor(asset, assets); ok {
			addNode(anchor)
		}
	}
	for _, edge := range graph.Edges {
		if !isLineageDependencyEdge(projectview.DevelopEdgeView{Type: edge.Kind}) {
			continue
		}
		consumer, consumerOK := assets[edge.Source]
		provider, providerOK := assets[edge.Target]
		if !consumerOK || !providerOK {
			continue
		}
		source, sourceOK := lineageVisibleAnchor(provider, assets)
		target, targetOK := lineageVisibleAnchor(consumer, assets)
		if !sourceOK || !targetOK || source.ID == target.ID {
			continue
		}
		policy := lineageProjectionEdge(source.Type, target.Type, edge.Kind)
		addNode(source)
		addNode(target)
		candidates = append(candidates, collapsedEdge{
			source: source.ID,
			target: target.ID,
			kind:   policy.kind,
			label:  policy.label,
		})
	}

	seenEdges := map[string]struct{}{}
	for _, edge := range candidates {
		source := assets[edge.source]
		target := assets[edge.target]
		if lineageVisualLayer(source.Type) >= lineageVisualLayer(target.Type) {
			continue
		}
		key := edge.source + "|" + edge.target + "|" + edge.kind
		if _, ok := seenEdges[key]; ok {
			continue
		}
		seenEdges[key] = struct{}{}
		out.Edges = append(out.Edges, assetLineageEdge{
			ID:     key,
			Source: edge.source,
			Target: edge.target,
			Label:  uisignals.Optional(edge.label),
			Kind:   edge.kind,
		})
	}
	sortLineageNodes(out.Nodes)
	sortLineageGraphEdges(out.Edges)
	return out
}

func edgesByFromAsset(edges []projectview.DevelopEdgeView) map[string][]projectview.DevelopEdgeView {
	out := map[string][]projectview.DevelopEdgeView{}
	for _, edge := range edges {
		out[edge.FromAssetID] = append(out[edge.FromAssetID], edge)
	}
	return out
}

func edgesByToAsset(edges []projectview.DevelopEdgeView) map[string][]projectview.DevelopEdgeView {
	out := map[string][]projectview.DevelopEdgeView{}
	for _, edge := range edges {
		out[edge.ToAssetID] = append(out[edge.ToAssetID], edge)
	}
	return out
}

func sortLineageRows(rows []map[string]any) {
	sort.Slice(rows, func(i, j int) bool {
		return fmt.Sprint(rows[i]["relation"], rows[i]["type"], rows[i]["asset"]) < fmt.Sprint(rows[j]["relation"], rows[j]["type"], rows[j]["asset"])
	})
}

func lineageTablesFromGraph(projectID string, selected projectview.DevelopAssetView, graph assetLineageGraph, assets map[string]projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) ([]map[string]any, []map[string]any) {
	anchorID := selected.ID
	if selected.Type != "catalog" {
		if anchor, ok := lineageVisibleAnchor(selected, assets); ok {
			anchorID = anchor.ID
		}
	}
	usesRows := []map[string]any{}
	usedByRows := []map[string]any{}
	hasDependencyEdges := false
	for _, edge := range graph.Edges {
		if edge.Kind != "contains" {
			hasDependencyEdges = true
			break
		}
	}
	for _, edge := range graph.Edges {
		if hasDependencyEdges {
			if edge.Kind == "contains" {
				continue
			}
			if edge.Target == anchorID {
				if peer, ok := assets[edge.Source]; ok {
					usesRows = append(usesRows, lineageGraphTableRow(projectID, edge, peer, edges))
				}
			}
			if edge.Source == anchorID {
				if peer, ok := assets[edge.Target]; ok {
					usedByRows = append(usedByRows, lineageGraphTableRow(projectID, edge, peer, edges))
				}
			}
			continue
		}
		if edge.Source == anchorID {
			if peer, ok := assets[edge.Target]; ok {
				usesRows = append(usesRows, lineageGraphTableRow(projectID, edge, peer, edges))
			}
		}
		if edge.Target == anchorID {
			if peer, ok := assets[edge.Source]; ok {
				usedByRows = append(usedByRows, lineageGraphTableRow(projectID, edge, peer, edges))
			}
		}
	}
	sortLineageRows(usesRows)
	sortLineageRows(usedByRows)
	return usesRows, usedByRows
}

func lineageGraphTableRow(projectID string, edge assetLineageEdge, peer projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) map[string]any {
	return map[string]any{
		"relation":  firstNonEmpty(uisignals.ValueOrZero(edge.Label), labelFromKey(edge.Kind)),
		"asset":     assetTitle(peer),
		"assetHref": lineageAssetHref(projectID, peer, edges),
		"type":      assetTypeLabel(peer.Type),
		"key":       peer.Key,
	}
}

func lineageTable(rows []map[string]any, empty string) recordTable {
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "relation", Header: "Relationship", Width: uisignals.Pointer("190px")},
			{ID: "asset", Header: "Asset", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("assetHref"), Width: uisignals.Pointer("260px")},
			{ID: "type", Header: "Type", Width: uisignals.Pointer("150px")},
			{ID: "key", Header: "Key", Kind: uisignals.Pointer("code")},
		},
		Rows:     rows,
		Empty:    empty,
		MinWidth: uisignals.Pointer("760px"),
	}
}

func lineageNode(projectID string, asset projectview.DevelopAssetView, rank int, selected bool, edges []projectview.DevelopEdgeView) assetLineageNode {
	return assetLineageNode{
		ID:       asset.ID,
		Label:    assetTitle(asset),
		Kind:     asset.Type,
		Meta:     uisignals.Optional(asset.Key),
		Href:     uisignals.Optional(lineageAssetHref(projectID, asset, edges)),
		Side:     lineageSideForRank(rank),
		Rank:     int64(rank),
		Selected: uisignals.Optional(selected),
	}
}

func lineageSideForRank(rank int) string {
	switch {
	case rank < 0:
		return "upstream"
	case rank > 0:
		return "downstream"
	default:
		return "selected"
	}
}

func lineageDependencyRootIDs(selected projectview.DevelopAssetView, outgoing map[string][]projectview.DevelopEdgeView, assets map[string]projectview.DevelopAssetView) []string {
	rootIDs := []string{selected.ID}
	if !isRollupLineageAsset(selected.Type) {
		return rootIDs
	}
	seen := map[string]struct{}{selected.ID: {}}
	var walk func(string)
	walk = func(assetID string) {
		for _, edge := range sortedLineageEdges(outgoing[assetID], assets) {
			if !isContainsEdge(edge) {
				continue
			}
			if _, ok := seen[edge.ToAssetID]; ok {
				continue
			}
			if _, ok := assets[edge.ToAssetID]; !ok {
				continue
			}
			seen[edge.ToAssetID] = struct{}{}
			rootIDs = append(rootIDs, edge.ToAssetID)
			walk(edge.ToAssetID)
		}
	}
	walk(selected.ID)
	return rootIDs
}

func isLineageHiddenContextAsset(asset projectview.DevelopAssetView) bool {
	return asset.Type == "catalog"
}

func lineageVisibleAnchor(asset projectview.DevelopAssetView, assets map[string]projectview.DevelopAssetView) (projectview.DevelopAssetView, bool) {
	current := asset
	seen := map[string]struct{}{}
	for current.ID != "" {
		if isLineageVisibleGraphAsset(current.Type) {
			return current, true
		}
		if _, ok := seen[current.ID]; ok {
			return projectview.DevelopAssetView{}, false
		}
		seen[current.ID] = struct{}{}
		parent, ok := assets[current.ParentID]
		if !ok {
			return projectview.DevelopAssetView{}, false
		}
		current = parent
	}
	return projectview.DevelopAssetView{}, false
}

func isLineageVisibleGraphAsset(typ string) bool {
	return lineageVisualLayer(typ) >= 0
}

type lineageProjectionLayerPolicy struct {
	assetType string
	layer     int
}

var lineageProjectionLayers = []lineageProjectionLayerPolicy{
	{assetType: "connection", layer: 0},
	{assetType: "source", layer: 1},
	{assetType: "model_table", layer: 2},
	{assetType: "semantic_model", layer: 3},
	{assetType: "dashboard", layer: 4},
}

func lineageVisualLayer(typ string) int {
	for _, policy := range lineageProjectionLayers {
		if typ == policy.assetType {
			return policy.layer
		}
	}
	return -1
}

type lineageProjectionEdgeKey struct {
	sourceType string
	targetType string
}

type lineageProjectionEdgePolicy struct {
	key   lineageProjectionEdgeKey
	kind  string
	label string
}

var lineageProjectionEdges = []lineageProjectionEdgePolicy{
	{
		key:   lineageProjectionEdgeKey{sourceType: "connection", targetType: "source"},
		kind:  "lineage_connection_source",
		label: "Provides source",
	},
	{
		key:   lineageProjectionEdgeKey{sourceType: "source", targetType: "model_table"},
		kind:  "lineage_source_model_table",
		label: "Feeds model table",
	},
	{
		key:   lineageProjectionEdgeKey{sourceType: "model_table", targetType: "semantic_model"},
		kind:  "lineage_model_table_semantic_model",
		label: "Feeds semantic model",
	},
	{
		key:   lineageProjectionEdgeKey{sourceType: "semantic_model", targetType: "dashboard"},
		kind:  "lineage_semantic_model_dashboard",
		label: "Powers dashboard",
	},
}

func lineageProjectionEdge(sourceType, targetType, fallback string) lineageProjectionEdgePolicy {
	key := lineageProjectionEdgeKey{sourceType: sourceType, targetType: targetType}
	for _, policy := range lineageProjectionEdges {
		if policy.key == key {
			return policy
		}
	}
	return lineageProjectionEdgePolicy{
		key:   key,
		kind:  fallback,
		label: labelFromKey(fallback),
	}
}

func lineageCollapsedEdgeKind(sourceType, targetType, fallback string) string {
	return lineageProjectionEdge(sourceType, targetType, fallback).kind
}

func lineageCollapsedEdgeLabel(sourceType, targetType, fallback string) string {
	return lineageProjectionEdge(sourceType, targetType, fallback).label
}

func isRollupLineageAsset(typ string) bool {
	switch typ {
	case "dashboard", "page", "semantic_model":
		return true
	default:
		return false
	}
}

func addContainsContext(selectedID string, graph *assetLineageGraph, nodeIndex map[string]int, assets map[string]projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, addNode func(projectview.DevelopAssetView, int, bool), addEdge func(projectview.DevelopEdgeView)) {
	containsEdges := make([]projectview.DevelopEdgeView, 0)
	for _, edge := range edges {
		if isContainsEdge(edge) {
			containsEdges = append(containsEdges, edge)
		}
	}
	containsEdges = sortedLineageEdges(containsEdges, assets)
	for _, edge := range containsEdges {
		fromIndex, fromOK := nodeIndex[edge.FromAssetID]
		toIndex, toOK := nodeIndex[edge.ToAssetID]
		if !fromOK && !toOK {
			continue
		}
		if fromOK && toOK {
			addEdge(edge)
			continue
		}
		if fromOK && edge.FromAssetID == selectedID {
			asset, ok := assets[edge.ToAssetID]
			if !ok {
				continue
			}
			addNode(asset, int(graph.Nodes[fromIndex].Rank)+1, false)
			addEdge(edge)
			continue
		}
		if toOK {
			asset, ok := assets[edge.FromAssetID]
			if !ok {
				continue
			}
			addNode(asset, int(graph.Nodes[toIndex].Rank)-1, false)
			addEdge(edge)
		}
	}
}

func isLineageDependencyEdge(edge projectview.DevelopEdgeView) bool {
	return !isContainsEdge(edge)
}

func isContainsEdge(edge projectview.DevelopEdgeView) bool {
	return edge.Type == "contains"
}

func lineageEdgeKey(edge projectview.DevelopEdgeView) string {
	if edge.ID != "" {
		return edge.ID
	}
	return edge.FromAssetID + "|" + edge.ToAssetID + "|" + edge.Type
}

func sortedLineageEdges(edges []projectview.DevelopEdgeView, assets map[string]projectview.DevelopAssetView) []projectview.DevelopEdgeView {
	out := append([]projectview.DevelopEdgeView(nil), edges...)
	sort.SliceStable(out, func(i, j int) bool {
		return lineageEdgeSortKey(out[i], assets) < lineageEdgeSortKey(out[j], assets)
	})
	return out
}

func lineageEdgeSortKey(edge projectview.DevelopEdgeView, assets map[string]projectview.DevelopAssetView) string {
	from := assets[edge.FromAssetID]
	to := assets[edge.ToAssetID]
	return edge.Type + ":" + assetTitle(from) + ":" + assetTitle(to) + ":" + lineageEdgeKey(edge)
}

func sortLineageNodes(nodes []assetLineageNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		left := nodes[i]
		right := nodes[j]
		if left.Rank != right.Rank {
			return left.Rank < right.Rank
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Label != right.Label {
			return left.Label < right.Label
		}
		return left.ID < right.ID
	})
}

func sortLineageGraphEdges(edges []assetLineageEdge) {
	sort.SliceStable(edges, func(i, j int) bool {
		left := edges[i]
		right := edges[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		if left.Target != right.Target {
			return left.Target < right.Target
		}
		return left.ID < right.ID
	})
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func lineageAssetHref(projectID string, asset projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) string {
	return assetnav.CanonicalAssetSectionHref(asset, "details")
}
