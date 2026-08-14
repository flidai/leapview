// Package cli owns command-line adapters for the Dashboard capability.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/spf13/cobra"
)

type options struct {
	remote          cliapi.RemoteOptions
	pagination      cliapi.PaginationOptions
	workspaceID     string
	count           int
	filterStateJSON string
}

// Command constructs the dashboard inspection and query command.
func Command(ctx context.Context, client cliapi.Client) *cobra.Command {
	values := &options{}
	parent := &cobra.Command{Use: "dashboards", Short: "Inspect dashboards"}
	parent.PersistentFlags().StringVar(&values.workspaceID, "workspace", "", "workspace id")

	list := requestCommand(ctx, client, values, "list", "List dashboards", 0, func(ctx context.Context, api *dashboardgen.GenClient, _ []string) (dashboardgen.GenSchemaDashboardListResponse, error) {
		response, err := api.ListDashboards(ctx, dashboardgen.GenListDashboardsClientRequest{
			Workspace: values.workspaceID,
			Params: dashboardgen.GenListDashboardsClientParams{
				Limit:     values.pagination.LimitPtr(),
				PageToken: optionalString(values.pagination.PageToken),
			},
		})
		return response.Body, err
	})
	values.pagination.AddFlags(list)

	describe := requestCommand(ctx, client, values, "describe <dashboard>", "Describe a dashboard", 1, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaDashboardManifestResponse, error) {
		response, err := api.GetDashboard(ctx, dashboardgen.GenGetDashboardClientRequest{
			Workspace: values.workspaceID, Dashboard: args[0],
		})
		return response.Body, err
	})
	page := requestCommand(ctx, client, values, "page <dashboard> <page>", "Describe a dashboard page", 2, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaDashboardPageResponse, error) {
		response, err := api.GetDashboardPage(ctx, dashboardgen.GenGetDashboardPageClientRequest{
			Workspace: values.workspaceID, Dashboard: args[0], Page: args[1],
		})
		return response.Body, err
	})
	visual := requestCommand(ctx, client, values, "visual <dashboard> <page> <visual>", "Describe a dashboard visual", 3, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaDashboardVisualDescribeResponse, error) {
		response, err := api.GetDashboardVisual(ctx, dashboardgen.GenGetDashboardVisualClientRequest{
			Workspace: values.workspaceID, Dashboard: args[0], Page: args[1], Visual: args[2],
		})
		return response.Body, err
	})
	filter := requestCommand(ctx, client, values, "filter <dashboard> <page> <filter>", "Describe a compiled dashboard filter binding", 3, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaDashboardFilterDescribeResponse, error) {
		response, err := api.GetDashboardFilter(ctx, dashboardgen.GenGetDashboardFilterClientRequest{
			Workspace: values.workspaceID, Dashboard: args[0], Page: args[1], Filter: args[2],
		})
		return response.Body, err
	})
	visualData := requestCommand(ctx, client, values, "visual-data <dashboard> <page> <visual>", "Query dashboard visual data", 3, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaVisualizationEnvelope, error) {
		body, err := visualQueryBody(values.count, values.filterStateJSON)
		if err != nil {
			return dashboardgen.GenSchemaVisualizationEnvelope{}, err
		}
		response, err := api.QueryDashboardVisualData(ctx, dashboardgen.GenQueryDashboardVisualDataClientRequest{
			Workspace: values.workspaceID, Dashboard: args[0], Page: args[1], Visual: args[2], Body: body,
		})
		return response.Body, err
	})
	visualData.Flags().StringVar(&values.filterStateJSON, "filter-state-json", "", "versioned dashboard filter state JSON")
	visualData.Flags().IntVar(&values.count, "count", 0, "row count for table, matrix, or pivot visuals")

	queryPage := requestCommand(ctx, client, values, "query-page <dashboard> <page>", "Query a dashboard page", 2, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaDashboardPageQueryResponse, error) {
		body, err := filterStateBody(values.filterStateJSON)
		if err != nil {
			return dashboardgen.GenSchemaDashboardPageQueryResponse{}, err
		}
		response, err := api.QueryDashboardPage(ctx, dashboardgen.GenQueryDashboardPageClientRequest{
			Workspace: values.workspaceID, Dashboard: args[0], Page: args[1], Body: body,
		})
		return response.Body, err
	})
	queryPage.Flags().StringVar(&values.filterStateJSON, "filter-state-json", "", "versioned dashboard filter state JSON")

	filterOptions := requestCommand(ctx, client, values, "filter-options <dashboard> <page> <filter>", "List dashboard filter options", 3, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaDashboardFilterOptionListResponse, error) {
		body, err := filterStateBody(values.filterStateJSON)
		if err != nil {
			return dashboardgen.GenSchemaDashboardFilterOptionListResponse{}, err
		}
		response, err := api.ListDashboardFilterValues(ctx, dashboardgen.GenListDashboardFilterValuesClientRequest{
			Workspace: values.workspaceID, Dashboard: args[0], Page: args[1], Filter: args[2], Body: body,
			Params: dashboardgen.GenListDashboardFilterValuesClientParams{
				Limit:     values.pagination.LimitPtr(),
				PageToken: optionalString(values.pagination.PageToken),
			},
		})
		return response.Body, err
	})
	values.pagination.AddFlags(filterOptions)
	filterOptions.Flags().StringVar(&values.filterStateJSON, "filter-state-json", "", "versioned dashboard filter state JSON")

	parent.AddCommand(list, describe, page, visual, filter, visualData, queryPage, filterOptions)
	return parent
}

