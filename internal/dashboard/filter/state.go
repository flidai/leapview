package filter

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type AppliedState struct {
	Expression         Expression `json:"expression"`
	ResolvedExpression Expression `json:"resolvedExpression"`
	EvaluatedAt        *time.Time `json:"evaluatedAt,omitempty"`
}

type State struct {
	Revision         uint64                  `json:"revision"`
	AppliedControls  map[string]AppliedState `json:"appliedControls"`
	DraftControls    map[string]Expression   `json:"draftControls,omitempty"`
	DirtyBindings    []string                `json:"dirtyBindings"`
	DefaultsRevision string                  `json:"defaultsRevision"`
}

type TimeSemantics struct {
	Timezone  string `json:"timezone"`
	Calendar  string `json:"calendar"`
	WeekStart string `json:"weekStart"`
}

type BindingSpec struct {
	ValueKind  ValueKind
	Default    Expression
	Required   bool
	Selection  SelectionPolicy
	Editable   bool
	Time       TimeSemantics
	Predicates []PredicatePolicy
}

type CommandKind string

const (
	CommandMutate CommandKind = "mutate"
	CommandApply  CommandKind = "apply"
	CommandCancel CommandKind = "cancel"
	CommandReset  CommandKind = "reset"
)

type MutationOperation string

const (
	MutationSet          MutationOperation = "set"
	MutationClear        MutationOperation = "clear"
	MutationResetBinding MutationOperation = "reset_binding"
)

type ResetScope string

const (
	ResetPage      ResetScope = "page"
	ResetDashboard ResetScope = "dashboard"
)

type Command struct {
	Kind             CommandKind       `json:"kind"`
	BaseRevision     uint64            `json:"baseRevision"`
	ClientMutationID string            `json:"clientMutationID"`
	BindingKey       string            `json:"bindingKey,omitempty"`
	Operation        MutationOperation `json:"operation,omitempty"`
	Expression       *Expression       `json:"expression,omitempty"`
	ResetScope       ResetScope        `json:"resetScope,omitempty"`
	BindingKeys      []string          `json:"bindingKeys,omitempty"`
}

type CommandResult struct {
	State     State `json:"state"`
	Duplicate bool  `json:"duplicate,omitempty"`
}

var (
	ErrStaleRevision  = errors.New("filter state revision is stale")
	ErrBindingLocked  = errors.New("filter binding is locked")
	ErrFilterRequired = errors.New("filter binding is required")
)

type IdempotencyRecord struct {
	Fingerprint string `json:"fingerprint"`
}

type AnchorResolver func(bindingKey string, anchor RelativeAnchor) (Value, error)

// MachineSnapshot contains everything required to resume command processing
// on another replica without losing idempotency records.
type MachineSnapshot struct {
	Version   int                          `json:"version"`
	State     State                        `json:"state"`
	Seen      map[string]IdempotencyRecord `json:"seen,omitempty"`
	SeenOrder []string                     `json:"seenOrder,omitempty"`
}

const MachineSnapshotVersion = 1

type Machine struct {
	mu             sync.Mutex
	mode           ApplicationMode
	bindings       map[string]BindingSpec
	state          State
	seen           map[string]IdempotencyRecord
	seenOrder      []string
	clock          func() time.Time
	anchorResolver AnchorResolver
}

const maxRememberedMutationIDs = 512

func NewMachine(mode ApplicationMode, bindings map[string]BindingSpec) *Machine {
	if mode == "" {
		mode = ApplicationImmediate
	}
	machine := &Machine{
		mode: mode, bindings: cloneBindingSpecs(bindings), seen: map[string]IdempotencyRecord{},
		clock: func() time.Time { return time.Now().UTC() },
		state: State{
			Revision: 1, AppliedControls: map[string]AppliedState{}, DraftControls: map[string]Expression{},
			DirtyBindings: []string{}, DefaultsRevision: defaultsRevision(bindings),
		},
	}
	evaluatedAt := machine.clock()
	for key, binding := range machine.bindings {
		canonical, err := Canonicalize(binding.Default, binding.ValueKind)
		if err != nil {
			panic(fmt.Sprintf("invalid compiled default for binding %q: %v", key, err))
		}
		if !predicateAllowed(canonical, binding.Predicates) {
			panic(fmt.Sprintf("compiled default for binding %q uses disallowed predicate %q operator %q", key, canonical.Kind, canonical.Operator))
		}
		if binding.Required && canonical.Kind == ExpressionUnfiltered {
			panic(fmt.Sprintf("required filter binding %q cannot have an unfiltered default", key))
		}
		machine.bindings[key] = bindingWithDefault(binding, canonical)
		applied, err := machine.resolve(key, canonical, evaluatedAt)
		if err != nil {
			panic(fmt.Sprintf("resolve compiled default for binding %q: %v", key, err))
		}
		machine.state.AppliedControls[key] = applied
	}
	return machine
}

