package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	"github.com/flidai/leapview/internal/platform/cliapi"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/spf13/cobra"
)

type apiCallOptions struct {
	pathParams     []string
	queryParams    []string
	bodyJSON       string
	bodyFile       string
	contentType    string
	idempotencyKey string
	ifMatch        string
}

func apiCommand(ctx context.Context, opts *rootOptions) *cobra.Command {
	parent := &cobra.Command{Use: "api", Short: "Call any generated LeapView API operation"}
	list := &cobra.Command{
		Use:   "list",
		Short: "List generated API operations",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAPIList()
		},
	}
	describe := &cobra.Command{
		Use:   "describe <operation>",
		Short: "Describe a generated API operation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAPIDescribe(args[0])
		},
	}
	callOpts := &apiCallOptions{}
	call := &cobra.Command{
		Use:   "call <operation>",
		Short: "Call a generated API operation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAPICall(ctx, opts, args[0], callOpts)
		},
	}
	addTargetTokenFlags(call, opts)
	call.Flags().StringArrayVar(&callOpts.pathParams, "path", nil, "path parameter as key=value; repeatable")
	call.Flags().StringArrayVar(&callOpts.queryParams, "query", nil, "query parameter as key=value; repeatable")
	call.Flags().StringVar(&callOpts.bodyJSON, "body-json", "", "request JSON body")
	call.Flags().StringVar(&callOpts.bodyFile, "body-file", "", "request body file")
	call.Flags().StringVar(&callOpts.contentType, "content-type", "", "request body content type")
	call.Flags().StringVar(
		&callOpts.idempotencyKey,
		"idempotency-key",
		"",
		"stable retry key for mutating operations",
	)
	call.Flags().StringVar(&callOpts.ifMatch, "if-match", "", "strong resource revision for commands requiring concurrency control")
	parent.AddCommand(list, describe, call)
	return parent
}

func runAPIList() error {
	contracts := sortedAPIOperationContracts()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "OPERATION\tMETHOD\tPATH\tTAGS")
	for _, contract := range contracts {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", contract.OperationID, contract.Method, contract.Path, strings.Join(contract.Tags, ","))
	}
	return tw.Flush()
}

func runAPIDescribe(operationID string) error {
	contract, ok := apiaggregate.GetAPIGenOperationContract(operationID)
	if !ok {
		return fmt.Errorf("unknown API operation %q", operationID)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(contract)
}

func runAPICall(ctx context.Context, opts *rootOptions, operationID string, callOpts *apiCallOptions) error {
	contract, ok := apiaggregate.GetAPIGenOperationContract(operationID)
	if !ok {
		return fmt.Errorf("unknown API operation %q", operationID)
	}
	pathParams, err := parseKeyValuePairs(callOpts.pathParams)
	if err != nil {
		return fmt.Errorf("path: %w", err)
	}
	if err := requirePathParams(contract.Path, pathParams); err != nil {
		return err
	}
	query, err := parseQueryValues(callOpts.queryParams)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	credentials, err := (capabilityAPIClient{}).Resolve(ctx, cliapi.Credentials{Target: opts.target, Token: opts.token})
	if err != nil {
		return err
	}
	target, token := credentials.Target, credentials.Token
	endpoint, err := apiOperationURL(target, operationID, pathParams, query)
	if err != nil {
		return err
	}
	body, contentType, err := apiRequestBody(operationID, callOpts, contract.RequestBodyRequired)
	if err != nil {
		return err
	}
	idempotencyKey, ifMatch := generatedCommandHeaders(contract, callOpts)
	if contract.Command != nil && contract.Command.Concurrency == "if-match" && ifMatch == "" {
		return fmt.Errorf("operation %q requires --if-match", operationID)
	}
	return doRawAPI(
		ctx,
		contract.Method,
		endpoint,
		token,
		contentType,
		idempotencyKey,
		ifMatch,
		body,
		os.Stdout,
	)
}

func generatedCommandHeaders(contract apiaggregate.GenOperationContract, callOpts *apiCallOptions) (idempotencyKey, ifMatch string) {
	if contract.Command == nil {
		return "", ""
	}
	if contract.Command.Idempotency == "required" {
		idempotencyKey = strings.TrimSpace(callOpts.idempotencyKey)
		if idempotencyKey == "" {
			idempotencyKey = apitransport.NewRequestID()
		}
	}
	if contract.Command.Concurrency == "if-match" {
		ifMatch = strings.TrimSpace(callOpts.ifMatch)
	}
	return idempotencyKey, ifMatch
}

func sortedAPIOperationContracts() []apiaggregate.GenOperationContract {
	registry := apiaggregate.GetAPIGenOperationContracts()
	contracts := make([]apiaggregate.GenOperationContract, 0, len(registry))
	for _, contract := range registry {
		contracts = append(contracts, contract)
	}
	sort.Slice(contracts, func(i, j int) bool {
		return contracts[i].OperationID < contracts[j].OperationID
	})
	return contracts
}

func parseKeyValuePairs(values []string) (map[string]string, error) {
	out := map[string]string{}
	for _, raw := range values {
		key, value, ok := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("%q must be key=value", raw)
		}
		if _, exists := out[key]; exists {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		out[key] = value
	}
	return out, nil
}

func parseQueryValues(values []string) (url.Values, error) {
	out := url.Values{}
	for _, raw := range values {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("%q must be key=value", raw)
		}
		out.Add(key, value)
	}
	return out, nil
}

