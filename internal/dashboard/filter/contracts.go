package filter

type PredicatePolicy struct {
	Kind      ExpressionKind `json:"kind" yaml:"kind"`
	Operators []Operator     `json:"operators,omitempty" yaml:"operators,omitempty"`
}

type OptionSourceKind string

const (
	OptionSourceNone     OptionSourceKind = ""
	OptionSourceStatic   OptionSourceKind = "static"
	OptionSourceDistinct OptionSourceKind = "distinct"
)

type OptionSource struct {
	Kind   OptionSourceKind `json:"kind,omitempty" yaml:"kind,omitempty"`
	Limit  int              `json:"limit,omitempty" yaml:"limit,omitempty"`
	Values []Option         `json:"values,omitempty" yaml:"values,omitempty"`
}

type Option struct {
	Value Value  `json:"value" yaml:"value"`
	Label string `json:"label" yaml:"label"`
}

type Formatting struct {
	Pattern string `json:"pattern,omitempty" yaml:"pattern,omitempty"`
	Unit    string `json:"unit,omitempty" yaml:"unit,omitempty"`
}

type Definition struct {
	Label       string            `json:"label" yaml:"label"`
	Description string            `json:"description,omitempty" yaml:"description,omitempty"`
	Field       string            `json:"field" yaml:"field"`
	Dataset     string            `json:"dataset,omitempty" yaml:"dataset,omitempty"`
	ValueKind   ValueKind         `json:"valueKind,omitempty" yaml:"-"`
	Time        TimeSemantics     `json:"time,omitempty" yaml:"-"`
	Predicates  []PredicatePolicy `json:"predicates" yaml:"predicates"`
	Options     OptionSource      `json:"options,omitempty" yaml:"options,omitempty"`
	Formatting  Formatting        `json:"formatting,omitempty" yaml:"formatting,omitempty"`
}

type SelectionMode string

const (
	SelectionSingle   SelectionMode = "single"
	SelectionMultiple SelectionMode = "multiple"
)

type SelectionPolicy struct {
	Mode              SelectionMode `json:"mode,omitempty" yaml:"mode,omitempty"`
	MaxSelectedValues int           `json:"maxSelectedValues,omitempty" yaml:"max_selected_values,omitempty"`
}

type URLEncoding string

const URLEncodingTypedV1 URLEncoding = "typed_v1"

type URLPolicy struct {
	Param    string      `json:"param,omitempty" yaml:"param,omitempty"`
	Encoding URLEncoding `json:"encoding,omitempty" yaml:"encoding,omitempty"`
}

type PanePolicy struct {
	Visible *bool  `json:"visible,omitempty" yaml:"visible,omitempty"`
	Order   int    `json:"order,omitempty" yaml:"order,omitempty"`
	Label   string `json:"label,omitempty" yaml:"label,omitempty"`
}

type TargetPolicy struct {
	Include []string `json:"include,omitempty" yaml:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty" yaml:"exclude,omitempty"`
}

type BindingRef struct {
	Scope Scope  `json:"scope" yaml:"scope"`
	ID    string `json:"id" yaml:"id"`
}

type OptionInteractions struct {
	Include []BindingRef `json:"include,omitempty" yaml:"include,omitempty"`
	Exclude []BindingRef `json:"exclude,omitempty" yaml:"exclude,omitempty"`
}

type Binding struct {
	Filter             string             `json:"filter" yaml:"filter"`
	Default            Expression         `json:"default" yaml:"default"`
	Selection          SelectionPolicy    `json:"selection,omitempty" yaml:"selection,omitempty"`
	ReaderEditable     *bool              `json:"readerEditable,omitempty" yaml:"reader_editable,omitempty"`
	URL                URLPolicy          `json:"url,omitempty" yaml:"url,omitempty"`
	Pane               PanePolicy         `json:"pane,omitempty" yaml:"pane,omitempty"`
	TargetPolicy       TargetPolicy       `json:"targetPolicy,omitempty" yaml:"targets,omitempty"`
	OptionInteractions OptionInteractions `json:"optionInteractions,omitempty" yaml:"option_interactions,omitempty"`

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
	Mode ApplicationMode `json:"mode" yaml:"mode"`
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
	Style       PresentationStyle `json:"style" yaml:"style"`
	Search      bool              `json:"search,omitempty" yaml:"search,omitempty"`
	SelectAll   bool              `json:"selectAll,omitempty" yaml:"select_all,omitempty"`
	ShowCounts  bool              `json:"showCounts,omitempty" yaml:"show_counts,omitempty"`
	ShowSummary bool              `json:"showSummary,omitempty" yaml:"show_summary,omitempty"`
	Compact     bool              `json:"compact,omitempty" yaml:"compact,omitempty"`
	Title       string            `json:"title,omitempty" yaml:"title,omitempty"`
	Description string            `json:"description,omitempty" yaml:"description,omitempty"`
	AriaLabel   string            `json:"ariaLabel,omitempty" yaml:"aria_label,omitempty"`
}
