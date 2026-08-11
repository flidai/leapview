// Package cli owns the Agent capability's command behavior.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/spf13/cobra"
)

// Dependencies are application facilities required by the Agent CLI adapter.
type Dependencies struct {
	Client     cliapi.Client
	Operations func() []agenttools.APIGenOperation
}

type options struct {
	target       string
	token        string
	conversation string
	jsonOutput   bool
	pagination   cliapi.PaginationOptions
}

// Command constructs the Agent command tree without depending on application
// globals or process startup.
func Command(ctx context.Context, dependencies Dependencies) *cobra.Command {
	values := &options{}
	parent := &cobra.Command{Use: "agent", Short: "Use the LeapView read-only agent"}
	ask := &cobra.Command{
		Use:   "ask [question]",
		Short: "Ask the LeapView read-only agent a question",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAsk(ctx, dependencies.Client, values, args[0], cmd.OutOrStdout())
		},
	}
	ask.Flags().StringVar(&values.target, "target", "", "LeapView server URL")
	ask.Flags().StringVar(&values.token, "token", "", "API token")
	ask.Flags().StringVar(&values.conversation, "conversation", "", "existing agent conversation id")
	ask.Flags().BoolVar(&values.jsonOutput, "json", false, "print JSON response")

	conversations := &cobra.Command{
		Use:   "conversations",
		Short: "List agent conversations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := values.pagination.Validate(cmd); err != nil {
				return err
			}
			return runConversations(ctx, dependencies.Client, values, cmd.OutOrStdout())
		},
	}
	conversations.Flags().StringVar(&values.target, "target", "", "LeapView server URL")
	conversations.Flags().StringVar(&values.token, "token", "", "API token")
	conversations.Flags().BoolVar(&values.jsonOutput, "json", false, "print JSON response")
	values.pagination.AddFlags(conversations)

	tools := &cobra.Command{
		Use:   "tools",
		Short: "List the canonical agent tools",
		Long:  "List the canonical agent tools exposed by built-in chat and deployment MCP, including each tool's privilege, effect, defaults, closed input schema, and backing operation.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTools(cmd.OutOrStdout(), dependencies.Operations)
		},
	}

	parent.AddCommand(ask, conversations, tools)
	return parent
}

func runAsk(ctx context.Context, client cliapi.Client, values *options, question string, out io.Writer) error {
	if client == nil {
		return fmt.Errorf("agent CLI API client is required")
	}
	credentials := cliapi.Credentials{Target: values.target, Token: values.token}
	api, err := agentClient(ctx, client, credentials)
	if err != nil {
		return err
	}
	conversationID := values.conversation
	if conversationID == "" {
		title := "CLI conversation"
		conversation, err := api.CreateAgentConversation(ctx, agentgen.GenCreateAgentConversationClientRequest{
			Headers: agentgen.GenCreateAgentConversationClientHeaders{
				IdempotencyKey: fmt.Sprintf("cli-conversation-%d", time.Now().UnixNano()),
			},
			Body: agentgen.GenSchemaAgentConversationCreateRequest{Title: &title},
		})
		if err != nil {
			var failure agentgen.GenCreateAgentConversationFailure
			if errors.As(err, &failure) {
				handler := func(problem apigenclient.ProblemDetails) error {
					return generatedAgentProblemError("create agent conversation", problem)
				}
				return agentgen.MatchGenCreateAgentConversationFailure(failure, handler)
			}
			return err
		}
		conversationID = conversation.Body.Id
	}
	runResponse, err := api.CreateAgentRun(ctx, agentgen.GenCreateAgentRunClientRequest{
		Conversation: conversationID,
		Headers: agentgen.GenCreateAgentRunClientHeaders{
			IdempotencyKey: fmt.Sprintf("cli-run-%d", time.Now().UnixNano()),
		},
		Body: agentgen.GenSchemaAgentRunCreateRequest{Input: question},
	})
	if err != nil {
		var failure agentgen.GenCreateAgentRunFailure
		if errors.As(err, &failure) {
			handler := func(problem apigenclient.ProblemDetails) error {
				return generatedAgentProblemError("create agent run", problem)
			}
			return agentgen.MatchGenCreateAgentRunFailure(failure, handler, handler, handler, handler)
		}
		return err
	}
	run := runResponse.Body
	for run.Status == "queued" || run.Status == "running" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		currentRun, err := api.GetAgentRun(ctx, agentgen.GenGetAgentRunClientRequest{
			Conversation: conversationID,
			Run:          run.Id,
		})
		if err != nil {
			return err
		}
		run = currentRun.Body
	}
	messages, err := api.ListAgentMessages(ctx, agentgen.GenListAgentMessagesClientRequest{
		Conversation: conversationID,
	})
	if err != nil {
		return err
	}
	content := ""
	for _, message := range messages.Body.Items {
		if stringValue(message.RunId) == run.Id && message.Role == "assistant" && stringValue(message.ContentText) != "" {
			content = stringValue(message.ContentText)
		}
	}
	if values.jsonOutput {
		return json.NewEncoder(out).Encode(map[string]any{"conversationId": conversationID, "run": run, "content": content})
	}
	fmt.Fprintln(out, content)
	fmt.Fprintf(out, "\nconversation=%s run=%s stop=%s\n", conversationID, run.Id, stringValue(run.StopReason))
	if run.Status != "completed" {
		return fmt.Errorf("agent run ended with status %s: %s", run.Status, stringValue(run.Error))
	}
	return nil
}

