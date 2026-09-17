package module

import (
	"context"
	"net/http"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// APIGenResourceResolver resolves the exact graph resources named by one
// generated route. Resolvers must return canonical ResourceRefs; they may not
// infer a project or resource from an untyped fallback.
type APIGenResourceResolver func(*http.Request, projectgraph.ResourceID) []access.ResourceRef

// APIGenInstanceResolver resolves the immutable instance audience for
// instance-scoped typed operations. It deliberately returns only the opaque
// instance identity; instance actions must not be represented as graph
// resources or project-scoped pairs.
type APIGenInstanceResolver func(*http.Request) string

// APIGenDeliveryAuthorizer handles target-owned delivery routes whose public
// project path is an authorization scope, not a graph ResourceRef.
type APIGenDeliveryAuthorizer func(context.Context, *http.Request, string, string, projectgraph.ResourceID, access.Capability) (bool, error)

type APIGenResourceResolvers struct {
	Dashboard     APIGenResourceResolver
	SemanticModel APIGenResourceResolver
	Connection    APIGenResourceResolver
	Source        APIGenResourceResolver
	Model         APIGenResourceResolver
	Pipeline      APIGenResourceResolver
	Project       APIGenResourceResolver
	Instance      APIGenInstanceResolver
	Delivery      APIGenDeliveryAuthorizer
}

type APIGenOperationContract struct {
	OperationID string
	Kind        string
	Namespace   string
	Method      string
	Path        string
	Protected   bool
	AuthzMode   string
	Action      string
	Resolver    string
	Manual      bool
	Command     *APIGenCommandContract
	Extensions  map[string]any
}

// APIGenCommandContract is the authorization subset of APIGen's normalized
// command descriptor. Privilege is retained as the generated field name at
// this boundary while its value is required to be a canonical capability.
type APIGenCommandContract struct {
	Owner               string
	AuthzMode           string
	Privilege           string
	Target              *APIGenCommandTarget
	Idempotency         string
	Concurrency         string
	AdditionalExposures []string
	UIActionID          string
}

type APIGenCommandTarget struct {
	Parameter string
	Type      string
}
