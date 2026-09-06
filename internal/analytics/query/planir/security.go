package planir

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// BindSecurityAdmission attaches an opaque in-process consumer capability to
// a graph after its security seal has been established. It also captures the
// exact renderer envelope for this graph. The capability and envelope are not
// serialized or exposed by any graph projection. A graph can only retain the
// admission by ordinary in-memory cloning (for example WithTotalRows).
func BindSecurityAdmission(graph *Graph, capability any) error {
	if graph == nil {
		return fmt.Errorf("security admission graph is nil")
	}
	if len(graph.securitySeal) == 0 {
		return fmt.Errorf("security admission requires an established security seal")
	}
	if !admissionComparable(capability) {
		return fmt.Errorf("security admission capability is invalid")
	}
	if err := graph.Validate(); err != nil {
		return fmt.Errorf("validate security admission graph: %w", err)
	}
	if graph.securityAdmission != nil && !sameAdmissionCapability(graph.securityAdmission.capability, capability) {
		return fmt.Errorf("security admission capability is already bound")
	}
	rendered, err := RenderDuckDB(graph)
	if err != nil {
		return fmt.Errorf("render security admission graph: %w", err)
	}
	if graph.securityAdmission != nil && !sameRendered(graph.securityAdmission.rendered, rendered) {
		return fmt.Errorf("security admission graph changed after binding")
	}
	graph.securityAdmission = &securityAdmission{capability: capability, rendered: cloneRendered(rendered)}
	return nil
}

// CheckSecurityAdmission reports whether graph carries the exact opaque
// capability supplied by its protected consumer and still renders to the
// exact envelope captured when admission was bound. It intentionally has no
// projection that lets callers retrieve the capability or envelope.
func CheckSecurityAdmission(graph *Graph, capability any) bool {
	if graph == nil || graph.securityAdmission == nil || !admissionComparable(capability) || !sameAdmissionCapability(graph.securityAdmission.capability, capability) {
		return false
	}
	rendered, err := RenderDuckDB(graph)
	return err == nil && sameRendered(graph.securityAdmission.rendered, rendered)
}

// securityAdmission is intentionally private: callers can supply a capability
// to BindSecurityAdmission/CheckSecurityAdmission, but cannot inspect or
// manufacture the admitted renderer envelope.
type securityAdmission struct {
	capability any
	rendered   Rendered
}

func cloneRendered(rendered Rendered) Rendered {
	return Rendered{
		SQL: rendered.SQL, Args: append([]any(nil), rendered.Args...), Columns: append([]string(nil), rendered.Columns...),
	}
}

func sameRendered(left, right Rendered) bool {
	return left.SQL == right.SQL && reflect.DeepEqual(left.Args, right.Args) && reflect.DeepEqual(left.Columns, right.Columns)
}

func validateSecurityAdmissionEnvelope(graph *Graph, admission *securityAdmission) error {
	if graph == nil || admission == nil || !admissionComparable(admission.capability) {
		return fmt.Errorf("security admission is invalid")
	}
	rendered, err := RenderDuckDB(graph)
	if err != nil {
		return fmt.Errorf("render admitted graph: %w", err)
	}
	if !sameRendered(admission.rendered, rendered) {
		return fmt.Errorf("security admission graph changed after binding")
	}
	return nil
}

func admissionComparable(value any) bool {
	if value == nil {
		return false
	}
	typeOf := reflect.TypeOf(value)
	return typeOf.Comparable()
}

func sameAdmissionCapability(left, right any) bool {
	if !admissionComparable(left) || !admissionComparable(right) {
		return false
	}
	leftType, rightType := reflect.TypeOf(left), reflect.TypeOf(right)
	return leftType == rightType && reflect.ValueOf(left).Interface() == reflect.ValueOf(right).Interface()
}

// SecurityBarrier is a scan-local security operation, never an ordinary
// FilterRows node. The private requirement binds its source and predicates
// independently of the exported, inspectable plan. Renderers may not weaken it.
type SecurityBarrier struct {
	NodeMeta
	Input       string                       `json:"input"`
	Policy      string                       `json:"policy"`
	Predicates  []Predicate                  `json:"predicates,omitempty"`
	Routes      map[string]RelationshipRoute `json:"routes,omitempty"`
	Targets     map[string]string            `json:"targets,omitempty"`
	requirement *securityRequirement
}

type securityRequirement struct {
	scan    string
	barrier string
}