func generatedAgentProblemError(operation string, problem apigenclient.ProblemDetails) error {
	detail := problem.Detail
	if detail == "" {
		detail = problem.Title
	}
	if detail == "" {
		detail = "request failed"
	}
	if problem.Code != "" {
		return fmt.Errorf("%s failed (%s): %s", operation, problem.Code, detail)
	}
	return fmt.Errorf("%s failed: %s", operation, detail)
}

func runConversations(ctx context.Context, client cliapi.Client, values *options, out io.Writer) error {
	if client == nil {
		return fmt.Errorf("agent CLI API client is required")
	}
	api, err := agentClient(ctx, client, cliapi.Credentials{Target: values.target, Token: values.token})
	if err != nil {
		return err
	}
	response, err := api.ListAgentConversations(ctx, agentgen.GenListAgentConversationsClientRequest{
		Params: agentgen.GenListAgentConversationsClientParams{
			Limit:     values.pagination.LimitPtr(),
			PageToken: optionalString(values.pagination.PageToken),
		},
	})
	if err != nil {
		return err
	}
	if values.jsonOutput {
		return json.NewEncoder(out).Encode(response.Body.Items)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tTITLE\tUPDATED")
	for _, row := range response.Body.Items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", row.Id, row.Status, row.Title, row.UpdatedAt)
	}
	return tw.Flush()
}

func agentClient(ctx context.Context, client cliapi.Client, credentials cliapi.Credentials) (*agentgen.GenClient, error) {
	if client == nil {
		return nil, fmt.Errorf("agent CLI API client is required")
	}
	transport, err := client.Transport(ctx, credentials)
	if err != nil {
		return nil, err
	}
	return agentgen.NewGenClient(transport), nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func runTools(out io.Writer, operations func() []agenttools.APIGenOperation) error {
	if operations == nil {
		return fmt.Errorf("agent CLI operation catalog is required")
	}
	reference, err := agenttools.ReferenceCatalog(operations())
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPRIVILEGE\tEFFECT\tDEFAULTS\tINPUT_SCHEMA\tOPERATION")
	for _, tool := range reference {
		defaults, _ := json.Marshal(tool.Defaults)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			tool.Name, tool.Privilege, tool.Effect, defaults, compactJSON(tool.InputSchema), tool.OperationID)
	}
	return tw.Flush()
}

func compactJSON(value json.RawMessage) string {
	var output bytes.Buffer
	if err := json.Compact(&output, value); err != nil {
		return string(value)
	}
	return output.String()
}
