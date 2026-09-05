package module

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringaccessadapter "github.com/flidai/leapview/internal/dashboard/authoring/accessadapter"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	authoringcompileradapter "github.com/flidai/leapview/internal/dashboard/authoring/compileradapter"
	authoringpostgres "github.com/flidai/leapview/internal/dashboard/authoring/postgres"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
)

// AuthoringApplication is the dashboard module's transport-facing authoring
// surface. The concrete application remains internal to the dashboard
// capability composition boundary.
type AuthoringApplication = authoringapplication.Application

// Authoring identity aliases keep callers on the dashboard module surface.
type DashboardID = authoring.DashboardID
type DraftID = authoring.DraftID
type RevisionID = authoring.RevisionID

// AuthorizeResource is the canonical access decision port needed to authorize
// dashboard authoring operations.
type AuthorizeResource func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error)
type AuthorizeProjectCapability func(context.Context, string, projectgraph.ResourceID, access.Capability) (bool, error)

// AuthoringConfig contains only capability composition ports. Project export
// behavior is injected as a function so dashboard authoring does not import
// the project compiler, and runtime acquisition remains topology-neutral.
type AuthoringConfig struct {
	// Persistence is the opaque native dashboard authority bundle. It is
	// the concrete authoring repository and uses UUIDv7 identity generators.
	Persistence                *NativePersistence
	AuthorizeResource          AuthorizeResource
	AuthorizeProjectCapability AuthorizeProjectCapability
	AcquireRuntime             func(context.Context) (runtimehost.Lease, error)
}

// BuildAuthoring constructs the complete dashboard authoring application and
// its adapters behind the dashboard module surface.
func BuildAuthoring(config AuthoringConfig) (*AuthoringApplication, error) {
	if config.Persistence == nil {
		return nil, fmt.Errorf("dashboard authoring persistence is required")
	}
	if config.Persistence != nil && !config.Persistence.valid() {
		return nil, fmt.Errorf("dashboard authoring native persistence is not configured")
	}
	if config.AuthorizeResource == nil || config.AuthorizeProjectCapability == nil {
		return nil, fmt.Errorf("dashboard authoring resource and project capability authorizers are required")
	}
	if config.AcquireRuntime == nil {
		return nil, fmt.Errorf("dashboard authoring runtime provider is required")
	}
	var repository authoring.Repository
	repository = config.Persistence.authoring
	ids := authoringIDs{
		dashboard: func() (authoring.DashboardID, error) { return authoringpostgres.NewDashboardID() },
		draft:     func() (authoring.DraftID, error) { return authoringpostgres.NewDraftID() },
		revision:  func() (authoring.RevisionID, error) { return authoringpostgres.NewRevisionID() },
	}
	authorizer, err := authoringaccessadapter.New(authoringaccessadapter.Options{
		AuthorizeResource:          authoringaccessadapter.AuthorizeResource(config.AuthorizeResource),
		AuthorizeProjectCapability: authoringaccessadapter.AuthorizeProjectCapability(config.AuthorizeProjectCapability),
	})
	if err != nil {
		return nil, fmt.Errorf("build dashboard authoring access adapter: %w", err)
	}
	compiler, err := authoringcompileradapter.New(authoringcompileradapter.Options{AcquireRuntime: config.AcquireRuntime})
	if err != nil {
		return nil, fmt.Errorf("build dashboard authoring compiler adapter: %w", err)
	}
	service, err := authoringservice.NewService(authoringservice.Options{
		Repository: repository,
		Authorizer: authorizer,
		Compiler:   compiler,
		Now:        func() time.Time { return time.Now().UTC() },
		NewDashboardID: func() (authoring.DashboardID, error) {
			return ids.dashboard()
		},
		NewDraftID: func() (authoring.DraftID, error) {
			return ids.draft()
		},
		NewRevisionID: func() (authoring.RevisionID, error) {
			return ids.revision()
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build dashboard authoring service: %w", err)
	}
	application, err := authoringapplication.New(authoringapplication.Options{
		Authoring:      service,
		Repository:     repository,
		Authorizer:     authorizer,
		Compiler:       compiler,
		AcquireRuntime: config.AcquireRuntime,
	})
	if err != nil {
		return nil, fmt.Errorf("build dashboard authoring application: %w", err)
	}
	return application, nil
}

type authoringIDs struct {
	dashboard func() (authoring.DashboardID, error)
	draft     func() (authoring.DraftID, error)
	revision  func() (authoring.RevisionID, error)
}

func newAuthoringIDs(reader io.Reader) authoringIDs {
	return authoringIDs{
		dashboard: func() (authoring.DashboardID, error) {
			value, err := newAuthoringID("dashboard", reader)
			return authoring.DashboardID(value), err
		},
		draft: func() (authoring.DraftID, error) {
			value, err := newAuthoringID("draft", reader)
			return authoring.DraftID(value), err
		},
		revision: func() (authoring.RevisionID, error) {
			value, err := newAuthoringID("revision", reader)
			return authoring.RevisionID(value), err
		},
	}
}

func newAuthoringID(prefix string, reader io.Reader) (string, error) {
	if reader == nil {
		return "", fmt.Errorf("generate %s id: entropy reader is required", prefix)
	}
	const entropyBytes = 16
	entropy := make([]byte, entropyBytes)
	if _, err := io.ReadFull(reader, entropy); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	const hexChars = "0123456789abcdef"
	encoded := make([]byte, entropyBytes*2)
	for index, value := range entropy {
		encoded[index*2] = hexChars[value>>4]
		encoded[index*2+1] = hexChars[value&0x0f]
	}
	return prefix + "-" + string(encoded), nil
}
