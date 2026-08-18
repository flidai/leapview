package filter

type PredicatePolicy struct {
	Kind      ExpressionKind `json:"kind" yaml:"-"`
	Operators []Operator     `json:"operators,omitempty" yaml:"-"`
}

type OptionSourceKind string

const (
	OptionSourceNone     OptionSourceKind = ""
	OptionSourceStatic   OptionSourceKind = "static"
	OptionSourceDistinct OptionSourceKind = "distinct"
)

type OptionSource struct {
	Kind        OptionSourceKind `json:"kind,omitempty" yaml:"-"`
	Limit       int              `json:"limit,omitempty" yaml:"-"`
	IncludeNull bool             `json:"includeNull,omitempty" yaml:"-"`
	Values      []Option         `json:"values,omitempty" yaml:"-"`
}

type Option struct {
	Value Value  `json:"value" yaml:"-"`
	Label string `json:"label" yaml:"-"`
}

type Formatting struct {
	Pattern string `json:"pattern,omitempty" yaml:"-"`
	Unit    string `json:"unit,omitempty" yaml:"-"`
}

type Definition struct {
	Label       string            `json:"label" yaml:"-"`
	Description string            `json:"description,omitempty" yaml:"-"`
	Field       string            `json:"field" yaml:"-"`
	Dataset     string            `json:"dataset,omitempty" yaml:"-"`
	ValueKind   ValueKind         `json:"valueKind,omitempty" yaml:"-"`
	Time        TimeSemantics     `json:"time,omitempty" yaml:"-"`
	Predicates  []PredicatePolicy `json:"predicates" yaml:"-"`
	Options     OptionSource      `json:"options,omitempty" yaml:"-"`
	Formatting  Formatting        `json:"formatting,omitempty" yaml:"-"`
}

type SelectionMode string

const (
	SelectionSingle   SelectionMode = "single"
	SelectionMultiple SelectionMode = "multiple"
)

type SelectionPolicy struct {
	Mode              SelectionMode `json:"mode,omitempty" yaml:"-"`
	MaxSelectedValues int           `json:"maxSelectedValues,omitempty" yaml:"-"`
}

type URLEncoding string

const URLEncodingTypedV1 URLEncoding = "typed_v1"

type URLPolicy struct {
	Param    string      `json:"param,omitempty" yaml:"-"`
	Encoding URLEncoding `json:"encoding,omitempty" yaml:"-"`
}

type PanePolicy struct {
	Visible *bool  `json:"visible,omitempty" yaml:"-"`
	Order   int    `json:"order,omitempty" yaml:"-"`
	Label   string `json:"label,omitempty" yaml:"-"`
}

type TargetPolicy struct {
	Include []string `json:"include,omitempty" yaml:"-"`
	Exclude []string `json:"exclude,omitempty" yaml:"-"`
}

type BindingRef struct {
	Scope Scope  `json:"scope" yaml:"-"`
	ID    string `json:"id" yaml:"-"`
}

type OptionInteractions struct {
	Include []BindingRef `json:"include,omitempty" yaml:"-"`
	Exclude []BindingRef `json:"exclude,omitempty" yaml:"-"`
}

type Binding struct {
	Filter             string             `json:"filter" yaml:"-"`
	Default            Expression         `json:"default" yaml:"-"`
	Required           bool               `json:"required,omitempty" yaml:"-"`
	Selection          SelectionPolicy    `json:"selection,omitempty" yaml:"-"`
	ReaderEditable     *bool              `json:"readerEditable,omitempty" yaml:"-"`
	URL                URLPolicy          `json:"url,omitempty" yaml:"-"`
	Pane               PanePolicy         `json:"pane,omitempty" yaml:"-"`
	TargetPolicy       TargetPolicy       `json:"targetPolicy,omitempty" yaml:"-"`
	OptionInteractions OptionInteractions `json:"optionInteractions,omitempty" yaml:"-"`

	Key                string       `json:"key,omitempty" yaml:"-"`
	ID                 string       `json:"id,omitempty" yaml:"-"`
	Scope              Scope        `json:"scope,omitempty" yaml:"-"`
	PageID             string       `json:"pageID,omitempty" yaml:"-"`
	ValueKind          ValueKind    `json:"valueKind,omitempty" yaml:"-"`
	Targets            []string     `json:"targets,omitempty" yaml:"-"`
	OptionDependencies []BindingRef `json:"optionDependencies,omitempty" yaml:"-"`
}

func (binding Binding) Editable() bool {
	return binding.ReaderEditable == nil || *binding.ReaderEditable
}

func (pane PanePolicy) IsVisible() bool {
	return pane.Visible == nil || *pane.Visible
}

type ApplicationMode string

const (
	ApplicationImmediate ApplicationMode = "immediate"
	ApplicationDeferred  ApplicationMode = "deferred"
)

type ApplicationPolicy struct {
	Mode ApplicationMode `json:"mode" yaml:"-"`
}

func (policy ApplicationPolicy) WithDefaults() ApplicationPolicy {
	if policy.Mode == "" {
		policy.Mode = ApplicationImmediate
	}
	return policy
}

type PresentationStyle string

const (
	PresentationDropdown       PresentationStyle = "dropdown"
	PresentationList           PresentationStyle = "list"
	PresentationButtons        PresentationStyle = "buttons"
	PresentationInput          PresentationStyle = "input"
	PresentationNumericRange   PresentationStyle = "numeric_range"
	PresentationDateRange      PresentationStyle = "date_range"
	PresentationRelativePeriod PresentationStyle = "relative_period"
)

type Presentation struct {
	Style       PresentationStyle `json:"style" yaml:"-"`
	Search      bool              `json:"search,omitempty" yaml:"-"`
	SelectAll   bool              `json:"selectAll,omitempty" yaml:"-"`
	ShowCounts  bool              `json:"showCounts,omitempty" yaml:"-"`
	ShowSummary bool              `json:"showSummary,omitempty" yaml:"-"`
	Compact     bool              `json:"compact,omitempty" yaml:"-"`
	Title       string            `json:"title,omitempty" yaml:"-"`
	Description string            `json:"description,omitempty" yaml:"-"`
	AriaLabel   string            `json:"ariaLabel,omitempty" yaml:"-"`
}