func RestoreMachine(mode ApplicationMode, bindings map[string]BindingSpec, snapshot MachineSnapshot) (*Machine, error) {
	if snapshot.Version != MachineSnapshotVersion {
		return nil, fmt.Errorf("unsupported filter machine snapshot version %d", snapshot.Version)
	}
	machine := NewMachine(mode, bindings)
	machine.state = cloneState(snapshot.State)
	machine.seen = make(map[string]IdempotencyRecord, len(snapshot.Seen))
	for key, value := range snapshot.Seen {
		machine.seen[key] = value
	}
	machine.seenOrder = append([]string(nil), snapshot.SeenOrder...)
	if len(machine.seenOrder) > maxRememberedMutationIDs {
		machine.seenOrder = machine.seenOrder[len(machine.seenOrder)-maxRememberedMutationIDs:]
	}
	if err := machine.validateRestoredState(); err != nil {
		return nil, err
	}
	return machine, nil
}

func (machine *Machine) Snapshot() MachineSnapshot {
	machine.mu.Lock()
	defer machine.mu.Unlock()
	seen := make(map[string]IdempotencyRecord, len(machine.seen))
	for key, value := range machine.seen {
		seen[key] = value
	}
	return MachineSnapshot{
		Version: MachineSnapshotVersion, State: cloneState(machine.state),
		Seen: seen, SeenOrder: append([]string(nil), machine.seenOrder...),
	}
}

