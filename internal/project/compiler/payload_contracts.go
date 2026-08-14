package compiler

import dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
import dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"

type catalogPayloadV1 struct {
	Workspace      catalogWorkspacePayloadV1   `json:"Workspace"`
	SemanticModels []catalogModelPayloadV1     `json:"SemanticModels"`
	Dashboards     []catalogDashboardPayloadV1 `json:"Dashboards"`
}

type catalogWorkspacePayloadV1 struct {
	ID          string `json:"ID"`
	Title       string `json:"Title"`
	Description string `json:"Description"`
}

type catalogModelPayloadV1 struct {
	ID          string `json:"ID"`
	Title       string `json:"Title"`
	Path        string `json:"Path"`
	Description string `json:"Description"`
}

type catalogDashboardPayloadV1 struct {
	ID          string                    `json:"ID"`
	Title       string                    `json:"Title"`
	Path        string                    `json:"Path"`
	Description string                    `json:"Description"`
	Tags        []string                  `json:"Tags"`
	Appearance  dashboardappearance.Patch `json:"Appearance,omitempty"`
}

type connectionPayloadV1 struct {
	Kind     string                      `json:"Kind"`
	Path     string                      `json:"Path"`
	Root     string                      `json:"Root"`
	Scope    string                      `json:"Scope"`
	Options  map[string]any              `json:"Options"`
	Defaults connectionDefaultsPayloadV1 `json:"Defaults"`
}

type connectionDefaultsPayloadV1 struct {
	Options map[string]any `json:"Options"`
}

type sourcePayloadV1 struct {
	Format     string                          `json:"Format"`
	Path       string                          `json:"Path"`
	Connection string                          `json:"Connection"`
	Object     string                          `json:"Object"`
	Options    map[string]any                  `json:"Options"`
	Fields     map[string]sourceFieldPayloadV1 `json:"Fields"`
	Schema     schemaPayloadV1                 `json:"Schema"`
}

type sourceFieldPayloadV1 struct {
	Name        string `json:"Name"`
	Field       string `json:"Field"`
	Table       string `json:"Table"`
	Type        string `json:"Type"`
	Description string `json:"Description"`
}

type modelTablePayloadV1 struct {
	Source             string                          `json:"Source"`
	Sources            []string                        `json:"Sources"`
	SourceDependencies []string                        `json:"SourceDependencies"`
	ModelDependencies  []string                        `json:"ModelDependencies"`
	Transform          transformPayloadV1              `json:"Transform"`
	SQL                string                          `json:"SQL"`
	PrimaryKey         string                          `json:"PrimaryKey"`
	Grain              string                          `json:"Grain"`
	Dimensions         map[string]fieldPayloadV1       `json:"Dimensions"`
	Fields             map[string]fieldPayloadV1       `json:"Fields"`
	Columns            map[string]modelColumnPayloadV1 `json:"Columns"`
	Schema             schemaPayloadV1                 `json:"Schema"`
}

type semanticTablePayloadV1 struct {
	Table string `json:"Table"`
	modelTablePayloadV1
}

type transformPayloadV1 struct {
	SQL string `json:"SQL"`
}

type semanticModelPayloadV1 struct {
	Name          string                                `json:"Name"`
	Title         string                                `json:"Title"`
	Description   string                                `json:"Description"`
	Connections   map[string]connectionPayloadV1        `json:"Connections"`
	Sources       map[string]sourcePayloadV1            `json:"Sources"`
	Tables        map[string]modelTablePayloadV1        `json:"Tables"`
	Models        map[string]modelTablePayloadV1        `json:"Models"`
	Measures      map[string]measurePayloadV1           `json:"Measures"`
	Dimensions    map[string]semanticDimensionPayloadV1 `json:"Dimensions"`
	Metrics       map[string]metricPayloadV1            `json:"Metrics"`
	Relationships []relationshipPayloadV1               `json:"Relationships"`
}

type fieldPayloadV1 struct {
	Field       string `json:"Field"`
	Table       string `json:"Table"`
	Name        string `json:"Name"`
	Label       string `json:"Label"`
	Description string `json:"Description"`
	Type        string `json:"Type"`
	Expr        string `json:"Expr"`
	Expression  string `json:"Expression"`
}

type measurePayloadV1 struct {
	Field           string `json:"Field"`
	Fact            string `json:"Fact"`
	Name            string `json:"Name"`
	Label           string `json:"Label"`
	Description     string `json:"Description"`
	Aggregation     string `json:"Aggregation"`
	InputField      string `json:"InputField"`
	InputExpression string `json:"InputExpression"`
	Empty           string `json:"Empty"`
	Unit            string `json:"Unit"`
	Format          string `json:"Format"`
	Hidden          bool   `json:"Hidden"`
}

