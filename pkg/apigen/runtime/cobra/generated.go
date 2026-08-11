package cobra

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	spcobra "github.com/spf13/cobra"
)

// AddGeneratedCommands builds Cobra commands from generated command specs.
func AddGeneratedCommands(rootCmd *spcobra.Command, client *Client, specs []CommandSpec, opts RuntimeOptions) error {
	if err := validateCommandSpecs(specs); err != nil {
		return err
	}

	sorted := append([]CommandSpec(nil), specs...)
	sort.Slice(sorted, func(i, j int) bool {
		left := strings.Join(sorted[i].Command, " ")
		right := strings.Join(sorted[j].Command, " ")
		if left == right {
			return sorted[i].OperationID < sorted[j].OperationID
		}
		return left < right
	})

	ensureGeneratedRootGroups(rootCmd, sorted, opts)
	groups := map[string]*spcobra.Command{}

	for _, spec := range sorted {
		if len(spec.Command) == 0 {
			continue
		}

		parent := rootCmd
		for i := 0; i < len(spec.Command)-1; i++ {
			segment := spec.Command[i]
			nodePath := strings.Join(spec.Command[:i+1], " ")
			node, ok := groups[nodePath]
			if !ok {
				node = &spcobra.Command{
					Use:   segment,
					Short: generatedGroupDescription(spec.Command[:i+1], opts),
					RunE: func(cmd *spcobra.Command, args []string) error {
						if len(args) == 0 {
							return cmd.Help()
						}
						_ = cmd.Help()
						return fmt.Errorf("unknown subcommand %q", args[0])
					},
				}
				if i == 0 {
					if group := resolveRootGroup(spec.Command, opts); group != nil {
						node.GroupID = group.ID
					}
				}
				parent.AddCommand(node)
				groups[nodePath] = node
			}
			parent = node
		}

		parent.AddCommand(newGeneratedLeafCommand(spec, client, opts))
	}

	return nil
}

func validateCommandSpecs(specs []CommandSpec) error {
	seen := make(map[string]string, len(specs))
	paths := make(map[string][]string, len(specs))
	for _, spec := range specs {
		if len(spec.Command) == 0 {
			continue
		}
		command := strings.Join(spec.Command, " ")
		if existing, ok := seen[command]; ok {
			return fmt.Errorf("duplicate generated CLI command %q for operations %q and %q", command, existing, spec.OperationID)
		}
		for other, otherPath := range paths {
			if commandPathPrefix(spec.Command, otherPath) || commandPathPrefix(otherPath, spec.Command) {
				return fmt.Errorf("generated CLI command %q for operation %q conflicts with %q for operation %q", command, spec.OperationID, other, seen[other])
			}
		}
		seen[command] = spec.OperationID
		paths[command] = append([]string(nil), spec.Command...)
	}
	return nil
}

func ensureGeneratedRootGroups(rootCmd *spcobra.Command, specs []CommandSpec, opts RuntimeOptions) {
	required := map[string]string{}
	for _, spec := range specs {
		group := resolveRootGroup(spec.Command, opts)
		if group == nil {
			continue
		}
		required[group.ID] = group.Title
	}

	for groupID, title := range required {
		if rootCmd.ContainsGroup(groupID) {
			continue
		}
		rootCmd.AddGroup(&spcobra.Group{ID: groupID, Title: title})
	}
}

func resolveRootGroup(commandPath []string, opts RuntimeOptions) *CommandGroup {
	if opts.RootGroupResolver == nil {
		return nil
	}
	group := opts.RootGroupResolver(commandPath)
	if group == nil || strings.TrimSpace(group.ID) == "" || strings.TrimSpace(group.Title) == "" {
		return nil
	}
	return group
}

func generatedGroupDescription(commandPath []string, opts RuntimeOptions) string {
	if opts.GroupDescriptionResolver != nil {
		if value := strings.TrimSpace(opts.GroupDescriptionResolver(commandPath)); value != "" {
			return value
		}
	}
	if len(commandPath) > 0 {
		return "Manage " + commandPath[len(commandPath)-1]
	}
	return "Manage resources"
}