func (machine *Machine) validateRestoredState() error {
	if machine.state.Revision == 0 {
		return fmt.Errorf("restored filter revision must be positive")
	}
	for key, binding := range machine.bindings {
		applied, ok := machine.state.AppliedControls[key]
		if !ok {
			return fmt.Errorf("restored filter state is missing binding %q", key)
		}
		if _, err := Canonicalize(applied.Expression, binding.ValueKind); err != nil {
			return fmt.Errorf("restored binding %q expression: %w", key, err)
		}
		if !predicateAllowed(applied.Expression, binding.Predicates) {
			return fmt.Errorf("restored binding %q expression uses a disallowed predicate", key)
		}
		if binding.Required && applied.Expression.Kind == ExpressionUnfiltered {
			return fmt.Errorf("restored binding %q is required", key)
		}
		if _, err := Canonicalize(applied.ResolvedExpression, binding.ValueKind); err != nil {
			return fmt.Errorf("restored binding %q resolved expression: %w", key, err)
		}
	}
	for key := range machine.state.AppliedControls {
		if _, ok := machine.bindings[key]; !ok {
			return fmt.Errorf("restored filter state contains unknown binding %q", key)
		}
	}
	for id := range machine.seen {
		if !containsString(machine.seenOrder, id) {
			return fmt.Errorf("restored idempotency record %q is missing from order", id)
		}
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func bindingWithDefault(binding BindingSpec, expression Expression) BindingSpec {
	binding.Default = expression
	return binding
}

func (machine *Machine) State() State {
	machine.mu.Lock()
	defer machine.mu.Unlock()
	return cloneState(machine.state)
}

func (machine *Machine) SetAnchorResolver(resolver AnchorResolver) {
	machine.mu.Lock()
	machine.anchorResolver = resolver
	machine.mu.Unlock()
}

func (machine *Machine) Execute(command Command) (CommandResult, error) {
	machine.mu.Lock()
	defer machine.mu.Unlock()
	if command.ClientMutationID == "" {
		return CommandResult{State: cloneState(machine.state)}, fmt.Errorf("client mutation ID is required")
	}
	fingerprintBytes, _ := json.Marshal(command)
	fingerprint := string(fingerprintBytes)
	if seen, ok := machine.seen[command.ClientMutationID]; ok {
		if seen.Fingerprint != fingerprint {
			return CommandResult{State: cloneState(machine.state)}, fmt.Errorf("client mutation ID %q was reused with a different command", command.ClientMutationID)
		}
		return CommandResult{State: cloneState(machine.state), Duplicate: true}, nil
	}
	if command.BaseRevision != machine.state.Revision {
		return CommandResult{State: cloneState(machine.state)}, fmt.Errorf("%w: base %d current %d", ErrStaleRevision, command.BaseRevision, machine.state.Revision)
	}
	var err error
	switch command.Kind {
	case CommandMutate:
		err = machine.mutate(command)
	case CommandApply:
		err = machine.apply()
	case CommandCancel:
		machine.cancel()
	case CommandReset:
		err = machine.reset(command)
	default:
		err = fmt.Errorf("unsupported filter command kind %q", command.Kind)
	}
	if err != nil {
		return CommandResult{State: cloneState(machine.state)}, err
	}
	machine.remember(command.ClientMutationID, fingerprint)
	return CommandResult{State: cloneState(machine.state)}, nil
}

func (machine *Machine) mutate(command Command) error {
	binding, ok := machine.bindings[command.BindingKey]
	if !ok {
		return fmt.Errorf("unknown filter binding %q", command.BindingKey)
	}
	if !binding.Editable {
		return fmt.Errorf("%w: %s", ErrBindingLocked, command.BindingKey)
	}
	var expression Expression
	switch command.Operation {
	case MutationSet:
		if command.Expression == nil {
			return fmt.Errorf("set mutation requires expression")
		}
		expression = *command.Expression
	case MutationClear:
		if command.Expression != nil {
			return fmt.Errorf("clear mutation does not accept expression")
		}
		expression = Expression{Kind: ExpressionUnfiltered}
	case MutationResetBinding:
		if command.Expression != nil {
			return fmt.Errorf("reset binding mutation does not accept expression")
		}
		expression = binding.Default
	default:
		return fmt.Errorf("unsupported filter mutation operation %q", command.Operation)
	}
	canonical, err := Canonicalize(expression, binding.ValueKind)
	if err != nil {
		return err
	}
	if err := validateSelection(canonical, binding.Selection); err != nil {
		return err
	}
	if !predicateAllowed(canonical, binding.Predicates) {
		return fmt.Errorf("filter predicate %q operator %q is not allowed", canonical.Kind, canonical.Operator)
	}
	if binding.Required && canonical.Kind == ExpressionUnfiltered {
		return fmt.Errorf("%w: %s", ErrFilterRequired, command.BindingKey)
	}
	if machine.mode == ApplicationDeferred {
		machine.state.DraftControls[command.BindingKey] = canonical
		machine.markDirty(command.BindingKey)
		return nil
	}
	applied, err := machine.resolve(command.BindingKey, canonical, machine.clock())
	if err != nil {
		return err
	}
	machine.state.AppliedControls[command.BindingKey] = applied
	machine.state.Revision++
	return nil
}

func predicateAllowed(expression Expression, policies []PredicatePolicy) bool {
	if expression.Kind == ExpressionUnfiltered {
		return true
	}
	for _, policy := range policies {
		if policy.Kind != expression.Kind {
			continue
		}
		if expression.Operator == "" {
			return true
		}
		for _, operator := range policy.Operators {
			if operator == expression.Operator {
				return true
			}
		}
	}
	return false
}

func validateSelection(expression Expression, policy SelectionPolicy) error {
	if expression.Kind != ExpressionSet {
		return nil
	}
	if policy.Mode == SelectionSingle && len(expression.Values) > 1 {
		return fmt.Errorf("single-selection binding accepts at most one value")
	}
	if policy.MaxSelectedValues > 0 && len(expression.Values) > policy.MaxSelectedValues {
		return fmt.Errorf("selection contains %d values; maximum is %d", len(expression.Values), policy.MaxSelectedValues)
	}
	return nil
}

func (machine *Machine) apply() error {
	if machine.mode != ApplicationDeferred {
		return fmt.Errorf("apply is only valid in deferred mode")
	}
	if len(machine.state.DirtyBindings) == 0 {
		return nil
	}
	evaluatedAt := machine.clock()
	next := make(map[string]AppliedState, len(machine.state.DirtyBindings))
	for _, key := range machine.state.DirtyBindings {
		applied, err := machine.resolve(key, machine.state.DraftControls[key], evaluatedAt)
		if err != nil {
			return err
		}
		next[key] = applied
	}
	for key, applied := range next {
		machine.state.AppliedControls[key] = applied
	}
	machine.state.Revision++
	machine.cancel()
	return nil
}

func (machine *Machine) cancel() {
	machine.state.DraftControls = map[string]Expression{}
	machine.state.DirtyBindings = []string{}
}

func (machine *Machine) reset(command Command) error {
	if command.ResetScope != ResetPage && command.ResetScope != ResetDashboard {
		return fmt.Errorf("unsupported reset scope %q", command.ResetScope)
	}
	keys := append([]string(nil), command.BindingKeys...)
	sort.Strings(keys)
	evaluatedAt := machine.clock()
	for _, key := range keys {
		binding, ok := machine.bindings[key]
		if !ok {
			return fmt.Errorf("unknown filter binding %q", key)
		}
		if machine.mode == ApplicationDeferred {
			machine.state.DraftControls[key] = binding.Default
			machine.markDirty(key)
			continue
		}
		applied, err := machine.resolve(key, binding.Default, evaluatedAt)
		if err != nil {
			return err
		}
		machine.state.AppliedControls[key] = applied
	}
	if machine.mode == ApplicationImmediate && len(keys) > 0 {
		machine.state.Revision++
	}
	return nil
}

func (machine *Machine) markDirty(key string) {
	for _, existing := range machine.state.DirtyBindings {
		if existing == key {
			return
		}
	}
	machine.state.DirtyBindings = append(machine.state.DirtyBindings, key)
	sort.Strings(machine.state.DirtyBindings)
}

func (machine *Machine) resolve(bindingKey string, expression Expression, evaluatedAt time.Time) (AppliedState, error) {
	if expression.Kind != ExpressionRelativePeriod {
		return AppliedState{Expression: expression, ResolvedExpression: expression}, nil
	}
	binding := machine.bindings[bindingKey]
	anchor, err := machine.relativeAnchor(bindingKey, expression, binding, evaluatedAt)
	if err != nil {
		return AppliedState{}, err
	}
	resolved, err := resolveRelativeRange(expression, binding.ValueKind, binding.Time, anchor)
	if err != nil {
		return AppliedState{}, err
	}
	evaluatedAt = evaluatedAt.UTC()
	return AppliedState{Expression: expression, ResolvedExpression: resolved, EvaluatedAt: &evaluatedAt}, nil
}

func (machine *Machine) relativeAnchor(bindingKey string, expression Expression, binding BindingSpec, evaluatedAt time.Time) (Value, error) {
	switch expression.Anchor {
	case AnchorCurrentTime:
		if binding.ValueKind == ValueDate {
			return Value{Kind: ValueDate, Value: evaluatedAt.Format("2006-01-02")}, nil
		}
		return Value{Kind: ValueTimestamp, Value: evaluatedAt.Format(time.RFC3339Nano)}, nil
	case AnchorFixed:
		return *expression.AnchorValue, nil
	case AnchorFirstAvailable, AnchorLastAvailable:
		if machine.anchorResolver == nil {
			return Value{}, fmt.Errorf("binding %q relative anchor %q requires data resolver", bindingKey, expression.Anchor)
		}
		return machine.anchorResolver(bindingKey, expression.Anchor)
	default:
		return Value{}, fmt.Errorf("unsupported relative anchor %q", expression.Anchor)
	}
}

func resolveRelativeRange(expression Expression, kind ValueKind, semantics TimeSemantics, anchor Value) (Expression, error) {
	locationName := semantics.Timezone
	if locationName == "" {
		locationName = "UTC"
	}
	location, err := time.LoadLocation(locationName)
	if err != nil {
		return Expression{}, err
	}
	var instant time.Time
	switch kind {
	case ValueDate:
		instant, err = time.ParseInLocation("2006-01-02", anchor.Value.(string), location)
	case ValueTimestamp:
		instant, err = time.Parse(time.RFC3339Nano, anchor.Value.(string))
		instant = instant.In(location)
	default:
		return Expression{}, fmt.Errorf("relative range requires date or timestamp")
	}
	if err != nil {
		return Expression{}, err
	}
	start := periodStart(instant, expression.Unit, semantics.WeekStart)
	var lower, upper time.Time
	switch expression.Direction {
	case DirectionPrevious:
		if expression.IncludeCurrent {
			lower = addPeriods(start, expression.Unit, -(expression.Count - 1))
			upper = addPeriods(start, expression.Unit, 1)
		} else {
			lower = addPeriods(start, expression.Unit, -expression.Count)
			upper = start
		}
	case DirectionCurrent:
		lower = start
		upper = addPeriods(start, expression.Unit, expression.Count)
	case DirectionNext:
		if expression.IncludeCurrent {
			lower = start
			upper = addPeriods(start, expression.Unit, expression.Count)
		} else {
			lower = addPeriods(start, expression.Unit, 1)
			upper = addPeriods(start, expression.Unit, expression.Count+1)
		}
	default:
		return Expression{}, fmt.Errorf("unsupported relative direction %q", expression.Direction)
	}
	value := func(timestamp time.Time) Value {
		if kind == ValueDate {
			return Value{Kind: kind, Value: timestamp.Format("2006-01-02")}
		}
		return Value{Kind: kind, Value: timestamp.UTC().Format(time.RFC3339Nano)}
	}
	return Expression{
		Kind:  ExpressionRange,
		Lower: &Bound{Value: value(lower), Inclusive: true},
		Upper: &Bound{Value: value(upper), Inclusive: false},
	}, nil
}

func periodStart(value time.Time, unit RelativeUnit, weekStart string) time.Time {
	location := value.Location()
	switch unit {
	case UnitMinute:
		return time.Date(value.Year(), value.Month(), value.Day(), value.Hour(), value.Minute(), 0, 0, location)
	case UnitHour:
		return time.Date(value.Year(), value.Month(), value.Day(), value.Hour(), 0, 0, 0, location)
	case UnitDay:
		return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, location)
	case UnitWeek:
		start := time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, location)
		first := time.Monday
		if weekStart == "sunday" {
			first = time.Sunday
		}
		offset := (7 + int(start.Weekday()) - int(first)) % 7
		return start.AddDate(0, 0, -offset)
	case UnitMonth:
		return time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, location)
	case UnitQuarter:
		month := time.Month(((int(value.Month()) - 1) / 3 * 3) + 1)
		return time.Date(value.Year(), month, 1, 0, 0, 0, 0, location)
	case UnitYear:
		return time.Date(value.Year(), time.January, 1, 0, 0, 0, 0, location)
	default:
		return value
	}
}

