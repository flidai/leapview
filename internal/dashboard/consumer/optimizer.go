package consumer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

type Strategy string

const (
	StrategySeparate Strategy = "separate"
	StrategyBatch    Strategy = "batch"
	StrategyBundle   Strategy = "bundle"
)

type LogicalQuery struct {
	Target Target
	Query  dataquery.Query
}

type Job struct {
	Strategy Strategy
	Queries  []LogicalQuery
}

type Plan struct {
	Jobs []Job
}

type Optimizer struct {
	planner *semanticquery.Planner
}

// NewOptimizerFromPlanner binds the dashboard consumer optimizer to the
// planner compiled for the active serving generation. The planner is shared
// with governed execution and authorization; this constructor deliberately
// does not compile or copy semantic metadata.
func NewOptimizerFromPlanner(planner *semanticquery.Planner) (*Optimizer, error) {
	if planner == nil || !planner.IsCompiled() {
		return nil, fmt.Errorf("compiled semantic planner is required")
	}
	return &Optimizer{planner: planner}, nil
}

type analyzedQuery struct {
	logical   LogicalQuery
	index     int
	scope     string
	facts     string
	scalar    bool
	grouped   bool
	aggregate bool
}

func (o *Optimizer) Optimize(queries []LogicalQuery) (Plan, error) {
	return o.optimize(queries, true)
}

// OptimizeForConcurrency chooses between one heterogeneous shared projection
// and independent fact-signature bundles. A serial executor benefits from one
// governed fact scan; concurrent readers can execute the smaller bundles in
// parallel without paying projection materialization on the critical path.
func (o *Optimizer) OptimizeForConcurrency(queries []LogicalQuery, concurrency int) (Plan, error) {
	return o.optimize(queries, concurrency <= 1)
}

func (o *Optimizer) optimize(queries []LogicalQuery, fuseHeterogeneousFacts bool) (Plan, error) {
	planner := o.planner
	if !planner.IsCompiled() {
		return Plan{}, fmt.Errorf("compiled semantic planner is required")
	}
	analyzed := make([]analyzedQuery, len(queries))
	for index, logical := range queries {
		item := analyzedQuery{logical: logical, index: index}
		if logical.Query.Kind == dataquery.KindSemanticAggregate {
			request := semanticRequest(logical.Query)
			analysis, err := planner.AnalyzeAggregate(request)
			if err != nil {
				return Plan{}, fmt.Errorf("consumer %s:%s: %w", logical.Target.Kind, logical.Target.ID, err)
			}
			item.aggregate = true
			item.scalar = len(logical.Query.Fields) == 0 && logical.Query.Time.Field == ""
			item.grouped = !item.scalar
			item.facts = strings.Join(analysis.Facts, ",")
		}
		scope, err := physicalScopeKey(logical.Query)
		if err != nil {
			return Plan{}, fmt.Errorf("consumer %s:%s: %w", logical.Target.Kind, logical.Target.ID, err)
		}
		item.scope = scope
		analyzed[index] = item
	}

	assigned := make([]bool, len(analyzed))
	type plannedJob struct {
		job   Job
		index int
	}
	jobs := []plannedJob{}
	for sourceIndex, source := range analyzed {
		if assigned[sourceIndex] || !source.aggregate || !source.grouped {
			continue
		}
		members := []int{sourceIndex}
		for candidateIndex, candidate := range analyzed {
			if candidateIndex == sourceIndex || assigned[candidateIndex] || !candidate.aggregate || candidate.scope != source.scope {
				continue
			}
			if !fuseHeterogeneousFacts && candidate.facts != source.facts {
				continue
			}
			members = append(members, candidateIndex)
		}
		if len(members) < 2 {
			continue
		}
		job := Job{Strategy: StrategyBundle}
		for _, member := range members {
			assigned[member] = true
			job.Queries = append(job.Queries, analyzed[member].logical)
		}
		jobs = append(jobs, plannedJob{job: job, index: sourceIndex})
	}

	// Scalar aggregates with the same governed scope can be combined into one
	// model-scoped multi-member query even when their fact sets differ.
	for index, source := range analyzed {
		if assigned[index] || !source.aggregate || !source.scalar {
			continue
		}
		members := []int{index}
		for candidateIndex := index + 1; candidateIndex < len(analyzed); candidateIndex++ {
			candidate := analyzed[candidateIndex]
			if assigned[candidateIndex] || !candidate.aggregate || !candidate.scalar || candidate.scope != source.scope {
				continue
			}
			members = append(members, candidateIndex)
		}
		if len(members) < 2 {
			continue
		}
		job := Job{Strategy: StrategyBatch}
		for _, member := range members {
			assigned[member] = true
			job.Queries = append(job.Queries, analyzed[member].logical)
		}
		jobs = append(jobs, plannedJob{job: job, index: index})
	}

	for index, source := range analyzed {
		if assigned[index] || !source.aggregate {
			continue
		}
		members := []int{index}
		for candidateIndex := index + 1; candidateIndex < len(analyzed); candidateIndex++ {
			candidate := analyzed[candidateIndex]
			if assigned[candidateIndex] || !candidate.aggregate || candidate.scope != source.scope || candidate.facts != source.facts {
				continue
			}
			members = append(members, candidateIndex)
		}
		strategy := StrategySeparate
		if len(members) > 1 {
			strategy = StrategyBundle
		}
		job := Job{Strategy: strategy}
		for _, member := range members {
			assigned[member] = true
			job.Queries = append(job.Queries, analyzed[member].logical)
		}
		jobs = append(jobs, plannedJob{job: job, index: index})
	}
	for index, item := range analyzed {
		if assigned[index] {
			continue
		}
		assigned[index] = true
		jobs = append(jobs, plannedJob{job: Job{Strategy: StrategySeparate, Queries: []LogicalQuery{item.logical}}, index: index})
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		left := consumerKindPriority(jobs[i].job.Queries[0].Target.Kind)
		right := consumerKindPriority(jobs[j].job.Queries[0].Target.Kind)
		if left != right {
			return left < right
		}
		return jobs[i].index < jobs[j].index
	})
	plan := Plan{Jobs: make([]Job, len(jobs))}
	for index := range jobs {
		plan.Jobs[index] = jobs[index].job
	}
	return plan, nil
}