func newGeneratedLeafCommand(spec CommandSpec, client *Client, opts RuntimeOptions) *spcobra.Command {
	use := spec.Command[len(spec.Command)-1]
	if len(spec.Args) > 0 {
		parts := make([]string, 0, len(spec.Args))
		for _, arg := range spec.Args {
			name := arg.DisplayName
			if strings.TrimSpace(name) == "" {
				name = strings.ReplaceAll(arg.Name, "_", "-")
			}
			parts = append(parts, "<"+name+">")
		}
		use += " " + strings.Join(parts, " ")
	}

	cmd := &spcobra.Command{
		Use:   use,
		Short: spec.Summary,
		Long:  spec.Description,
		Args:  spcobra.ExactArgs(len(spec.Args)),
		RunE: func(cmd *spcobra.Command, args []string) error {
			return runGeneratedCommand(cmd, client, spec, args, opts)
		},
	}

	positional := make(map[string]struct{}, len(spec.Args))
	for _, arg := range spec.Args {
		positional[arg.Source+":"+arg.Name] = struct{}{}
	}

	for _, parameter := range spec.Parameters {
		if _, ok := positional[parameter.In+":"+parameter.Name]; ok {
			continue
		}
		flagName := toFlagName(parameter.Name)
		addTypedFlag(cmd, flagName, parameter.Type, parameter.Enum, parameter.Description, parameter.Name, false)
		if parameter.Required {
			_ = cmd.MarkFlagRequired(flagName)
		}
	}

	if spec.RequestBody != nil {
		if spec.RequestBody.InputMode == "json" || spec.RequestBody.InputMode == "flags_or_json" {
			cmd.Flags().String("json", "", "JSON input (raw string or @filename or - for stdin)")
		}
		if spec.RequestBody.InputMode == "text" {
			cmd.Flags().String("text", "", "Text input (raw string or @filename or - for stdin)")
		}
		if spec.RequestBody.InputMode == "binary" || spec.RequestBody.InputMode == "file" {
			cmd.Flags().String("file", "", "Binary/file input path (- for stdin)")
		}
		if spec.RequestBody.InputMode == "multipart" {
			cmd.Flags().StringArray("part", nil, "Multipart part input name=value, name=@file, or name=-")
		}
		if spec.RequestBody.InputMode == "flags" || spec.RequestBody.InputMode == "flags_or_json" {
			for _, field := range spec.RequestBody.Fields {
				if _, ok := positional["body:"+field.Name]; ok {
					continue
				}
				flagName := toFlagName(field.Name)
				addTypedFlag(cmd, flagName, field.Type, field.Enum, field.Description, field.Name, true)
				if field.Required && spec.RequestBody.InputMode == "flags" {
					_ = cmd.MarkFlagRequired(flagName)
				}
			}
		}
	}

	if spec.Confirm == "always" {
		cmd.Flags().Bool("yes", false, "Skip confirmation prompt")
	}
	if spec.Pagination != nil {
		cmd.Flags().Bool("all", false, "Fetch all pages")
	}

	if mutate := opts.CommandMutators[spec.OperationID]; mutate != nil {
		mutate(cmd)
	}

	return cmd
}

