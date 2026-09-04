package access

import (
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/project/graph"
)

func TestRoleAssignmentInputValidate(t *testing.T) {
	subject, err := NewSubjectRef(SubjectKindPrincipal, "principal-control")
	if err != nil {
		t.Fatal(err)
	}
	valid := RoleAssignmentInput{
		ID:         "00000000-0000-7000-8000-000000000001",
		InstanceID: "instance-control",
		ProjectID:  "project-control",
		Subject:    subject,
		Role:       string(ProjectRoleViewer),
		Name:       "Viewer",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid role assignment rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RoleAssignmentInput)
	}{
		{name: "missing instance", mutate: func(input *RoleAssignmentInput) { input.InstanceID = "" }},
		{name: "invalid assignment id", mutate: func(input *RoleAssignmentInput) { input.ID = "not a UUID" }},
		{name: "negative expected revision", mutate: func(input *RoleAssignmentInput) { input.ExpectedRevision = -1 }},
		{name: "invalid subject", mutate: func(input *RoleAssignmentInput) { input.Subject = SubjectRef{} }},
		{name: "invalid project id", mutate: func(input *RoleAssignmentInput) { input.ProjectID = "project/control" }},
		{name: "invalid role", mutate: func(input *RoleAssignmentInput) { input.Role = "platform_admin" }},
		{name: "control character in name", mutate: func(input *RoleAssignmentInput) { input.Name = "viewer\noperator" }},
		{name: "oversized name", mutate: func(input *RoleAssignmentInput) { input.Name = strings.Repeat("x", 256) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if err := input.Validate(); !errors.Is(err, ErrControlInvalidInput) {
				t.Fatalf("Validate() error = %v, want ErrControlInvalidInput", err)
			}
		})
	}
}

func TestControlGrantInputValidateAgainstGraph(t *testing.T) {
	project := controlDomainProject(t)
	subject, err := NewSubjectRef(SubjectKindGroup, "group-control")
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := NewResourceRef("dashboard-control", graph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	valid := ControlGrantInput{
		ID:         "00000000-0000-7000-8000-000000000002",
		InstanceID: "instance-control",
		Subject:    subject,
		Resource:   dashboard,
		Capability: CapabilityResourceRead,
		Name:       "Dashboard read",
	}
	canonical, err := valid.ValidateAgainstGraph(project)
	if err != nil {
		t.Fatalf("valid grant rejected: %v", err)
	}
	if canonical.Resource() != dashboard || canonical.Subject() != subject || canonical.Capability() != CapabilityResourceRead {
		t.Fatalf("validated grant = %#v, want canonical dashboard/group/read grant", canonical)
	}

	projectRoot, err := NewResourceRef("project-control", graph.KindProject)
	if err != nil {
		t.Fatal(err)
	}
	model, err := NewResourceRef("model-control", graph.KindModel)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*ControlGrantInput)
		want   error
	}{
		{name: "invalid grant id", mutate: func(input *ControlGrantInput) { input.ID = "grant id" }, want: ErrControlInvalidInput},
		{name: "negative expected revision", mutate: func(input *ControlGrantInput) { input.ExpectedRevision = -1 }, want: ErrControlInvalidInput},
		{name: "project mismatch", mutate: func(input *ControlGrantInput) { input.ProjectID = "project-other" }, want: ErrControlTargetConflict},
		{name: "project root target", mutate: func(input *ControlGrantInput) { input.Resource = projectRoot }, want: ErrControlInvalidInput},
		{name: "resource kind mismatch", mutate: func(input *ControlGrantInput) {
			input.Resource = mustControlResourceRef(t, "dashboard-control", graph.KindModel)
		}, want: ErrResourceKindMismatch},
		{name: "unsupported capability", mutate: func(input *ControlGrantInput) { input.Resource, input.Capability = model, CapabilityResourceShare }, want: ErrCapabilityNotAllowed},
		{name: "invalid subject", mutate: func(input *ControlGrantInput) { input.Subject = SubjectRef{} }, want: ErrInvalidCanonicalGrant},
		{name: "invalid name", mutate: func(input *ControlGrantInput) { input.Name = "bad\rname" }, want: ErrControlInvalidInput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			_, err := input.ValidateAgainstGraph(project)
			if !errors.Is(err, test.want) {
				t.Fatalf("ValidateAgainstGraph() error = %v, want %v", err, test.want)
			}
		})
	}
}

func controlDomainProject(t *testing.T) graph.ProjectGraph {
	t.Helper()
	project, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "project-control", Kind: graph.KindProject, Name: "Control"},
		{ID: "dashboard-control", Kind: graph.KindDashboard, Name: "Dashboard"},
		{ID: "model-control", Kind: graph.KindModel, Name: "Model"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func mustControlResourceRef(t *testing.T, id graph.ResourceID, kind graph.Kind) ResourceRef {
	t.Helper()
	resource, err := NewResourceRef(id, kind)
	if err != nil {
		t.Fatal(err)
	}
	return resource
}
