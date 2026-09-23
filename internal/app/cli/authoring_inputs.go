package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"strings"

	manageddatacli "github.com/flidai/leapview/internal/manageddata/cli"
	"github.com/flidai/leapview/internal/manageddata/localplan"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/project/developmentinput"
)

type localDevelopmentInputTarget struct {
	ProjectRoot string
	ProjectID   string
	Environment string
	Origin      string
	Output      io.Writer
}

type developmentInputStagingDependencies struct {
	list       func(string) ([]string, error)
	resolve    func(string, string) (manageddatacli.DevelopmentInput, error)
	plan       func(context.Context, localplan.Request) (localplan.Result, error)
	sync       func(context.Context, manageddatacli.SyncRequest) error
	httpClient *http.Client
}

type plannedDevelopmentInput struct {
	name      string
	selection manageddatacli.DevelopmentInput
	plan      localplan.Result
}

func stageDeclaredDevelopmentInputs(ctx context.Context, credentials cliapi.Credentials, local localDevelopmentSession) error {
	planner := localplan.NewService(loadManagedDataPlanCatalog)
	return stageDeclaredDevelopmentInputsWithDependencies(ctx, credentials, localDevelopmentInputTarget{
		ProjectRoot: local.state.Checkout.CanonicalRoot,
		ProjectID:   local.state.Authority.ProjectUID,
		Environment: local.state.Authority.Environment,
		Origin:      local.state.Network.URL,
		Output:      local.output,
	}, developmentInputStagingDependencies{
		list: developmentinput.Names, resolve: resolveDevelopmentInput,
		plan: planner.Plan, sync: manageddatacli.RunSync, httpClient: http.DefaultClient,
	})
}

func stageDeclaredDevelopmentInputsWithDependencies(
	ctx context.Context,
	credentials cliapi.Credentials,
	target localDevelopmentInputTarget,
	dependencies developmentInputStagingDependencies,
) error {
	if ctx == nil {
		return errors.New("development input staging requires context")
	}
	root := strings.TrimSpace(target.ProjectRoot)
	projectID := strings.TrimSpace(target.ProjectID)
	environment := strings.TrimSpace(target.Environment)
	origin := strings.TrimRight(strings.TrimSpace(target.Origin), "/")
	if root == "" || projectID == "" || origin == "" {
		return errors.New("local development input target identity is incomplete")
	}
	if environment != "dev" {
		return fmt.Errorf("declared development inputs may only be staged to the verified dev environment, got %q", environment)
	}
	if strings.TrimSpace(credentials.ProjectID) != projectID {
		return errors.New("local development input Project identity does not match the authenticated target")
	}
	credentialOrigin := strings.TrimRight(strings.TrimSpace(credentials.CanonicalOrigin), "/")
	if credentialOrigin == "" {
		credentialOrigin = strings.TrimRight(strings.TrimSpace(credentials.Target), "/")
	}
	if credentialOrigin != origin {
		return errors.New("local development input origin does not match the authenticated target")
	}
	if strings.TrimSpace(credentials.Token) == "" {
		return errors.New("local development input staging requires an authenticated authoring session")
	}
	if dependencies.list == nil || dependencies.resolve == nil || dependencies.plan == nil || dependencies.sync == nil {
		return errors.New("development input staging is not configured")
	}
	names, err := dependencies.list(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	sort.Strings(names)
	planned := make([]plannedDevelopmentInput, 0, len(names))
	connections := make(map[string]string, len(names))
	for _, name := range names {
		selection, err := dependencies.resolve(root, name)
		if err != nil {
			return fmt.Errorf("development input %q: %w", name, err)
		}
		if previous, exists := connections[selection.Connection]; exists {
			return fmt.Errorf("development inputs %q and %q select the same connection %q", previous, name, selection.Connection)
		}
		connections[selection.Connection] = name
		plan, err := dependencies.plan(ctx, localplan.Request{
			SourceRoot: selection.SourceRoot,
			Connection: selection.Connection,
			From:       selection.From,
		})
		if err != nil {
			return fmt.Errorf("plan development input %q: %w", name, err)
		}
		if strings.TrimSpace(selection.Name) != name ||
			plan.ConnectionName != selection.Connection ||
			plan.Root != selection.From ||
			plan.Manifest.RevisionID() == "" ||
			plan.Manifest.RevisionID() != selection.Manifest.RevisionID() {
			return fmt.Errorf("development input %q changed after validation; restart leapview dev", name)
		}
		planned = append(planned, plannedDevelopmentInput{name: name, selection: selection, plan: plan})
	}
	output := target.Output
	if output == nil {
		output = io.Discard
	}
	httpClient := dependencies.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	for _, input := range planned {
		revisionID := input.plan.Manifest.RevisionID()
		if _, err := fmt.Fprintf(output, "staging declared development input %s (%s)\n", input.name, revisionID); err != nil {
			return err
		}
		if err := dependencies.sync(ctx, manageddatacli.SyncRequest{
			SourceRoot:   input.selection.SourceRoot,
			ProjectID:    projectID,
			Connection:   input.selection.Connection,
			ConnectionID: input.plan.Connection,
			Root:         input.plan.Root,
			Target:       origin,
			Token:        credentials.Token,
			Plan:         input.plan,
			Out:          output,
			HTTPClient:   httpClient,
			Format:       "text",
		}); err != nil {
			return fmt.Errorf("stage development input %q: %w", input.name, err)
		}
	}
	return nil
}