func runGeneratedCommand(cmd *spcobra.Command, client *Client, spec CommandSpec, args []string, opts RuntimeOptions) error {
	if override := opts.RunOverrides[spec.OperationID]; override != nil {
		return override(client)(cmd, args)
	}

	if spec.Confirm == "always" {
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes && !ConfirmPrompt("Are you sure?") {
			return nil
		}
	}

	argValues := map[string]string{}
	for i, arg := range spec.Args {
		argValues[arg.Source+":"+arg.Name] = args[i]
	}

	urlPath := spec.Path
	for _, parameter := range spec.Parameters {
		if parameter.In != "path" {
			continue
		}
		value, err := resolveInputValue(cmd, parameter.Name, "path", argValues)
		if err != nil {
			return err
		}
		urlPath = strings.Replace(urlPath, "{"+parameter.Name+"}", url.PathEscape(value), 1)
	}
	if strings.Contains(urlPath, "{") {
		return fmt.Errorf("unresolved path parameter in URL: %s", urlPath)
	}

	query := url.Values{}
	headers := http.Header{}
	for _, parameter := range spec.Parameters {
		if parameter.In != "query" {
			continue
		}
		value, ok, err := resolveTypedInputValue(cmd, parameter.Name, parameter.Type, "query", argValues)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		addQueryValue(query, parameter.Name, value)
	}
	for _, parameter := range spec.Parameters {
		if parameter.In != "header" {
			continue
		}
		value, ok, err := resolveTypedInputValue(cmd, parameter.Name, parameter.Type, "header", argValues)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		headers.Add(parameter.Name, fmt.Sprint(value))
	}

	var body any
	if spec.RequestBody != nil {
		built, err := buildRequestBody(cmd, spec, argValues)
		if err != nil {
			return err
		}
		body = built
	}

	allPages, _ := cmd.Flags().GetBool("all")
	if allPages && spec.Pagination != nil {
		bodyBytes, err := fetchAllPages(client, spec, urlPath, query, headers)
		if err != nil {
			return err
		}
		return renderResponseBody(cmd, spec, bodyBytes, opts)
	}

	contentType, bodyKind := requestContent(spec.RequestBody)
	resp, err := client.DoWithHeaders(spec.Method, urlPath, query, headers, body, contentType, bodyKind)
	if err != nil {
		return err
	}
	if err := CheckError(resp); err != nil {
		return err
	}

	if spec.Output.Mode == "empty" || resp.StatusCode == 204 {
		if outputFormat(cmd) == OutputJSON {
			return PrintJSON(os.Stdout, map[string]string{"status": "ok"})
		}
		_, _ = fmt.Fprintln(os.Stdout, "Done.")
		return nil
	}

	bodyBytes, err := ReadBody(resp)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	return renderResponseBody(cmd, spec, bodyBytes, opts)
}

func buildRequestBody(cmd *spcobra.Command, spec CommandSpec, argValues map[string]string) (any, error) {
	requestBody := spec.RequestBody
	if requestBody == nil {
		return nil, nil
	}

	switch requestBody.InputMode {
	case "none":
		return nil, nil
	case "text":
		textInput, _ := cmd.Flags().GetString("text")
		if strings.TrimSpace(textInput) == "" {
			if requestBody.Required {
				return nil, fmt.Errorf("request body is required; use --text")
			}
			return nil, nil
		}
		return readRawStringInput(textInput)
	case "binary", "file":
		path, _ := cmd.Flags().GetString("file")
		if strings.TrimSpace(path) == "" {
			if requestBody.Required {
				return nil, fmt.Errorf("request body is required; use --file")
			}
			return nil, nil
		}
		return readRawBytesInput(path)
	case "json":
		jsonInput, _ := cmd.Flags().GetString("json")
		if strings.TrimSpace(jsonInput) == "" {
			if requestBody.Required {
				return nil, fmt.Errorf("request body is required; use --json")
			}
			return nil, nil
		}
		return readRawJSONInput(jsonInput)
	case "multipart":
		return buildMultipartBody(cmd, requestBody)
	case "flags", "flags_or_json":
		if requestBody.InputMode == "flags_or_json" {
			jsonInput, _ := cmd.Flags().GetString("json")
			if strings.TrimSpace(jsonInput) != "" {
				return readRawJSONInput(jsonInput)
			}
		}
		if requestBody.Required && requestBody.SchemaType == "object" && len(requestBody.Fields) == 0 {
			return map[string]any{}, nil
		}

		body := map[string]any{}
		form := url.Values{}
		setCount := 0
		for _, field := range requestBody.Fields {
			if value, ok := argValues["body:"+field.Name]; ok {
				casted := castStringValue(value, field.Type)
				body[field.Name] = casted
				form.Set(field.Name, fmt.Sprint(casted))
				setCount++
				continue
			}
			flagName := toFlagName(field.Name)
			if !cmd.Flags().Changed(flagName) {
				if field.Required {
					return nil, fmt.Errorf("required flag %q not set", flagName)
				}
				continue
			}
			value, err := getFlagValue(cmd, flagName, field.Type)
			if err != nil {
				return nil, err
			}
			body[field.Name] = value
			form.Set(field.Name, fmt.Sprint(value))
			setCount++
		}

		if requestBody.Required && setCount == 0 {
			return nil, fmt.Errorf("request body is required")
		}
		if requestBody.BodyKind == "form_urlencoded" {
			return form, nil
		}
		return body, nil
	default:
		return nil, fmt.Errorf("unsupported request body input mode %q", requestBody.InputMode)
	}
}

