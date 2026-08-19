package manifest

import (
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/dashboard/publication"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

// RuntimeSourceAlias returns the stable SQL/runtime identifier used for an
// authored source name. Compiler lowering and target-side observation evidence
// must use this one mapping so canonical resource IDs cannot lose their live
// schema evidence at the delivery boundary.
func RuntimeSourceAlias(sourceName string) string {
	var builder strings.Builder
	for index, char := range sourceName {
		valid := char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || index > 0 && char >= '0' && char <= '9'
		if valid {
			builder.WriteRune(char)
			continue
		}
		builder.WriteByte('_')
	}
	out := builder.String()
	if out == "" || out[0] >= '0' && out[0] <= '9' {
		out = "source_" + out
	}
	return out
}

// DashboardSourceMetadata retains descriptive authored identity alongside a
// normalized dashboard document. It carries no serving namespace.
type DashboardSourceMetadata struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
	// Domain is descriptive authored metadata. It is never consulted for
	// serving, authorization, or any other access decision.
	Domain string   `json:"domain,omitempty"`
	Tags   []string `json:"tags,omitempty"`
}

type DashboardSource struct {
	Document document.DashboardDocument `json:"document"`
	Metadata DashboardSourceMetadata    `json:"metadata"`
	Path     string                     `json:"path"`
}

// Project is the mutable, project-wide compiler assembly contract. It is
// intentionally independent of artifact/bundle storage and contains no
// serving namespace. Compiler code may populate this value while the
// artifact capability owns immutable serialization of it.
type Project struct {
	ID                   string                                    `json:"id"`
	Name                 string                                    `json:"name"`
	Title                string                                    `json:"title,omitempty"`
	Description          string                                    `json:"description,omitempty"`
	Connections          map[string]semanticmodel.Connection       `json:"connections,omitempty"`
	Sources              map[string]semanticmodel.Source           `json:"sources,omitempty"`
	Models               map[string]semanticmodel.Table            `json:"models,omitempty"`
	SemanticModels       map[string]*semanticmodel.Model           `json:"semanticModels,omitempty"`
	DashboardDefinitions map[string]dashboarddefinition.Definition `json:"dashboardDefinitions,omitempty"`
	DashboardSources     map[string]DashboardSource                `json:"dashboardSources,omitempty"`
	Publications         map[string]publication.Definition         `json:"publications,omitempty"`
	Access               AccessPolicy                              `json:"access,omitempty"`
	RefreshPipelines     map[string]refreshschedule.Definition     `json:"refreshPipelines,omitempty"`
	NameIndex            NameIndex                                 `json:"nameIndex,omitempty"`
	ResourceFiles        map[string]string                         `json:"resourceFiles,omitempty"`
}

// NameIndex preserves authored symbolic names while canonical maps remain
// keyed by stable resource IDs.
type NameIndex struct {
	Connections    map[string]string `json:"connections,omitempty"`
	Sources        map[string]string `json:"sources,omitempty"`
	Models         map[string]string `json:"models,omitempty"`
	SemanticModels map[string]string `json:"semanticModels,omitempty"`
	Dashboards     map[string]string `json:"dashboards,omitempty"`
	Pipelines      map[string]string `json:"pipelines,omitempty"`
	Publications   map[string]string `json:"publications,omitempty"`
}

type AccessPolicy struct {
	Groups       map[string]Group       `json:"groups,omitempty"`
	RoleBindings map[string]RoleBinding `json:"roleBindings,omitempty"`
	Grants       map[string]Grant       `json:"grants,omitempty"`
	DataPolicies map[string]DataPolicy  `json:"dataPolicies,omitempty"`
}

type Group struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Members     []GroupMember `json:"members,omitempty"`
}
type GroupMember struct {
	PrincipalID string `json:"principalId,omitempty"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}
type Subject struct {
	Kind        string `json:"kind"`
	PrincipalID string `json:"principalId,omitempty"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Group       string `json:"group,omitempty"`
	Publication string `json:"publication,omitempty"`
}
type RoleBinding struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Role    string  `json:"role"`
	Subject Subject `json:"subject"`
}
type SecurableRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}
type Grant struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Object     SecurableRef `json:"object"`
	Subject    Subject      `json:"subject"`
	Capability string       `json:"capability"`
}
type DataPolicy struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Object         SecurableRef `json:"object"`
	Subject        Subject      `json:"subject,omitempty"`
	PolicyType     string       `json:"policyType"`
	ExpressionJSON string       `json:"expressionJson"`
}