func requirePathParams(path string, values map[string]string) error {
	names := pathParamNames(path)
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
		value, ok := values[name]
		if !ok || strings.TrimSpace(value) == "" {
			return fmt.Errorf("missing path parameter %q; pass --path %s=<value>", name, name)
		}
	}
	for name := range values {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unknown path parameter %q", name)
		}
	}
	return nil
}

func pathParamNames(path string) []string {
	names := []string{}
	for {
		start := strings.Index(path, "{")
		if start < 0 {
			return names
		}
		path = path[start+1:]
		end := strings.Index(path, "}")
		if end < 0 {
			return names
		}
		names = append(names, path[:end])
		path = path[end+1:]
	}
}

func apiRequestBody(operationID string, callOpts *apiCallOptions, required bool) (io.Reader, string, error) {
	if callOpts.bodyJSON != "" && callOpts.bodyFile != "" {
		return nil, "", fmt.Errorf("use only one of --body-json or --body-file")
	}
	if callOpts.bodyJSON != "" {
		if !json.Valid([]byte(callOpts.bodyJSON)) {
			return nil, "", fmt.Errorf("body-json must be valid JSON")
		}
		contentType := callOpts.contentType
		if contentType == "" {
			contentType = apiOperationRequestContentType(operationID, "application/json")
		}
		return strings.NewReader(callOpts.bodyJSON), contentType, nil
	}
	if callOpts.bodyFile != "" {
		bodyBytes, err := os.ReadFile(callOpts.bodyFile)
		if err != nil {
			return nil, "", err
		}
		contentType := callOpts.contentType
		if contentType == "" {
			contentType = apiOperationRequestContentType(operationID, "application/octet-stream")
		}
		return bytes.NewReader(bodyBytes), contentType, nil
	}
	if required {
		return nil, "", fmt.Errorf("operation requires --body-json or --body-file")
	}
	return nil, "", nil
}

func apiOperationRequestContentType(operationID string, fallback string) string {
	spec, err := apiaggregate.GetEmbeddedOpenAPISpec()
	if err != nil {
		return fallback
	}
	paths, _ := spec["paths"].(map[string]any)
	for _, rawPath := range paths {
		path, _ := rawPath.(map[string]any)
		for _, method := range []string{"get", "post", "put", "patch", "delete"} {
			operation, _ := path[method].(map[string]any)
			if operation["operationId"] != operationID {
				continue
			}
			requestBody, _ := operation["requestBody"].(map[string]any)
			content, _ := requestBody["content"].(map[string]any)
			if _, ok := content[fallback]; ok {
				return fallback
			}
			if len(content) == 1 {
				for contentType := range content {
					return contentType
				}
			}
			return fallback
		}
	}
	return fallback
}

func doRawAPI(
	ctx context.Context,
	method,
	endpoint,
	token,
	contentType,
	idempotencyKey string,
	ifMatch string,
	body io.Reader,
	out io.Writer,
) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-LeapView-Client", "cli")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", method, endpoint, strings.TrimSpace(string(bytes)))
	}
	if len(bytes) == 0 {
		return nil
	}
	if _, err := out.Write(bytes); err != nil {
		return err
	}
	if !strings.HasSuffix(string(bytes), "\n") {
		_, err = fmt.Fprintln(out)
	}
	return err
}