func buildMultipartBody(cmd *spcobra.Command, requestBody *RequestBodySpec) (any, error) {
	rawParts, _ := cmd.Flags().GetStringArray("part")
	byName := make(map[string]MultipartPartSpec, len(requestBody.Parts))
	seen := make(map[string]int, len(requestBody.Parts))
	collected := make(map[string][]MultipartPart, len(requestBody.Parts))
	for _, spec := range requestBody.Parts {
		byName[spec.Name] = spec
	}

	for _, raw := range rawParts {
		name, value, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("multipart parts must use name=value, name=@file, or name=-")
		}
		name = strings.TrimSpace(name)
		spec, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown multipart part %q", name)
		}
		seen[name]++
		if seen[name] > 1 && !spec.Repeated {
			return nil, fmt.Errorf("duplicate multipart part %q", name)
		}
		part, err := parseMultipartPartInput(spec, value)
		if err != nil {
			return nil, err
		}
		collected[name] = append(collected[name], part)
	}

	hasRequiredPart := false
	for _, spec := range requestBody.Parts {
		if spec.Required {
			hasRequiredPart = true
		}
		if spec.Required && len(collected[spec.Name]) == 0 {
			return nil, fmt.Errorf("required multipart part %q is missing", spec.Name)
		}
	}
	if requestBody.Required && len(rawParts) == 0 && !hasRequiredPart {
		return nil, fmt.Errorf("request body is required; use --part")
	}

	body := MultipartBody{ContentType: requestBody.ContentType}
	for _, spec := range requestBody.Parts {
		body.Parts = append(body.Parts, collected[spec.Name]...)
	}
	return body, nil
}

func parseMultipartPartInput(spec MultipartPartSpec, value string) (MultipartPart, error) {
	data, filename, err := readMultipartPartInput(spec, value)
	if err != nil {
		return MultipartPart{}, err
	}
	return MultipartPart{
		Name:        multipartPartWireName(spec),
		ContentType: defaultGeneratedContentType(spec.ContentType),
		BodyKind:    spec.BodyKind,
		Filename:    filename,
		Data:        data,
	}, nil
}

func readMultipartPartInput(spec MultipartPartSpec, value string) ([]byte, *string, error) {
	switch spec.BodyKind {
	case "json", "form_urlencoded":
		data, filename, err := readMultipartTextOrFile(value)
		if err != nil {
			return nil, nil, fmt.Errorf("read multipart part %q: %w", spec.Name, err)
		}
		if spec.BodyKind == "json" && !json.Valid(data) {
			return nil, nil, fmt.Errorf("multipart part %q must be valid JSON", spec.Name)
		}
		return data, filename, nil
	case "text":
		data, filename, err := readMultipartTextOrFile(value)
		if err != nil {
			return nil, nil, fmt.Errorf("read multipart part %q: %w", spec.Name, err)
		}
		return data, filename, nil
	case "binary", "file":
		if value != "-" && !strings.HasPrefix(value, "@") {
			return nil, nil, fmt.Errorf("multipart part %q requires @file or - input", spec.Name)
		}
		data, filename, err := readMultipartFileOrStdin(value)
		if err != nil {
			return nil, nil, fmt.Errorf("read multipart part %q: %w", spec.Name, err)
		}
		if spec.BodyKind == "binary" {
			filename = nil
		}
		return data, filename, nil
	default:
		return nil, nil, fmt.Errorf("multipart part %q has unsupported body kind %q", spec.Name, spec.BodyKind)
	}
}

