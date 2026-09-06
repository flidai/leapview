package planir

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SecurityPolicy is the planner handoff produced by semantic authorization.
// A nil Predicate is meaningful: it is an explicitly authorized, grant-only
// dataset and still requires a barrier carrying the authorization identity.
type SecurityPolicy struct {
	PolicyDigest   string
	DecisionDigest string
	Predicate      *Predicate
}

// SecurityBarrier is a source boundary. Its predicate is rendered inside a
// restricted relation before any relationship join, aggregate, or consumer
// filter can observe the rows. Policy and decision identities are immutable
// seals used by rewrite validation and cache identity.
type SecurityBarrier struct {
	NodeMeta
	Input          string     `json:"input"`
	Dataset        string     `json:"dataset"`
	PolicyDigest   string     `json:"policy_digest"`
	DecisionDigest string     `json:"decision_digest"`
	Predicate      *Predicate `json:"predicate,omitempty"`
}

func (SecurityBarrier) Kind() Kind         { return KindSecurityBarrier }
func (n SecurityBarrier) Meta() NodeMeta   { return n.NodeMeta }
func (n SecurityBarrier) Inputs() []string { return []string{n.Input} }
func (SecurityBarrier) nodeMarker()        {}

var securityDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validateSecurityDigest(value, label string) error {
	if !securityDigestPattern.MatchString(value) {
		return fmt.Errorf("%s must be a canonical sha256 digest", label)
	}
	return nil
}

func cloneSecurityPredicate(value *Predicate) *Predicate {
	if value == nil {
		return nil
	}
	clone := canonicalPredicate(*value)
	return &clone
}

func securityBarrier(node Node) (SecurityBarrier, bool) {
	switch value := node.(type) {
	case SecurityBarrier:
		return value, true
	case *SecurityBarrier:
		if value != nil {
			return *value, true
		}
	}
	return SecurityBarrier{}, false
}

func securityScan(node Node) (ScanDataset, bool) {
	switch value := node.(type) {
	case ScanDataset:
		return value, true
	case *ScanDataset:
		if value != nil {
			return *value, true
		}
	}
	return ScanDataset{}, false
}

func securityTraverse(node Node) (TraverseRelationship, bool) {
	switch value := node.(type) {
	case TraverseRelationship:
		return value, true
	case *TraverseRelationship:
		if value != nil {
			return *value, true
		}
	}
	return TraverseRelationship{}, false
}

func validateSecurityTarget(n TraverseRelationship, nodes map[string]Node) error {
	target, ok := nodes[n.TargetInput]
	if !ok || target == nil {
		return fmt.Errorf("relationship target input %q is unavailable", n.TargetInput)
	}
	barrier, ok := securityBarrier(target)
	if !ok || barrier.Dataset != n.Path.ToDataset {
		return fmt.Errorf("relationship target input %q is not a barrier for dataset %q", n.TargetInput, n.Path.ToDataset)
	}
	scan, ok := securityScan(nodes[barrier.Input])
	if !ok || scan.Relation != n.Path.ToRelation {
		return fmt.Errorf("relationship target input %q does not match target relation", n.TargetInput)
	}
	return nil
}

func validateSecurityBarrierNode(n SecurityBarrier, nodes map[string]Node) error {
	if n.Input == "" || n.Dataset == "" {
		return fmt.Errorf("security barrier input and dataset are required")
	}
	if err := validateSecurityDigest(n.PolicyDigest, "security policy digest"); err != nil {
		return err
	}
	if err := validateSecurityDigest(n.DecisionDigest, "security decision digest"); err != nil {
		return err
	}
	scan, ok := securityScan(nodes[n.Input])
	if !ok {
		return fmt.Errorf("security barrier input %q must be ScanDataset", n.Input)
	}
	if !scan.RequiresSecurityBarrier || scan.Dataset != n.Dataset {
		return fmt.Errorf("security barrier dataset %q does not match required scan %q", n.Dataset, n.Input)
	}
	if n.Predicate != nil {
		fields := map[string]bool{}
		metrics := map[string]bool{}
		for _, field := range scan.AvailableFields {
			fields[field.Name] = true
		}
		for _, metric := range scan.AvailableMetrics {
			metrics[metric.Name] = true
		}
		if err := n.Predicate.validate(fields, metrics); err != nil {
			return fmt.Errorf("security barrier predicate: %w", err)
		}
	}
	return nil
}