func addPeriods(value time.Time, unit RelativeUnit, count int) time.Time {
	switch unit {
	case UnitMinute:
		return value.Add(time.Duration(count) * time.Minute)
	case UnitHour:
		return value.Add(time.Duration(count) * time.Hour)
	case UnitDay:
		return value.AddDate(0, 0, count)
	case UnitWeek:
		return value.AddDate(0, 0, count*7)
	case UnitMonth:
		return value.AddDate(0, count, 0)
	case UnitQuarter:
		return value.AddDate(0, count*3, 0)
	case UnitYear:
		return value.AddDate(count, 0, 0)
	default:
		return value
	}
}

func (machine *Machine) remember(id, fingerprint string) {
	machine.seen[id] = IdempotencyRecord{Fingerprint: fingerprint}
	machine.seenOrder = append(machine.seenOrder, id)
	if len(machine.seenOrder) <= maxRememberedMutationIDs {
		return
	}
	oldest := machine.seenOrder[0]
	machine.seenOrder = machine.seenOrder[1:]
	delete(machine.seen, oldest)
}

func cloneState(state State) State {
	result := state
	result.AppliedControls = make(map[string]AppliedState, len(state.AppliedControls))
	for key, applied := range state.AppliedControls {
		applied.Expression = cloneExpression(applied.Expression)
		applied.ResolvedExpression = cloneExpression(applied.ResolvedExpression)
		if applied.EvaluatedAt != nil {
			value := *applied.EvaluatedAt
			applied.EvaluatedAt = &value
		}
		result.AppliedControls[key] = applied
	}
	result.DraftControls = make(map[string]Expression, len(state.DraftControls))
	for key, expression := range state.DraftControls {
		result.DraftControls[key] = cloneExpression(expression)
	}
	result.DirtyBindings = append([]string(nil), state.DirtyBindings...)
	return result
}

func CloneState(state State) State {
	return cloneState(state)
}

func cloneExpression(expression Expression) Expression {
	bytes, _ := json.Marshal(expression)
	var result Expression
	_ = json.Unmarshal(bytes, &result)
	return result
}

func cloneBindingSpecs(bindings map[string]BindingSpec) map[string]BindingSpec {
	result := make(map[string]BindingSpec, len(bindings))
	for key, binding := range bindings {
		binding.Default = cloneExpression(binding.Default)
		binding.Predicates = append([]PredicatePolicy(nil), binding.Predicates...)
		for index := range binding.Predicates {
			binding.Predicates[index].Operators = append([]Operator(nil), binding.Predicates[index].Operators...)
		}
		result[key] = binding
	}
	return result
}

func defaultsRevision(bindings map[string]BindingSpec) string {
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := make([]struct {
		Key     string
		Default Expression
	}, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, struct {
			Key     string
			Default Expression
		}{key, bindings[key].Default})
	}
	bytes, _ := json.Marshal(ordered)
	sum := sha256Bytes(bytes)
	return sum
}

func sha256Bytes(value []byte) string {
	sum := sha256.Sum256(value)
	return fmt.Sprintf("%x", sum[:])
}