func physicalScopeKey(query dataquery.Query) (string, error) {
	encoded, err := json.Marshal(struct {
		ModelID     string                 `json:"modelId"`
		Filters     []dataquery.Filter     `json:"filters"`
		ColumnMasks []dataquery.ColumnMask `json:"columnMasks"`
	}{ModelID: query.ModelID, Filters: query.Filters, ColumnMasks: query.ColumnMasks})
	if err != nil {
		return "", fmt.Errorf("encode governed query scope: %w", err)
	}
	return string(encoded), nil
}

func semanticRequest(query dataquery.Query) semanticquery.Request {
	dimensions := make([]semanticquery.Field, len(query.Fields))
	for index, field := range query.Fields {
		dimensions[index] = semanticquery.Field{Field: field.Field, Alias: field.Alias}
	}
	metrics := make([]semanticquery.Field, len(query.Metrics))
	for index, field := range query.Metrics {
		metrics[index] = semanticquery.Field{Field: field.Field, Alias: field.Alias}
	}
	filters := make([]semanticquery.Filter, len(query.Filters))
	for index, filter := range query.Filters {
		filters[index] = semanticFilter(filter)
	}
	return semanticquery.Request{
		Table:      query.Target,
		Dimensions: dimensions,
		Metrics:    metrics,
		Time:       semanticquery.Time{Field: query.Time.Field, Grain: query.Time.Grain, Alias: query.Time.Alias},
		Filters:    filters,
		Limit:      query.Limit,
		Offset:     query.Offset,
	}
}

func semanticFilter(filter dataquery.Filter) semanticquery.Filter {
	groups := make([]semanticquery.FilterGroup, len(filter.Groups))
	for groupIndex, group := range filter.Groups {
		groups[groupIndex].Filters = make([]semanticquery.Filter, len(group.Filters))
		for filterIndex, child := range group.Filters {
			groups[groupIndex].Filters[filterIndex] = semanticFilter(child)
		}
	}
	return semanticquery.Filter{Field: filter.Field, Fact: filter.Fact, Operator: filter.Operator, Values: append([]any{}, filter.Values...), Groups: groups}
}

func consumerKindPriority(kind Kind) int {
	switch kind {
	case KindVisual:
		return 0
	case KindWindow:
		return 2
	default:
		return 3
	}
}