func readMultipartTextOrFile(value string) ([]byte, *string, error) {
	if value == "-" {
		data, err := io.ReadAll(os.Stdin)
		return data, nil, err
	}
	if strings.HasPrefix(value, "@") {
		return readMultipartFileOrStdin(value)
	}
	return []byte(value), nil, nil
}

func readMultipartFileOrStdin(value string) ([]byte, *string, error) {
	if value == "-" || value == "@-" {
		data, err := io.ReadAll(os.Stdin)
		return data, nil, err
	}
	path := strings.TrimPrefix(value, "@")
	if strings.TrimSpace(path) == "" {
		return nil, nil, fmt.Errorf("file path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	name := filepath.Base(path)
	return data, &name, nil
}

func multipartPartWireName(spec MultipartPartSpec) string {
	if strings.TrimSpace(spec.WireName) != "" {
		return spec.WireName
	}
	return spec.Name
}

func defaultGeneratedContentType(value string) string {
	if strings.TrimSpace(value) == "" {
		return "application/json"
	}
	return value
}

func requestContent(requestBody *RequestBodySpec) (string, string) {
	if requestBody == nil {
		return "", ""
	}
	return requestBody.ContentType, requestBody.BodyKind
}

func fetchAllPages(client *Client, spec CommandSpec, path string, baseQuery url.Values, headers http.Header) ([]byte, error) {
	if spec.Pagination == nil {
		return nil, fmt.Errorf("pagination is not configured")
	}
	itemsField := spec.Pagination.ItemsField
	if itemsField == "" {
		itemsField = "data"
	}
	nextField := spec.Pagination.NextPageTokenField
	if nextField == "" {
		nextField = "next_page_token"
	}

	var items []any
	pageToken := ""
	for {
		query := cloneQuery(baseQuery)
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}

		resp, err := client.DoWithHeaders(spec.Method, path, query, headers, nil, "", "")
		if err != nil {
			return nil, err
		}
		if err := CheckError(resp); err != nil {
			return nil, err
		}
		bodyBytes, err := ReadBody(resp)
		if err != nil {
			return nil, err
		}

		var payload map[string]any
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			return nil, fmt.Errorf("parse paginated response: %w", err)
		}
		pageItems, ok := payload[itemsField].([]any)
		if ok {
			items = append(items, pageItems...)
		}

		nextValue, _ := payload[nextField].(string)
		if strings.TrimSpace(nextValue) == "" {
			payload[itemsField] = items
			payload[nextField] = ""
			return json.Marshal(payload)
		}
		pageToken = nextValue
	}
}

func renderResponseBody(cmd *spcobra.Command, spec CommandSpec, body []byte, opts RuntimeOptions) error {
	if renderer := opts.ResponseRenderers[spec.OperationID]; renderer != nil {
		return renderer(cmd, body)
	}
	if len(body) == 0 {
		if outputFormat(cmd) == OutputJSON {
			return PrintJSON(os.Stdout, map[string]string{"status": "ok"})
		}
		_, _ = fmt.Fprintln(os.Stdout, "Done.")
		return nil
	}

	if quietMode(cmd) && len(spec.Output.QuietFields) > 0 {
		return renderQuiet(body, spec)
	}

	switch spec.Output.Mode {
	case "collection":
		return renderCollection(cmd, body, spec)
	case "detail":
		return renderDetail(cmd, body)
	case "raw":
		return renderRaw(cmd, body)
	default:
		return renderRaw(cmd, body)
	}
}