func (SecurityBarrier) Kind() Kind       { return KindSecurityBarrier }
func (n SecurityBarrier) Meta() NodeMeta { return n.NodeMeta }
func (n SecurityBarrier) Inputs() []string {
	inputs := []string{n.Input}
	seen := map[string]bool{n.Input: true}
	for _, id := range n.Targets {
		if !seen[id] {
			inputs = append(inputs, id)
			seen[id] = true
		}
	}
	sort.Strings(inputs)
	return inputs
}
func (SecurityBarrier) nodeMarker() {}

// NewSecurityBarrier captures an already evaluated requirement. It does not
// authorize a principal. The caller must use the semantic-access evaluator.
func NewSecurityBarrier(scan ScanDataset, id, policy string, predicates []Predicate) (ScanDataset, SecurityBarrier, error) {
	return NewRoutedSecurityBarrier(scan, id, policy, predicates, nil, nil)
}

// NewRoutedSecurityBarrier retains existing relationship routes for scan-local
// EXISTS checks. Every route target is an explicit governed source input.
func NewRoutedSecurityBarrier(scan ScanDataset, id, policy string, predicates []Predicate, routes map[string]RelationshipRoute, targets map[string]string) (ScanDataset, SecurityBarrier, error) {
	if scan.security != nil || id == "" || id == scan.NodeID || strings.TrimSpace(policy) == "" {
		return scan, SecurityBarrier{}, fmt.Errorf("security barrier requires a new occurrence and policy identity")
	}
	// JSON is used only to detach the existing typed IR, not to encode semantic
	// values or derive another digest. Exact literals are stored as text in IR.
	data, err := json.Marshal(predicates)
	if err != nil {
		return scan, SecurityBarrier{}, err
	}
	var detached []Predicate
	if err = json.Unmarshal(data, &detached); err != nil {
		return scan, SecurityBarrier{}, err
	}
	b := SecurityBarrier{NodeMeta: scan.NodeMeta, Input: scan.NodeID, Policy: policy, Predicates: detached}
	routeData, err := json.Marshal(routes)
	if err != nil {
		return scan, SecurityBarrier{}, err
	}
	if err = json.Unmarshal(routeData, &b.Routes); err != nil {
		return scan, SecurityBarrier{}, err
	}
	if len(targets) > 0 {
		b.Targets = map[string]string{}
		for dataset, input := range targets {
			b.Targets[dataset] = input
		}
	}
	b.NodeID = id
	b.FilterPhase = FilterPhaseScan
	required := &securityRequirement{scan: securityEncoding(scan), barrier: securityEncoding(b)}
	scan.security = required
	b.requirement = required
	return scan, b, nil
}

func securityEncoding(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(b)
}

func securityScan(node Node) (ScanDataset, bool) {
	switch n := node.(type) {
	case ScanDataset:
		return n, true
	case *ScanDataset:
		if n != nil {
			return *n, true
		}
	}
	return ScanDataset{}, false
}
func securityBarrier(node Node) (SecurityBarrier, bool) {
	switch n := node.(type) {
	case SecurityBarrier:
		return n, true
	case *SecurityBarrier:
		if n != nil {
			return *n, true
		}
	}
	return SecurityBarrier{}, false
}

