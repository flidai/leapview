// Package cli owns command-line adapters for the Dashboard capability.
package cli

import (
	"context"
	"encoding/json"
	"fmt"

	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/spf13/cobra"
)

type semanticOptions struct {
	remote      cliapi.RemoteOptions
	pagination  cliapi.PaginationOptions
	bodyJSON    string
}

// SemanticModelsCommand constructs the semantic-model inspection and query command.
func SemanticModelsCommand(ctx context.Context, client cliapi.Client) *cobra.Command {
	values := &semanticOptions{}
	parent := &cobra.Command{Use: "semantic-models", Short: "Inspect semantic models"}

	list := semanticRequestCommand(ctx, client, values, "list", "List semantic models", 0, func(ctx context.Context, api *dashboardgen.GenClient, _ []string) (dashboardgen.GenSchemaSemanticModelListResponse, error) {
		response, err := api.ListSemanticModels(ctx, dashboardgen.GenListSemanticModelsClientRequest{
			Params: dashboardgen.GenListSemanticModelsClientParams{
				Limit:     values.pagination.LimitPtr(),
				PageToken: optionalString(values.pagination.PageToken),
			},
		})
		return response.Body, err
	})
	values.pagination.AddFlags(list)
	describe := semanticRequestCommand(ctx, client, values, "describe <model>", "Describe a semantic model", 1, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaSemanticModelDescriptionResponse, error) {
		response, err := api.GetSemanticModel(ctx, dashboardgen.GenGetSemanticModelClientRequest{
			Model: args[0],
		})
		return response.Body, err
	})
	datasets := semanticRequestCommand(ctx, client, values, "datasets <model>", "List semantic model datasets", 1, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaSemanticDatasetListResponse, error) {
		response, err := api.ListSemanticDatasets(ctx, dashboardgen.GenListSemanticDatasetsClientRequest{
			Model: args[0],
			Params: dashboardgen.GenListSemanticDatasetsClientParams{
				Limit:     values.pagination.LimitPtr(),
				PageToken: optionalString(values.pagination.PageToken),
			},
		})
		return response.Body, err
	})
	values.pagination.AddFlags(datasets)
	dataset := semanticRequestCommand(ctx, client, values, "dataset <model> <dataset>", "Describe a semantic model dataset", 2, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaSemanticDatasetResponse, error) {
		response, err := api.GetSemanticDataset(ctx, dashboardgen.GenGetSemanticDatasetClientRequest{
			Model: args[0], Dataset: args[1],
		})
		return response.Body, err
	})
	fields := semanticRequestCommand(ctx, client, values, "fields <model> <dataset>", "List semantic model dataset fields", 2, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaSemanticFieldListResponse, error) {
		response, err := api.ListSemanticFields(ctx, dashboardgen.GenListSemanticFieldsClientRequest{
			Model: args[0], Dataset: args[1],
			Params: dashboardgen.GenListSemanticFieldsClientParams{
				Limit:     values.pagination.LimitPtr(),
				PageToken: optionalString(values.pagination.PageToken),
			},
		})
		return response.Body, err
	})
	values.pagination.AddFlags(fields)

	query := semanticRequestCommand(ctx, client, values, "query <model>", "Query governed semantic data", 1, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaSemanticQueryResponse, error) {
		body, err := bodyJSON[dashboardgen.GenSchemaSemanticQueryRequest](values.bodyJSON)
		if err != nil {
			return dashboardgen.GenSchemaSemanticQueryResponse{}, err
		}
		response, err := api.QuerySemanticModel(ctx, dashboardgen.GenQuerySemanticModelClientRequest{
			Model: args[0], Body: body,
		})
		return response.Body, err
	})
	preview := semanticRequestCommand(ctx, client, values, "preview <model> <dataset>", "Preview semantic model dataset rows", 2, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaSemanticQueryResponse, error) {
		body, err := bodyJSON[dashboardgen.GenSchemaSemanticPreviewRequest](values.bodyJSON)
		if err != nil {
			return dashboardgen.GenSchemaSemanticQueryResponse{}, err
		}
		response, err := api.PreviewSemanticDataset(ctx, dashboardgen.GenPreviewSemanticDatasetClientRequest{
			Model: args[0], Dataset: args[1], Body: body,
		})
		return response.Body, err
	})
	explainQuery := semanticRequestCommand(ctx, client, values, "explain-query <model>", "Explain a governed semantic query", 1, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaSemanticExplainResponse, error) {
		body, err := bodyJSON[dashboardgen.GenSchemaSemanticQueryRequest](values.bodyJSON)
		if err != nil {
			return dashboardgen.GenSchemaSemanticExplainResponse{}, err
		}
		response, err := api.ExplainSemanticModelQuery(ctx, dashboardgen.GenExplainSemanticModelQueryClientRequest{
			Model: args[0], Body: body,
		})
		return response.Body, err
	})
	explainPreview := semanticRequestCommand(ctx, client, values, "explain-preview <model> <dataset>", "Explain a semantic model dataset row preview", 2, func(ctx context.Context, api *dashboardgen.GenClient, args []string) (dashboardgen.GenSchemaSemanticExplainResponse, error) {
		body, err := bodyJSON[dashboardgen.GenSchemaSemanticPreviewRequest](values.bodyJSON)
		if err != nil {
			return dashboardgen.GenSchemaSemanticExplainResponse{}, err
		}
		response, err := api.ExplainSemanticPreview(ctx, dashboardgen.GenExplainSemanticPreviewClientRequest{
			Model: args[0], Dataset: args[1], Body: body,
		})
		return response.Body, err
	})
	for _, command := range []*cobra.Command{query, preview, explainQuery, explainPreview} {
		command.Flags().StringVar(&values.bodyJSON, "body-json", "", "request JSON body")
	}

	parent.AddCommand(list, describe, datasets, dataset, fields, query, preview, explainQuery, explainPreview)
	return parent
}

func semanticRequestCommand[T any](
	ctx context.Context,
	client cliapi.Client,
	values *semanticOptions,
	use string,
	short string,
	exactArgs int,
	execute func(context.Context, *dashboardgen.GenClient, []string) (T, error),
) *cobra.Command {
	command := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(command *cobra.Command, args []string) error {
			if err := values.pagination.Validate(command); err != nil {
				return err
			}
			api, err := semanticClient(ctx, client, values.remote.Credentials())
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

func semanticClient(ctx context.Context, client cliapi.Client, credentials cliapi.Credentials) (*dashboardgen.GenClient, error) {
	if client == nil {
		return nil, fmt.Errorf("dashboard semantic CLI API client is required")
	}
	transport, err := client.Transport(ctx, credentials)
	if err != nil {
		return nil, err
	}
	return dashboardgen.NewGenClient(transport), nil
}

func bodyJSON[T any](raw string) (*T, error) {
	if raw == "" {
		return nil, nil
	}
	var body T
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return nil, fmt.Errorf("body-json: %w", err)
	}
	return &body, nil
}