func renderCollection(cmd *spcobra.Command, body []byte, spec CommandSpec) error {
	if outputFormat(cmd) == OutputJSON {
		var payload any
		if err := json.Unmarshal(body, &payload); err == nil {
			return PrintJSON(os.Stdout, payload)
		}
		return PrintJSON(os.Stdout, map[string]string{"body": string(body)})
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	itemsField := "data"
	if spec.Pagination != nil && spec.Pagination.ItemsField != "" {
		itemsField = spec.Pagination.ItemsField
	}
	if len(spec.Output.TableColumns) > 0 {
		rows := extractRows(payload, itemsField, spec.Output.TableColumns)
		if len(rows) > 0 {
			PrintTable(os.Stdout, spec.Output.TableColumns, rows)
			return nil
		}
	}
	return PrintJSON(os.Stdout, payload)
}

func renderDetail(cmd *spcobra.Command, body []byte) error {
	if outputFormat(cmd) == OutputJSON {
		var payload any
		if err := json.Unmarshal(body, &payload); err == nil {
			return PrintJSON(os.Stdout, payload)
		}
		return PrintJSON(os.Stdout, map[string]string{"body": string(body)})
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	PrintDetail(os.Stdout, payload)
	return nil
}

func renderRaw(cmd *spcobra.Command, body []byte) error {
	if outputFormat(cmd) == OutputJSON {
		var payload any
		if err := json.Unmarshal(body, &payload); err == nil {
			return PrintJSON(os.Stdout, payload)
		}
		return PrintJSON(os.Stdout, map[string]string{"body": string(body)})
	}
	_, _ = fmt.Fprintln(os.Stdout, string(body))
	return nil
}

func renderQuiet(body []byte, spec CommandSpec) error {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		_, _ = fmt.Fprintln(os.Stdout, string(body))
		return nil
	}

	switch typed := payload.(type) {
	case map[string]any:
		if spec.Output.Mode == "collection" {
			itemsField := "data"
			if spec.Pagination != nil && spec.Pagination.ItemsField != "" {
				itemsField = spec.Pagination.ItemsField
			}
			if items, ok := typed[itemsField].([]any); ok {
				for _, item := range items {
					if record, ok := item.(map[string]any); ok {
						if value, ok := firstMatchingQuietField(record, spec.Output.QuietFields); ok {
							_, _ = fmt.Fprintln(os.Stdout, FormatValue(value))
						}
					}
				}
				return nil
			}
		}
		if value, ok := firstMatchingQuietField(typed, spec.Output.QuietFields); ok {
			_, _ = fmt.Fprintln(os.Stdout, FormatValue(value))
			return nil
		}
	}

	_, _ = fmt.Fprintln(os.Stdout, string(body))
	return nil
}

func firstMatchingQuietField(record map[string]any, fields []string) (any, bool) {
	for _, field := range fields {
		value, ok := record[field]
		if ok {
			return value, true
		}
	}
	return nil, false
}

func extractRows(payload map[string]any, itemsField string, columns []string) [][]string {
	items, ok := payload[itemsField].([]any)
	if !ok {
		return nil
	}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		row := make([]string, len(columns))
		for i, column := range columns {
			row[i] = ExtractField(record, column)
		}
		rows = append(rows, row)
	}
	return rows
}

func resolveInputValue(cmd *spcobra.Command, name string, source string, argValues map[string]string) (string, error) {
	if value, ok := argValues[source+":"+name]; ok {
		return value, nil
	}
	flagName := toFlagName(name)
	value, err := cmd.Flags().GetString(flagName)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("required %s parameter %q is missing", source, name)
	}
	return value, nil
}