func validateSecurityBarrier(b SecurityBarrier, nodes map[string]Node) error {
	scan, ok := securityScan(nodes[b.Input])
	if !ok || b.requirement == nil || scan.security != b.requirement {
		return fmt.Errorf("security barrier lacks its bound protected scan")
	}
	if securityEncoding(scan) != b.requirement.scan || securityEncoding(b) != b.requirement.barrier {
		return fmt.Errorf("security barrier requirement was changed")
	}
	fields := map[string]bool{}
	for _, field := range scan.AvailableFields {
		fields[field.Name] = true
	}
	usedTargets := map[string]bool{}
	for field, route := range b.Routes {
		if route.RootDataset != scan.Dataset || len(route.Edges) == 0 {
			return fmt.Errorf("security route has invalid root")
		}
		current := scan.Dataset
		visited := map[string]bool{current: true}
		for _, edge := range route.Edges {
			if edge.FromDataset != current || visited[edge.ToDataset] || len(edge.JoinKeys) == 0 {
				return fmt.Errorf("security route is discontinuous or cyclic")
			}
			target := nodes[b.Targets[edge.ToDataset]]
			if target == nil || len(target.Meta().RootDatasets) != 1 || target.Meta().RootDatasets[0] != edge.ToDataset {
				return fmt.Errorf("security route target is unavailable")
			}
			if _, ok := securityScan(target); !ok {
				if _, ok := securityBarrier(target); !ok {
					return fmt.Errorf("security route target is not a governed scan")
				}
			}
			usedTargets[edge.ToDataset] = true
			current = edge.ToDataset
			visited[current] = true
		}
		if !strings.HasPrefix(field, current+".") {
			return fmt.Errorf("security route does not reach its field")
		}
		fields[field] = true
	}
	if len(usedTargets) != len(b.Targets) {
		return fmt.Errorf("security target has no predicate route")
	}
	for _, predicate := range b.Predicates {
		if (predicate.Kind != PredicateCompare || predicate.Operator != "=") && (predicate.Kind != PredicateIn || predicate.Negated) {
			return fmt.Errorf("security barrier requires bound equality or membership")
		}
		if err := predicate.validate(fields, nil); err != nil {
			return err
		}
		for _, field := range predicate.fields() {
			if strings.Contains(field, ".") && !strings.HasPrefix(field, scan.Dataset+".") {
				if _, ok := b.Routes[field]; !ok {
					return fmt.Errorf("security predicate %q is outside protected scan %q", field, scan.Dataset)
				}
			}
		}
	}
	for field := range b.Routes {
		found := false
		for _, p := range b.Predicates {
			if p.Field == field {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("security route has no predicate")
		}
	}
	return nil
}

// SealSecurity captures the security-bearing topology after source lowering.
// A rewrite may retain it, but cannot reset it to bless modified requirements.
func (g *Graph) SealSecurity() error {
	if g.securitySeal != nil {
		return g.Validate()
	}
	seal := map[string]string{}
	for id, node := range g.Nodes {
		if s, ok := securityScan(node); ok {
			seal[id] = securityEncoding(s)
		}
		if b, ok := securityBarrier(node); ok {
			seal[id] = securityEncoding(b)
		}
		switch n := node.(type) {
		case TraverseRelationship:
			if n.TargetInput != "" {
				seal[id] = securityEncoding(n)
			}
		case *TraverseRelationship:
			if n != nil && n.TargetInput != "" {
				seal[id] = securityEncoding(n)
			}
		}
	}
	if len(seal) > 0 {
		g.securitySeal = seal
	}
	return g.Validate()
}

func (g *Graph) validateSecurity() error {
	ids := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if g.securitySeal != nil {
			var traversal *TraverseRelationship
			switch n := g.Nodes[id].(type) {
			case TraverseRelationship:
				traversal = &n
			case *TraverseRelationship:
				traversal = n
			}
			if traversal != nil {
				if _, known := g.securitySeal[id]; !known || traversal.TargetInput == "" {
					return fmt.Errorf("relationship %q has no sealed explicit target", id)
				}
			}
		}
		scan, ok := securityScan(g.Nodes[id])
		if !ok {
			continue
		}
		if g.securitySeal != nil {
			if _, known := g.securitySeal[id]; !known {
				return fmt.Errorf("new scan %q has no sealed security requirement", id)
			}
		}
		if scan.security == nil {
			continue
		}
		uses := 0
		for _, other := range ids {
			for _, input := range g.Nodes[other].Inputs() {
				if input == id {
					barrier, ok := securityBarrier(g.Nodes[other])
					if !ok || barrier.requirement != scan.security {
						return fmt.Errorf("protected scan %q escapes its security barrier", id)
					}
					uses++
				}
			}
		}
		if uses != 1 || g.Output == id {
			return fmt.Errorf("protected scan %q requires exactly one security barrier", id)
		}
	}
	keys := make([]string, 0, len(g.securitySeal))
	for id := range g.securitySeal {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		if g.Nodes[id] == nil || securityEncoding(g.Nodes[id]) != g.securitySeal[id] {
			return fmt.Errorf("sealed security occurrence %q was removed or rewritten", id)
		}
	}
	return nil
}

// ValidateSecurityRewrite compares a proposed rewrite to its original graph.
// New graph objects must retain the same security requirements and occurrences.
func ValidateSecurityRewrite(before, after *Graph) error {
	if err := before.Validate(); err != nil {
		return err
	}
	if err := after.Validate(); err != nil {
		return err
	}
	if !reflect.DeepEqual(before.securitySeal, after.securitySeal) {
		return fmt.Errorf("rewrite lost security requirements")
	}
	for id, node := range before.Nodes {
		if b, ok := securityBarrier(node); ok {
			other, ok := securityBarrier(after.Nodes[id])
			if !ok || other.requirement != b.requirement {
				return fmt.Errorf("rewrite lost security barrier %q", id)
			}
		}
	}
	return nil
}
