package query

import "testing"

func TestProtectedConsumerRequiresProjectAndEnvironmentIdentity(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	planner, err := NewCompiledPlanner(semanticAccessTestModel(t))
	if err != nil {
		t.Fatal(err)
	}
	base := SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod",
		ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID,
		Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
			return snapshot, authority, nil
		},
	}
	for _, test := range []struct {
		name string
		edit func(*SemanticAccessConsumerConfig)
	}{
		{name: "missing project", edit: func(config *SemanticAccessConsumerConfig) { config.ProjectID = "" }},
		{name: "missing environment", edit: func(config *SemanticAccessConsumerConfig) { config.Environment = "" }},
		{name: "noncanonical project", edit: func(config *SemanticAccessConsumerConfig) { config.ProjectID = " project:test" }},
		{name: "noncanonical environment", edit: func(config *SemanticAccessConsumerConfig) { config.Environment = "prod " }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.edit(&config)
			if _, err := NewSemanticAccessConsumer(planner, config); err == nil {
				t.Fatal("protected consumer accepted incomplete identity")
			}
		})
	}
}

func TestSemanticAccessConsumerBindingRejectsProjectAndEnvironmentRelabeling(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	consumer := newDiscoveryConsumer(t, snapshot, authority)
	base := SemanticAccessConsumerBinding{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod",
		Generation: "generation-1", ModelID: "semantic-model:test", PrincipalID: snapshot.PrincipalID,
	}
	for _, test := range []struct {
		name string
		edit func(*SemanticAccessConsumerBinding)
	}{
		{name: "project", edit: func(binding *SemanticAccessConsumerBinding) { binding.ProjectID = "project:other" }},
		{name: "environment", edit: func(binding *SemanticAccessConsumerBinding) { binding.Environment = "staging" }},
		{name: "missing project", edit: func(binding *SemanticAccessConsumerBinding) { binding.ProjectID = "" }},
		{name: "missing environment", edit: func(binding *SemanticAccessConsumerBinding) { binding.Environment = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding := base
			test.edit(&binding)
			ctx := WithSemanticAccessConsumer(t.Context(), consumer, binding)
			if _, ok := SemanticAccessConsumerContextFromContext(ctx); ok {
				t.Fatal("relabelled consumer binding was accepted")
			}
			if err := (SemanticAccessConsumerContext{Consumer: consumer, Binding: binding}).Validate(); err == nil {
				t.Fatal("relabelled consumer binding validated")
			}
		})
	}
}