func resolveTypedInputValue(cmd *spcobra.Command, name, typ, source string, argValues map[string]string) (any, bool, error) {
	if value, ok := argValues[source+":"+name]; ok {
		return castStringValue(value, typ), true, nil
	}
	flagName := toFlagName(name)
	if !cmd.Flags().Changed(flagName) {
		return nil, false, nil
	}
	value, err := getFlagValue(cmd, flagName, typ)
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func addQueryValue(query url.Values, name string, value any) {
	switch typed := value.(type) {
	case string:
		query.Set(name, typed)
	case int64:
		query.Set(name, strconv.FormatInt(typed, 10))
	case bool:
		query.Set(name, strconv.FormatBool(typed))
	case []string:
		for _, item := range typed {
			query.Add(name, item)
		}
	default:
		query.Set(name, fmt.Sprintf("%v", typed))
	}
}

func cloneQuery(in url.Values) url.Values {
	out := url.Values{}
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func toFlagName(name string) string {
	var out strings.Builder
	for i, r := range name {
		if r == '_' {
			out.WriteRune('-')
			continue
		}
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				out.WriteRune('-')
			}
			out.WriteRune(r + ('a' - 'A'))
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func addTypedFlag(cmd *spcobra.Command, name, typ string, enum []string, description, fallbackName string, bodyField bool) {
	usage := buildFlagUsage(fallbackName, typ, description, enum, bodyField)

	switch typ {
	case "integer", "int", "int32", "int64", "number":
		cmd.Flags().Int64(name, 0, usage)
	case "boolean", "bool":
		cmd.Flags().Bool(name, false, usage)
	case "array":
		cmd.Flags().StringSlice(name, nil, usage)
	default:
		cmd.Flags().String(name, "", usage)
	}
}

func buildFlagUsage(name, typ, description string, enum []string, bodyField bool) string {
	usage := strings.TrimSpace(description)
	if usage == "" {
		usage = humanizeIdentifier(name)
	}

	switch typ {
	case "object":
		if bodyField {
			usage += " (JSON object; use --json for nested input)"
		} else {
			usage += " (JSON object)"
		}
	case "array":
		if bodyField {
			usage += " (repeat flag or use --json for nested input)"
		} else {
			usage += " (repeat flag to pass multiple values)"
		}
	}
	if len(enum) > 0 {
		usage += " (one of: " + strings.Join(enum, ", ") + ")"
	}
	return usage
}

func humanizeIdentifier(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "Value"
	}
	replaced := strings.NewReplacer("_", " ", "-", " ").Replace(toFlagName(trimmed))
	parts := strings.Fields(replaced)
	for i, part := range parts {
		if part == "" {
			continue
		}
		runes := []rune(strings.ToLower(part))
		runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
		parts[i] = string(runes)
	}
	return strings.Join(parts, " ")
}

func getFlagValue(cmd *spcobra.Command, flagName, typ string) (any, error) {
	switch typ {
	case "integer", "int", "int32", "int64", "number":
		return cmd.Flags().GetInt64(flagName)
	case "boolean", "bool":
		return cmd.Flags().GetBool(flagName)
	case "array":
		return cmd.Flags().GetStringSlice(flagName)
	case "object":
		value, err := cmd.Flags().GetString(flagName)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(value) == "" {
			return map[string]any{}, nil
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(value), &payload); err != nil {
			return nil, fmt.Errorf("parse --%s as JSON object: %w", flagName, err)
		}
		return payload, nil
	default:
		return cmd.Flags().GetString(flagName)
	}
}

func castStringValue(value string, typ string) any {
	switch typ {
	case "integer", "int", "int32", "int64", "number":
		if parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
			return parsed
		}
	case "boolean", "bool":
		if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return value
}

func readRawJSONInput(jsonInput string) (any, error) {
	var raw any
	jsonData := jsonInput

	if jsonInput == "-" {
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		jsonData = string(data)
	} else if strings.HasPrefix(jsonInput, "@") {
		data, err := os.ReadFile(jsonInput[1:])
		if err != nil {
			return nil, fmt.Errorf("read file: %w", err)
		}
		jsonData = string(data)
	}

	if err := json.Unmarshal([]byte(jsonData), &raw); err != nil {
		return nil, fmt.Errorf("parse JSON input: %w", err)
	}

	return raw, nil
}

func readRawStringInput(value string) (string, error) {
	if value == "-" {
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(data), nil
	}
	if strings.HasPrefix(value, "@") {
		data, err := os.ReadFile(value[1:])
		if err != nil {
			return "", fmt.Errorf("read file: %w", err)
		}
		return string(data), nil
	}
	return value, nil
}

func readRawBytesInput(path string) ([]byte, error) {
	if path == "-" {
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return data, nil
	}
	return os.ReadFile(path)
}

func outputFormat(cmd *spcobra.Command) OutputFormat {
	value, _ := cmd.Root().PersistentFlags().GetString("output")
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", "text", "table", "csv":
		return OutputText
	default:
		return OutputFormat(value)
	}
}

func quietMode(cmd *spcobra.Command) bool {
	value, _ := cmd.Root().PersistentFlags().GetBool("quiet")
	return value
}

func commandPathPrefix(shorter []string, longer []string) bool {
	if len(shorter) >= len(longer) {
		return false
	}
	for i := range shorter {
		if shorter[i] != longer[i] {
			return false
		}
	}
	return true
}