func requestCommand[T any](
	ctx context.Context,
	client cliapi.Client,
	values *options,
	use string,
	short string,
	exactArgs int,
	execute func(context.Context, *dashboardgen.GenClient, []string) (T, error),
) *cobra.Command {
	command := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireWorkspace(values.workspaceID); err != nil {
				return err
			}
			if err := values.pagination.Validate(command); err != nil {
				return err
			}
			api, err := dashboardClient(ctx, client, values.remote.Credentials())
			if err != nil {
				return err
			}
			response, err := execute(ctx, api, args)
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(response)
		},
	}
	if exactArgs > 0 {
		command.Args = cobra.ExactArgs(exactArgs)
	}
	values.remote.AddFlags(command)
	return command
}

func requireWorkspace(workspaceID string) error {
	if strings.TrimSpace(workspaceID) == "" {
		return fmt.Errorf("--workspace is required")
	}
	return nil
}

func dashboardClient(ctx context.Context, client cliapi.Client, credentials cliapi.Credentials) (*dashboardgen.GenClient, error) {
	if client == nil {
		return nil, fmt.Errorf("dashboard CLI API client is required")
	}
	transport, err := client.Transport(ctx, credentials)
	if err != nil {
		return nil, err
	}
	return dashboardgen.NewGenClient(transport), nil
}

func filterStateBody(raw string) (*dashboardgen.GenSchemaDashboardPageQueryRequest, error) {
	if raw == "" {
		return nil, nil
	}
	filterState, err := decodeFilterState(raw)
	if err != nil {
		return nil, fmt.Errorf("filter-state-json: %w", err)
	}
	return &dashboardgen.GenSchemaDashboardPageQueryRequest{FilterState: &filterState}, nil
}

func visualQueryBody(count int, rawFilterState string) (*dashboardgen.GenSchemaDashboardVisualQueryRequest, error) {
	body := dashboardgen.GenSchemaDashboardVisualQueryRequest{}
	if count > 0 {
		limit := int32(count)
		body.Limit = &limit
	}
	if rawFilterState != "" {
		filterState, err := decodeFilterState(rawFilterState)
		if err != nil {
			return nil, fmt.Errorf("filter-state-json: %w", err)
		}
		body.FilterState = &filterState
	}
	if body.Limit == nil && body.FilterState == nil {
		return nil, nil
	}
	return &body, nil
}

func decodeFilterState(raw string) (dashboardgen.GenSchemaDashboardAppliedFilterInput, error) {
	var out dashboardgen.GenSchemaDashboardAppliedFilterInput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return dashboardgen.GenSchemaDashboardAppliedFilterInput{}, err
	}
	if out.Version == "" {
		return dashboardgen.GenSchemaDashboardAppliedFilterInput{}, fmt.Errorf("must include a filter state version")
	}
	return out, nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