type semanticDimensionPayloadV1 struct {
	Name        string                                       `json:"Name"`
	Label       string                                       `json:"Label"`
	Description string                                       `json:"Description"`
	Type        string                                       `json:"Type"`
	Grains      []string                                     `json:"Grains"`
	Timezone    string                                       `json:"Timezone"`
	Calendar    string                                       `json:"Calendar"`
	WeekStart   string                                       `json:"WeekStart"`
	Bindings    map[string]semanticDimensionBindingPayloadV1 `json:"Bindings"`
}

type semanticDimensionBindingPayloadV1 struct {
	Field string   `json:"Field"`
	Path  []string `json:"Path"`
}

type metricPayloadV1 struct {
	Name        string `json:"Name"`
	Label       string `json:"Label"`
	Description string `json:"Description"`
	Expression  string `json:"Expression"`
	Unit        string `json:"Unit"`
	Format      string `json:"Format"`
	Hidden      bool   `json:"Hidden"`
}

type relationshipPayloadV1 struct {
	ID          string `json:"ID"`
	Description string `json:"Description"`
	From        string `json:"From"`
	To          string `json:"To"`
	Cardinality string `json:"Cardinality"`
}

type modelColumnPayloadV1 struct {
	Field       string `json:"Field"`
	Name        string `json:"Name"`
	SourceField string `json:"SourceField"`
	Description string `json:"Description"`
	Type        string `json:"Type"`
}

type schemaPayloadV1 struct {
	Columns []schemaColumnPayloadV1 `json:"Columns"`
}

type schemaColumnPayloadV1 struct {
	Name         string `json:"Name"`
	Ordinal      int    `json:"Ordinal"`
	PhysicalType string `json:"PhysicalType"`
	Nullable     *bool  `json:"Nullable"`
	Default      string `json:"Default"`
	Comment      string `json:"Comment"`
	PrimaryKey   bool   `json:"PrimaryKey"`
}

type dashboardPayloadV1 struct {
	ID            string   `json:"ID"`
	Title         string   `json:"Title"`
	Description   string   `json:"Description"`
	SemanticModel string   `json:"SemanticModel"`
	Tags          []string `json:"Tags"`
}

type pagePayloadV1 struct {
	ID          string       `json:"ID"`
	Title       string       `json:"Title"`
	Description string       `json:"Description"`
	Canvas      pageCanvasV1 `json:"Canvas"`
	Grid        pageGridV1   `json:"Grid"`
}

type workspaceGroupPayloadV1 struct {
	ID          string                          `json:"ID"`
	Name        string                          `json:"Name"`
	Description string                          `json:"Description"`
	Members     []workspaceGroupMemberPayloadV1 `json:"Members"`
}

type workspaceGroupMemberPayloadV1 struct {
	PrincipalID string `json:"PrincipalID"`
	Email       string `json:"Email"`
	DisplayName string `json:"DisplayName"`
}

type workspaceRoleBindingPayloadV1 struct {
	ID      string                               `json:"ID"`
	Name    string                               `json:"Name"`
	Role    string                               `json:"Role"`
	Subject workspaceRoleBindingSubjectPayloadV1 `json:"Subject"`
}

type workspaceRoleBindingSubjectPayloadV1 struct {
	Kind        string `json:"Kind"`
	PrincipalID string `json:"PrincipalID"`
	Email       string `json:"Email"`
	DisplayName string `json:"DisplayName"`
	Group       string `json:"Group"`
}

type refreshPipelinePayloadV1 struct {
	ID            string                             `json:"ID"`
	Name          string                             `json:"Name"`
	SemanticModel string                             `json:"SemanticModel"`
	Schedules     []refreshPipelineSchedulePayloadV1 `json:"Schedules"`
}

type refreshPipelineSchedulePayloadV1 struct {
	Cron     string `json:"Cron"`
	Timezone string `json:"Timezone"`
}
type pageItemPayloadV1 struct {
	ID           string                       `json:"ID"`
	Kind         string                       `json:"Kind"`
	Visual       string                       `json:"Visual"`
	Binding      dashboardfilter.BindingRef   `json:"Binding"`
	Presentation dashboardfilter.Presentation `json:"Presentation"`
	Description  string                       `json:"Description"`
	Placement    pagePlacementV1              `json:"Placement"`
	Title        string                       `json:"Title"`
	Subtitle     string                       `json:"Subtitle"`
	Badges       []string                     `json:"Badges"`
}

type pageCanvasV1 struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type pageGridV1 struct {
	Columns   int `json:"columns"`
	RowHeight int `json:"rowHeight"`
	Gap       int `json:"gap"`
	Padding   int `json:"padding"`
}

type pagePlacementV1 struct {
	Col     int `json:"col"`
	Row     int `json:"row"`
	ColSpan int `json:"colSpan"`
	RowSpan int `json:"rowSpan"`
}
