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
	ID            string   `yaml:"id"`
	Name          string   `yaml:"name"`
	Title         string   `yaml:"title"`
	DisplayName   string   `yaml:"displayName"`
	Description   string   `yaml:"description"`
	Owner         string   `yaml:"owner"`
	Tags          []string `yaml:"tags"`
	Domain        string   `yaml:"domain"`
	Documentation string   `yaml:"documentation"`
	Provenance    struct {
		Origin string `yaml:"origin"`
		Path   string `yaml:"path"`
		Source string `yaml:"source"`
	} `yaml:"provenance"`
}

type projectResource struct {
	Connections    includeList `yaml:"connections"`
	Sources        includeList `yaml:"sources"`
	Models         includeList `yaml:"models"`
	SemanticModels includeList `yaml:"semanticModels"`
	Pipelines      includeList `yaml:"pipelines"`
	Dashboards     includeList `yaml:"dashboards"`
	Access         includeList `yaml:"access"`
	Publications   includeList `yaml:"publications"`
}

type includeList struct {
	Include []string `yaml:"include"`
}

type dashboardPublicationSpec struct {
	Dashboard   string                            `yaml:"dashboard"`
	DefaultPage string                            `yaml:"defaultPage"`
	Embedding   dashboardPublicationEmbeddingSpec `yaml:"embedding"`
}

type dashboardPublicationEmbeddingSpec struct {
	AllowedOrigins []string `yaml:"allowedOrigins"`
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

type projectGroupSpec struct {
	Description string                   `yaml:"description"`
	Members     []projectGroupMemberSpec `yaml:"members"`
}

type projectGroupMemberSpec struct {
	PrincipalID string `yaml:"principalId"`
	Email       string `yaml:"email"`
	DisplayName string `yaml:"displayName"`
}

type projectRoleBindingSpec struct {
	Role    string                        `yaml:"role"`
	Subject projectRoleBindingSubjectSpec `yaml:"subject"`
}

type projectRoleBindingSubjectSpec struct {
	Kind        string `yaml:"kind"`
	PrincipalID string `yaml:"principalId"`
	Email       string `yaml:"email"`
	DisplayName string `yaml:"displayName"`
	Group       string `yaml:"group"`
	Publication string `yaml:"publication"`
}

type projectGrantSpec struct {
	Object     projectResourceRefSpec        `yaml:"object"`
	Subject    projectRoleBindingSubjectSpec `yaml:"subject"`
	Privilege  string                        `yaml:"privilege"`
	Capability string                        `yaml:"capability"`
}

type projectDataPolicySpec struct {
	Object     projectDataPolicyTargetSpec   `yaml:"object"`
	Subject    projectRoleBindingSubjectSpec `yaml:"subject"`
	PolicyType string                        `yaml:"policyType"`
	Expression yaml.Node                     `yaml:"expression"`
}
type projectResourceRefSpec struct {
	Kind string `yaml:"kind"`
	ID   string `yaml:"id"`
}
type projectDataPolicyTargetSpec struct {
	Kind string `yaml:"kind"`
	ID   string `yaml:"id"`
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
