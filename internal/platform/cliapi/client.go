// Package cliapi defines capability-agnostic facilities used by CLI adapters
// to call the application's generated HTTP API.
package cliapi

import (
	"context"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
)

// Credentials contain the optional target and token supplied by a command.
// A Client resolves empty values through application-owned configuration and
// may attach the validated canonical origin advertised by the target.
type Credentials struct {
	Target          string
	Token           string
	CanonicalOrigin string
	// DeliveryMode is negotiated from the target's composed capabilities. It
	// is intentionally carried with resolved credentials so project-dev remote
	// factories cannot infer transport support from an implementation type.
	DeliveryMode DeliveryMode
}

// DeliveryMode identifies the target-owned project authoring transport.
type DeliveryMode string

const (
	DeliveryModeNativePostgres DeliveryMode = "native_postgres"
)

// Client is the narrow application-facing port used by capability CLI
// adapters. Implementations own credential and transport configuration.
type Client interface {
	Resolve(context.Context, Credentials) (Credentials, error)
	Environment(context.Context, Credentials, string) (string, error)
	Transport(context.Context, Credentials) (apigenclient.Transport, error)
}
