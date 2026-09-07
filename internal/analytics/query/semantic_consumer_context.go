package query

import (
	"context"
	"fmt"
)

// SemanticAccessConsumerBinding is the request-time serving identity carried
// alongside a semantic access consumer.  The values are deliberately scalar
// strings so the query package does not depend on project/runtime ownership
// packages; composition roots can copy their authoritative serving identity
// into this value before binding it to a request.
//
// A protected consumer must carry every field, including project/environment
// and the authenticated principal. Public consumers may leave PrincipalID
// empty, but still bind a model and serving scope so a consumer cannot be
// replayed across runtimes.
type SemanticAccessConsumerBinding struct {
	InstanceID  string
	ProjectID   string
	Environment string
	Generation  string
	ModelID     string
	PrincipalID string
}

// SemanticAccessConsumerContext is a process-local capability.  It is not a
// serialized authorization token: materialize uses the exact consumer pointer
// to obtain its request planner and to validate the admitted PlanIR before
// execution.
type SemanticAccessConsumerContext struct {
	Consumer *SemanticAccessConsumer
	Binding  SemanticAccessConsumerBinding
}

type semanticAccessConsumerContextKey struct{}

// WithSemanticAccessConsumer binds one request-bound consumer and its serving
// identity to ctx.  The consumer itself remains the authority; the binding is
// context metadata used by execution boundaries to reject cross-model,
// cross-project, cross-generation, and cross-principal replay.
func WithSemanticAccessConsumer(ctx context.Context, consumer *SemanticAccessConsumer, binding SemanticAccessConsumerBinding) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, semanticAccessConsumerContextKey{}, SemanticAccessConsumerContext{
		Consumer: consumer,
		Binding:  binding,
	})
}

// SemanticAccessConsumerContextFromContext returns the request capability, if
// one was bound.  Validation is intentionally repeated at this boundary so a
// caller cannot pair a consumer with identity metadata from another request.
func SemanticAccessConsumerContextFromContext(ctx context.Context) (SemanticAccessConsumerContext, bool) {
	if ctx == nil {
		return SemanticAccessConsumerContext{}, false
	}
	value, ok := ctx.Value(semanticAccessConsumerContextKey{}).(SemanticAccessConsumerContext)
	if !ok || value.Consumer == nil {
		return SemanticAccessConsumerContext{}, false
	}
	if err := value.Validate(); err != nil {
		return SemanticAccessConsumerContext{}, false
	}
	return value, true
}

// Validate checks the binding against the consumer's private identity.  It is
// exported for execution adapters that need to report a useful denial reason
// while keeping consumer configuration immutable and inaccessible.
func (value SemanticAccessConsumerContext) Validate() error {
	if value.Consumer == nil {
		return fmt.Errorf("semantic access consumer is required")
	}
	binding := value.Binding
	fields := []struct {
		name  string
		value string
	}{
		{name: "instance ID", value: binding.InstanceID},
		{name: "project ID", value: binding.ProjectID},
		{name: "environment", value: binding.Environment},
		{name: "generation", value: binding.Generation},
		{name: "model ID", value: binding.ModelID},
	}
	for _, field := range fields {
		if field.value == "" {
			return fmt.Errorf("semantic access %s is required", field.name)
		}
	}
	if value.Consumer.protected && binding.PrincipalID == "" {
		return fmt.Errorf("semantic access principal ID is required")
	}
	if value.Consumer.protected {
		config := value.Consumer.config
		if binding.InstanceID != config.InstanceID || binding.ProjectID != config.ProjectID || binding.Environment != config.Environment || binding.ModelID != config.ModelID || binding.Generation != config.Generation || binding.PrincipalID != config.PrincipalID {
			return fmt.Errorf("semantic access consumer identity does not match request binding")
		}
	}
	return nil
}
