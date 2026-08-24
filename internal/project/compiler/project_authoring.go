package compiler

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"gopkg.in/yaml.v3"
)

type resourceEnvelope struct {
	APIVersion string                   `yaml:"apiVersion"`
	Kind       string                   `yaml:"kind"`
	Metadata   metadata                 `yaml:"metadata"`
	AIContext  *semanticmodel.AIContext `yaml:"aiContext"`
	Spec       yaml.Node                `yaml:"spec"`
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

type projectSemanticModelSpec struct {
	Datasets      map[string]semanticmodel.SemanticDatasetSpec   `yaml:"datasets"`
	Relationships map[string]semanticmodel.RelationshipSpec      `yaml:"relationships"`
	Dimensions    map[string]semanticmodel.SemanticDimensionSpec `yaml:"dimensions"`
	Filters       map[string]semanticmodel.SemanticFilterSpec    `yaml:"filters"`
	Metrics       map[string]semanticmodel.SemanticMetricSpec    `yaml:"metrics"`
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