// ApplySecurityBarriers installs one sealed barrier for every protected root
// scan and every protected relationship target occurrence. The graph is
// mutated only after the input graph has validated; callers should apply this
// once to the final graph (after bundle coalescing).
func ApplySecurityBarriers(graph *Graph, policies map[string]SecurityPolicy) error {
	if graph == nil {
		return fmt.Errorf("plan graph is nil")
	}
	if err := graph.Validate(); err != nil {
		return err
	}
	if len(policies) == 0 {
		return nil
	}
	if graph.securitySealed {
		return fmt.Errorf("security barriers have already been applied")
	}
	datasets := make([]string, 0, len(policies))
	for dataset := range policies {
		datasets = append(datasets, dataset)
	}
	sort.Strings(datasets)
	for _, dataset := range datasets {
		policy := policies[dataset]
		if strings.TrimSpace(dataset) == "" {
			return fmt.Errorf("security policy dataset is empty")
		}
		if err := validateSecurityDigest(policy.PolicyDigest, "security policy digest"); err != nil {
			return fmt.Errorf("dataset %q: %w", dataset, err)
		}
		if err := validateSecurityDigest(policy.DecisionDigest, "security decision digest"); err != nil {
			return fmt.Errorf("dataset %q: %w", dataset, err)
		}
	}

	// Root scans are shared by bundle branches. One barrier protects that
	// physical occurrence for every consumer of the shared scan.
	rootRewrites := map[string]string{}
	ids := sortedNodeIDs(graph)
	for _, id := range ids {
		scan, ok := securityScan(graph.Nodes[id])
		if !ok {
			continue
		}
		policy, protected := policies[scan.Dataset]
		if !protected {
			continue
		}
		if scan.RequiresSecurityBarrier {
			return fmt.Errorf("scan %q is already security protected", id)
		}
		scan.RequiresSecurityBarrier = true
		scan.NodeID = id
		scan.NodeMeta = securityScanMeta(scan.NodeMeta, id, scan.Dataset, policy.Predicate)
		graph.Nodes[id] = scan
		barrierID := id + "_security_barrier"
		if _, exists := graph.Nodes[barrierID]; exists {
			return fmt.Errorf("security barrier node %q already exists", barrierID)
		}
		barrierMeta := scan.NodeMeta
		barrierMeta.NodeID = barrierID
		graph.Nodes[barrierID] = SecurityBarrier{NodeMeta: barrierMeta, Input: id, Dataset: scan.Dataset,
			PolicyDigest: policy.PolicyDigest, DecisionDigest: policy.DecisionDigest, Predicate: cloneSecurityPredicate(policy.Predicate)}
		rootRewrites[id] = barrierID
	}
	if len(rootRewrites) > 0 {
		for _, id := range ids {
			node := graph.Nodes[id]
			rewritten, err := replacePlanIRInputs(node, rootRewrites)
			if err != nil {
				return err
			}
			graph.Nodes[id] = rewritten
		}
		if replacement := rootRewrites[graph.Output]; replacement != "" {
			graph.Output = replacement
		}
	}

	// Targets are not graph inputs in the legacy IR; materialize one scan and
	// one barrier per TraverseRelationship occurrence so role-playing/self
	// joins cannot accidentally share authorization state.
	ids = sortedNodeIDs(graph)
	for _, id := range ids {
		traverse, ok := securityTraverse(graph.Nodes[id])
		if !ok || traverse.TargetInput != "" {
			continue
		}
		policy, protected := policies[traverse.Path.ToDataset]
		if !protected {
			continue
		}
		scanID := id + "_security_target_scan"
		barrierID := id + "_security_target_barrier"
		if _, exists := graph.Nodes[scanID]; exists {
			return fmt.Errorf("security target scan node %q already exists", scanID)
		}
		fields := securityTargetFields(traverse.Path, policy.Predicate)
		scanMeta := securityScanMeta(traverse.NodeMeta, scanID, traverse.Path.ToDataset, policy.Predicate)
		scanMeta.OutputGrain = Grain{}
		scanMeta.AvailableFields = fields
		scanMeta.AvailableMetrics = nil
		scanMeta.RootDatasets = []string{traverse.Path.ToDataset}
		scanMeta.FilterPhase = FilterPhaseScan
		scanMeta.PhysicalLineage = nil
		scanMeta.RelationshipRoutes = nil
		targetScan := ScanDataset{NodeMeta: scanMeta, Dataset: traverse.Path.ToDataset, Relation: traverse.Path.ToRelation, RequiresSecurityBarrier: true}
		barrierMeta := scanMeta
		barrierMeta.NodeID = barrierID
		graph.Nodes[scanID] = targetScan
		graph.Nodes[barrierID] = SecurityBarrier{NodeMeta: barrierMeta, Input: scanID, Dataset: traverse.Path.ToDataset,
			PolicyDigest: policy.PolicyDigest, DecisionDigest: policy.DecisionDigest, Predicate: cloneSecurityPredicate(policy.Predicate)}
		graph.Roots = append(graph.Roots, scanID)
		traverse.TargetInput = barrierID
		traverse.NodeID = id
		graph.Nodes[id] = traverse
	}
	if err := graph.Validate(); err != nil {
		return err
	}
	seals, _, err := securitySeals(graph)
	if err != nil {
		return err
	}
	graph.securityBaseline = append([]securitySeal(nil), seals...)
	graph.securitySealed = true
	graph.securityPolicyDatasets = append([]string(nil), datasets...)
	graph.securitySourceBaseline = securitySourceIdentities(graph)
	return nil
}

