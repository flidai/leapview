package compiler

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"gopkg.in/yaml.v3"
)

type resourceEnvelope struct {
	APIVersion string    `yaml:"apiVersion"`
	Kind       string    `yaml:"kind"`
	Metadata   metadata  `yaml:"metadata"`
	Spec       yaml.Node `yaml:"spec"`
}

type metadata struct {
	Name        string   `yaml:"name"`
	Workspace   string   `yaml:"workspace"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Owner       string   `yaml:"owner"`
	Tags        []string `yaml:"tags"`
}

type projectResource struct {
	Connections includeList `yaml:"connections"`
	Sources     includeList `yaml:"sources"`
	Workspaces  includeList `yaml:"workspaces"`
}

type includeList struct {
	Include []string `yaml:"include"`
}

type workspaceSpec struct {
	Uses             workspaceUses `yaml:"uses"`
	Models           includeList   `yaml:"models"`
	SemanticModels   includeList   `yaml:"semanticModels"`
	Dashboards       includeList   `yaml:"dashboards"`
	Publications     includeList   `yaml:"publications"`
	Access           includeList   `yaml:"access"`
	RefreshPipelines includeList   `yaml:"refreshPipelines"`
}

type dashboardPublicationSpec struct {
	Dashboard   string                            `yaml:"dashboard"`
	DefaultPage string                            `yaml:"defaultPage"`
	Embedding   dashboardPublicationEmbeddingSpec `yaml:"embedding"`
}

type dashboardPublicationEmbeddingSpec struct {
	AllowedOrigins []string `yaml:"allowedOrigins"`
}

type workspaceUses struct {
	Sources []string `yaml:"sources"`
}

type sourceSpec struct {
	Format      string                        `yaml:"format"`
	Description string                        `yaml:"description"`
	Path        string                        `yaml:"path"`
	Connection  string                        `yaml:"connection"`
	Object      string                        `yaml:"object"`
	Options     map[string]any                `yaml:"options"`
	Fields      map[string]projectSourceField `yaml:"fields"`
}

type projectSourceField struct {
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
}

type projectSemanticModelSpec struct {
	Tables        []string                                   `yaml:"tables"`
	Relationships []semanticmodel.Relationship               `yaml:"relationships"`
	Dimensions    map[string]semanticmodel.SemanticDimension `yaml:"dimensions"`
	Measures      map[string]semanticmodel.MetricMeasure     `yaml:"measures"`
	Metrics       map[string]semanticmodel.Metric            `yaml:"metrics"`
}

type dashboardSpec struct {
	Appearance        dashboardappearance.Patch                            `yaml:"appearance"`
	SemanticModel     string                                               `yaml:"semanticModel"`
	Filters           map[string]dashboardfilter.Definition                `yaml:"filters"`
	FilterBindings    map[string]dashboardfilter.Binding                   `yaml:"filter_bindings"`
	FilterApplication dashboardfilter.ApplicationPolicy                    `yaml:"filter_application"`
	Visuals           map[string]dashboardauthoring.AuthoringVisualization `yaml:"visuals"`
	Pages             []projectDashboardPage                               `yaml:"pages"`
}

type projectModelTableSpec struct {
	Source      string                               `yaml:"source"`
	Sources     []string                             `yaml:"sources"`
	SourceReads map[string][]string                  `yaml:"sourceReads"`
	SQL         string                               `yaml:"sql"`
	Transform   semanticmodel.Transform              `yaml:"transform"`
	Columns     map[string]semanticmodel.ModelColumn `yaml:"columns"`
	PrimaryKey  string                               `yaml:"primaryKey"`
	Grain       string                               `yaml:"grain"`
	Fields      map[string]projectModelField         `yaml:"fields"`
	Description string                               `yaml:"description"`
}

type projectModelField struct {
	Label       string `yaml:"label"`
	Description string `yaml:"description"`
	Expr        string `yaml:"expr"`
	Expression  string `yaml:"expression"`
	Type        string `yaml:"type"`
}

type projectDashboardPage struct {
	ID             string                             `yaml:"id"`
	Title          string                             `yaml:"title"`
	Description    string                             `yaml:"description"`
	Canvas         dashboard.PageCanvas               `yaml:"canvas"`
	Grid           dashboard.PageGrid                 `yaml:"grid"`
	FilterBindings map[string]dashboardfilter.Binding `yaml:"filter_bindings"`
	Components     []dashboard.PageVisual             `yaml:"components"`
}

type workspaceGroupSpec struct {
	Description string                     `yaml:"description"`
	Members     []workspaceGroupMemberSpec `yaml:"members"`
}

type workspaceGroupMemberSpec struct {
	PrincipalID string `yaml:"principalId"`
	Email       string `yaml:"email"`
	DisplayName string `yaml:"displayName"`
}

type workspaceRoleBindingSpec struct {
	Role    string                          `yaml:"role"`
	Subject workspaceRoleBindingSubjectSpec `yaml:"subject"`
}

type workspaceRoleBindingSubjectSpec struct {
	Kind        string `yaml:"kind"`
	PrincipalID string `yaml:"principalId"`
	Email       string `yaml:"email"`
	DisplayName string `yaml:"displayName"`
	Group       string `yaml:"group"`
	Publication string `yaml:"publication"`
}

type workspaceSecurableObjectSpec struct {
	Type string `yaml:"type"`
	ID   string `yaml:"id"`
}

type workspaceGrantSpec struct {
	Object    workspaceSecurableObjectSpec    `yaml:"object"`
	Subject   workspaceRoleBindingSubjectSpec `yaml:"subject"`
	Privilege string                          `yaml:"privilege"`
}

type workspaceDataPolicySpec struct {
	Object     workspaceSecurableObjectSpec    `yaml:"object"`
	Subject    workspaceRoleBindingSubjectSpec `yaml:"subject"`
	PolicyType string                          `yaml:"policyType"`
	Expression yaml.Node                       `yaml:"expression"`
}
type refreshPipelineSpec struct {
	SemanticModel string                `yaml:"semanticModel"`
	On            refreshPipelineOnSpec `yaml:"on"`
}

type refreshPipelineOnSpec struct {
	Schedule []refreshPipelineScheduleSpec `yaml:"schedule"`
}

type refreshPipelineScheduleSpec struct {
	Cron     string `yaml:"cron"`
	Timezone string `yaml:"timezone"`
}