func sortedNodeIDs(graph *Graph) []string {
	ids := make([]string, 0, len(graph.Nodes))
	for id := range graph.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func securitySourceIdentities(graph *Graph) []string {
	ids := make([]string, 0)
	for _, id := range sortedNodeIDs(graph) {
		if scan, ok := securityScan(graph.Nodes[id]); ok {
			identity, _ := json.Marshal(struct {
				ID       string `json:"id"`
				Kind     Kind   `json:"kind"`
				Dataset  string `json:"dataset"`
				Relation string `json:"relation,omitempty"`
			}{id, KindScanDataset, scan.Dataset, scan.Relation})
			ids = append(ids, string(identity))
			continue
		}
		if traverse, ok := securityTraverse(graph.Nodes[id]); ok {
			identity, _ := json.Marshal(struct {
				ID          string           `json:"id"`
				Kind        Kind             `json:"kind"`
				Input       string           `json:"input"`
				TargetInput string           `json:"target_input,omitempty"`
				Path        RelationshipPath `json:"path"`
			}{id, KindTraverseRelationship, traverse.Input, traverse.TargetInput, canonicalPath(traverse.Path)})
			ids = append(ids, string(identity))
		}
	}
	return ids
}

func securityScanMeta(meta NodeMeta, id, dataset string, predicate *Predicate) NodeMeta {
	meta.NodeID = id
	meta.RootDatasets = []string{dataset}
	meta.FilterPhase = FilterPhaseScan
	if predicate != nil {
		for _, field := range predicate.fields() {
			found := false
			for _, available := range meta.AvailableFields {
				if available.Name == field {
					found = true
					break
				}
			}
			if !found {
				meta.AvailableFields = append(meta.AvailableFields, Field{Name: field})
			}
		}
	}
	return meta
}

func securityTargetFields(path RelationshipPath, predicate *Predicate) []Field {
	fields := []Field{}
	if predicate != nil {
		for _, name := range predicate.fields() {
			fields = append(fields, Field{Name: name})
		}
	}
	for _, key := range path.JoinKeys {
		for _, name := range []string{key.To, path.ToDataset + "." + key.To} {
			found := false
			for _, field := range fields {
				if field.Name == name {
					found = true
					break
				}
			}
			if !found {
				fields = append(fields, Field{Name: name})
			}
		}
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	return fields
}

func validateSecurityCoverage(graph *Graph) error {
	consumers := map[string][]Node{}
	ids := sortedNodeIDs(graph)
	for _, id := range ids {
		node := graph.Nodes[id]
		for _, input := range node.Inputs() {
			consumers[input] = append(consumers[input], node)
		}
	}
	for _, id := range ids {
		node := graph.Nodes[id]
		scan, ok := securityScan(node)
		if !ok || !scan.RequiresSecurityBarrier {
			continue
		}
		barriers := 0
		sort.Slice(consumers[id], func(i, j int) bool { return consumers[id][i].Meta().NodeID < consumers[id][j].Meta().NodeID })
		for _, consumer := range consumers[id] {
			barrier, isBarrier := securityBarrier(consumer)
			if !isBarrier || barrier.Input != id {
				return fmt.Errorf("protected scan %q has a non-barrier consumer", id)
			}
			barriers++
		}
		if barriers != 1 {
			return fmt.Errorf("protected scan %q requires exactly one security barrier, found %d", id, barriers)
		}
	}
	for _, id := range ids {
		node := graph.Nodes[id]
		barrier, ok := securityBarrier(node)
		if !ok {
			continue
		}
		if barrier.Input == "" {
			return fmt.Errorf("security barrier %q has no input", id)
		}
		if _, ok := securityScan(graph.Nodes[barrier.Input]); !ok {
			return fmt.Errorf("security barrier %q does not directly wrap a scan", id)
		}
	}
	// A target barrier is an occurrence-specific authorization boundary. It
	// must not be reused by two traversals (which would make the seal's path
	// ambiguous and allow one occurrence to move with another).
	targetUsers := map[string][]string{}
	for _, id := range ids {
		node := graph.Nodes[id]
		traverse, ok := securityTraverse(node)
		if !ok || traverse.TargetInput == "" {
			continue
		}
		if _, ok := securityBarrier(graph.Nodes[traverse.TargetInput]); !ok {
			return fmt.Errorf("relationship %q target %q is not a security barrier", id, traverse.TargetInput)
		}
		targetUsers[traverse.TargetInput] = append(targetUsers[traverse.TargetInput], id)
	}
	barrierIDs := make([]string, 0, len(targetUsers))
	for barrierID := range targetUsers {
		barrierIDs = append(barrierIDs, barrierID)
	}
	sort.Strings(barrierIDs)
	for _, barrierID := range barrierIDs {
		users := targetUsers[barrierID]
		if len(users) > 1 {
			sort.Strings(users)
			return fmt.Errorf("security target barrier %q is shared by traversals %v", barrierID, users)
		}
	}
	return nil
}

// securityTargetRelation lowers an occurrence-specific target barrier to a
// derived relation. The target source is rendered here so its predicate
// parameters appear before the join's ON/outer WHERE parameters.
func (r *duckRenderer) securityTargetRelation(n TraverseRelationship) (string, error) {
	toRelation := n.Path.ToRelation
	if n.TargetInput != "" {
		target, err := r.source(n.TargetInput)
		if err != nil {
			return "", err
		}
		toRelation = strings.TrimSpace(target.from)
		suffix := " AS " + quoteName(n.Path.ToDataset)
		if strings.HasSuffix(toRelation, suffix) {
			toRelation = strings.TrimSpace(strings.TrimSuffix(toRelation, suffix))
		}
	}
	if toRelation == "" {
		toRelation = quoteName(n.Path.ToDataset)
	}
	return toRelation, nil
}

func (r *duckRenderer) sourceTraverse(n TraverseRelationship) (sourceContext, error) {
	ctx, err := r.source(n.Input)
	if err != nil {
		return sourceContext{}, err
	}
	if err := validName(n.Path.ToDataset); err != nil {
		return sourceContext{}, fmt.Errorf("relationship target %q: %w", n.Path.ToDataset, err)
	}
	whereArgs := []any(nil)
	if n.TargetInput != "" && ctx.whereArgCount > 0 {
		whereStart := len(r.args) - ctx.whereArgCount
		if whereStart < 0 {
			return sourceContext{}, fmt.Errorf("relationship source argument bookkeeping is invalid")
		}
		whereArgs = append(whereArgs, r.args[whereStart:]...)
		r.args = append([]any(nil), r.args[:whereStart]...)
	}
	toRelation, err := r.securityTargetRelation(n)
	if err != nil {
		return sourceContext{}, err
	}
	if len(whereArgs) > 0 {
		r.args = append(r.args, whereArgs...)
	}
	left := n.Path.FromDataset
	leftAlias := ctx.latestAlias(left)
	joined := ctx.joined
	if leftAlias == "" {
		leftAlias = "r0"
		ctx.from = ctx.from + " AS " + quoteName(leftAlias)
		ctx.aliases[ctx.root] = []string{leftAlias}
		joined = true
	}
	alias := nextSourceAlias(ctx)
	ctx.from += " LEFT JOIN " + toRelation + " AS " + quoteName(alias) + " ON "
	parts := make([]string, 0, len(n.Path.JoinKeys))
	for _, key := range n.Path.JoinKeys {
		if err := validName(key.From); err != nil {
			return sourceContext{}, fmt.Errorf("relationship key %q: %w", key.From, err)
		}
		if err := validName(key.To); err != nil {
			return sourceContext{}, fmt.Errorf("relationship key %q: %w", key.To, err)
		}
		parts = append(parts, quoteName(leftAlias)+"."+quoteName(key.From)+" = "+quoteName(alias)+"."+quoteName(key.To))
	}
	ctx.from += strings.Join(parts, " AND ")
	ctx.aliases[n.Path.ToDataset] = append(ctx.aliases[n.Path.ToDataset], alias)
	ctx.path = append(append([]string(nil), ctx.path...), n.Path.Name)
	if ctx.pathAliases == nil {
		ctx.pathAliases = map[string]string{}
	}
	ctx.pathAliases[strings.Join(ctx.path, "/")] = alias
	ctx.pathAliases[n.Path.Name] = alias
	ctx.joined = joined
	ctx.lastAlias = alias
	ctx.lineage = append(ctx.lineage, n.PhysicalLineage...)
	return ctx, nil
}

func (r *duckRenderer) sourceSecurityBarrier(n SecurityBarrier) (sourceContext, error) {
	ctx, err := r.source(n.Input)
	if err != nil {
		return sourceContext{}, err
	}
	if err := validName(n.Dataset); err != nil {
		return sourceContext{}, fmt.Errorf("security barrier dataset %q: %w", n.Dataset, err)
	}
	parts := append([]string(nil), ctx.where...)
	if n.Predicate != nil {
		predicate, err := renderPredicateWithResolver(*n.Predicate, &r.args, func(name string) (string, error) {
			return r.fieldExpr(name, ctx)
		})
		if err != nil {
			return sourceContext{}, err
		}
		parts = append(parts, predicate)
	}
	from := ctx.from
	if len(parts) > 0 {
		from = "(SELECT * FROM " + from + " WHERE " + strings.Join(parts, " AND ") + ")"
	} else {
		from = "(SELECT * FROM " + from + ")"
	}
	from += " AS " + quoteName(n.Dataset)
	return sourceContext{
		from:        from,
		aliases:     map[string][]string{n.Dataset: {n.Dataset}},
		pathAliases: map[string]string{"": ""},
		lineage:     append([]PhysicalLineage(nil), ctx.lineage...),
		root:        n.Dataset,
	}, nil
}

func replacePlanIRInputs(node Node, replacements map[string]string) (Node, error) {
	replace := func(value string) string {
		if replacement := replacements[value]; replacement != "" {
			return replacement
		}
		return value
	}
	switch value := node.(type) {
	case ScanDataset:
		return value, nil
	case SecurityBarrier:
		return value, nil
	case TraverseRelationship:
		value.Input, value.TargetInput = replace(value.Input), replace(value.TargetInput)
		return value, nil
	case FilterRows:
		value.Input = replace(value.Input)
		return value, nil
	case AggregateMetrics:
		value.Input = replace(value.Input)
		return value, nil
	case StitchAggregates:
		for i := range value.InputsList {
			value.InputsList[i] = replace(value.InputsList[i])
		}
		return value, nil
	case ComputeRatio:
		value.Input = replace(value.Input)
		return value, nil
	case ComputeDerived:
		value.Input = replace(value.Input)
		return value, nil
	case SortLimit:
		value.Input = replace(value.Input)
		return value, nil
	case TotalRows:
		value.Input = replace(value.Input)
		return value, nil
	case BundleBranches:
		for i := range value.Branches {
			value.Branches[i].Input = replace(value.Branches[i].Input)
		}
		return value, nil
	case SpatialEnvelope:
		value.Input = replace(value.Input)
		for i := range value.InputsList {
			value.InputsList[i] = replace(value.InputsList[i])
		}
		return value, nil
	case AnalyticalEnvelope:
		value.Input = replace(value.Input)
		return value, nil
	case *ScanDataset, *SecurityBarrier, *TraverseRelationship, *FilterRows, *AggregateMetrics, *StitchAggregates,
		*ComputeRatio, *ComputeDerived, *SortLimit, *TotalRows, *BundleBranches, *SpatialEnvelope, *AnalyticalEnvelope:
		if node == nil {
			return nil, fmt.Errorf("plan IR node is nil")
		}
		copy := dereferenceNode(node)
		return replacePlanIRInputs(copy, replacements)
	default:
		return nil, fmt.Errorf("unsupported PlanIR node %T", node)
	}
}

func dereferenceNode(node Node) Node {
	switch value := node.(type) {
	case *ScanDataset:
		return *value
	case *SecurityBarrier:
		return *value
	case *TraverseRelationship:
		return *value
	case *FilterRows:
		return *value
	case *AggregateMetrics:
		return *value
	case *StitchAggregates:
		return *value
	case *ComputeRatio:
		return *value
	case *ComputeDerived:
		return *value
	case *SortLimit:
		return *value
	case *TotalRows:
		return *value
	case *BundleBranches:
		return *value
	case *SpatialEnvelope:
		return *value
	case *AnalyticalEnvelope:
		return *value
	default:
		return node
	}
}

type securitySeal struct {
	BarrierID      string
	ScanID         string
	Dataset        string
	Relation       string
	ScanMeta       string
	BarrierMeta    string
	PolicyDigest   string
	DecisionDigest string
	Predicate      string
	Placement      string
	TargetID       string
	TargetPath     string
}

func securityPredicateBytes(predicate *Predicate) string {
	if predicate == nil {
		return ""
	}
	data, _ := json.Marshal(canonicalPredicate(*predicate))
	return string(data)
}

func securitySeals(graph *Graph) ([]securitySeal, map[string]bool, error) {
	protectedDatasets := map[string]bool{}
	seals := []securitySeal{}
	ids := sortedNodeIDs(graph)
	for _, id := range ids {
		node := graph.Nodes[id]
		barrier, ok := securityBarrier(node)
		if !ok {
			continue
		}
		scan, ok := securityScan(graph.Nodes[barrier.Input])
		if !ok {
			return nil, nil, fmt.Errorf("security barrier %q does not wrap a scan", id)
		}
		placement := "root"
		targetID := ""
		targetPath := ""
		for _, candidateID := range ids {
			candidate := graph.Nodes[candidateID]
			traverse, ok := securityTraverse(candidate)
			if ok && traverse.TargetInput == id {
				if targetID != "" {
					return nil, nil, fmt.Errorf("security target barrier %q has multiple traversal consumers", id)
				}
				path, _ := json.Marshal(canonicalPath(traverse.Path))
				placement = "target:" + candidateID + ":" + string(path)
				targetID = candidateID
				targetPath = string(path)
			}
		}
		protectedDatasets[scan.Dataset] = true
		scanMeta, _ := json.Marshal(canonicalMeta(scan.NodeMeta))
		barrierMeta, _ := json.Marshal(canonicalMeta(barrier.NodeMeta))
		seals = append(seals, securitySeal{BarrierID: id, ScanID: barrier.Input, Dataset: scan.Dataset, Relation: scan.Relation,
			ScanMeta: string(scanMeta), BarrierMeta: string(barrierMeta),
			PolicyDigest: barrier.PolicyDigest, DecisionDigest: barrier.DecisionDigest,
			Predicate: securityPredicateBytes(barrier.Predicate), Placement: placement, TargetID: targetID, TargetPath: targetPath})
	}
	sort.Slice(seals, func(i, j int) bool {
		left, _ := json.Marshal(seals[i])
		right, _ := json.Marshal(seals[j])
		return string(left) < string(right)
	})
	return seals, protectedDatasets, nil
}

func validateSecurityBaseline(graph *Graph) error {
	if !graph.securitySealed {
		return nil
	}
	current, protected, err := securitySeals(graph)
	if err != nil {
		return err
	}
	for _, dataset := range graph.securityPolicyDatasets {
		protected[dataset] = true
	}
	left, _ := json.Marshal(graph.securityBaseline)
	right, _ := json.Marshal(current)
	if string(left) != string(right) {
		return fmt.Errorf("security barrier seals changed after authorization")
	}
	if !sameStrings(graph.securitySourceBaseline, securitySourceIdentities(graph)) {
		return fmt.Errorf("security source occurrences changed after authorization")
	}
	ids := sortedNodeIDs(graph)
	for _, id := range ids {
		node := graph.Nodes[id]
		if scan, ok := securityScan(node); ok && protected[scan.Dataset] && !scan.RequiresSecurityBarrier {
			return fmt.Errorf("protected dataset scan %q is missing its security barrier", id)
		}
		if traverse, ok := securityTraverse(node); ok && protected[traverse.Path.ToDataset] && traverse.TargetInput == "" {
			return fmt.Errorf("protected relationship target %q lost its security barrier", traverse.Path.Name)
		}
	}
	return nil
}

// ValidateSecurityRewrite proves that a rewritten graph retains exactly the
// same protected occurrences, authorization identities, predicates, and
// root/relationship placement. It also rejects new unprotected occurrences of
// a dataset that was protected in the original graph.
func ValidateSecurityRewrite(before, after *Graph) error {
	if before == nil || after == nil {
		return fmt.Errorf("security rewrite graphs are required")
	}
	if err := before.Validate(); err != nil {
		return fmt.Errorf("validate pre-rewrite graph: %w", err)
	}
	if err := after.Validate(); err != nil {
		return fmt.Errorf("validate post-rewrite graph: %w", err)
	}
	if before.securitySealed && !after.securitySealed {
		return fmt.Errorf("security rewrite dropped the authorization seal")
	}
	if !sameStrings(before.securityPolicyDatasets, after.securityPolicyDatasets) {
		return fmt.Errorf("security policy dataset set changed during rewrite")
	}
	if !sameStrings(before.securitySourceBaseline, after.securitySourceBaseline) {
		return fmt.Errorf("security source occurrences changed during rewrite")
	}
	beforeSeals, protected, err := securitySeals(before)
	if err != nil {
		return err
	}
	for _, dataset := range before.securityPolicyDatasets {
		protected[dataset] = true
	}
	afterSeals, _, err := securitySeals(after)
	if err != nil {
		return err
	}
	left, _ := json.Marshal(beforeSeals)
	right, _ := json.Marshal(afterSeals)
	if string(left) != string(right) {
		return fmt.Errorf("security barrier seals changed during rewrite")
	}
	ids := sortedNodeIDs(after)
	for _, id := range ids {
		node := after.Nodes[id]
		if scan, ok := securityScan(node); ok && protected[scan.Dataset] && !scan.RequiresSecurityBarrier {
			return fmt.Errorf("protected dataset scan %q is missing its security barrier", id)
		}
		traverse, ok := securityTraverse(node)
		if !ok || !protected[traverse.Path.ToDataset] {
			continue
		}
		if traverse.TargetInput == "" {
			return fmt.Errorf("protected relationship target %q lost its security barrier", traverse.Path.Name)
		}
	}
	return nil
}
