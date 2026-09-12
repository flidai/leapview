package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type ossieOptions struct {
	sourceRoot    string
	input         string
	output        string
	semanticModel string
	format        string
}

// OssieCommand exposes the pinned Apache Ossie interchange through the same
// local project compiler used by validate, plan, and deploy. Import emits a
// native SemanticModel resource to stdout; export emits Ossie JSON or YAML.
func OssieCommand(ctx context.Context) *cobra.Command {
	opts := &ossieOptions{sourceRoot: "dashboards", format: "json"}
	root := &cobra.Command{Use: "semantic-model", Short: "Compile and interchange semantic models"}
	ossie := &cobra.Command{Use: "ossie", Short: "Import or export pinned Apache Ossie documents"}

	importCommand := &cobra.Command{
		Use:   "import [ossie-file]",
		Short: "Import an Ossie document as a native SemanticModel resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("semantic-model ossie import accepts at most one input file")
			}
			if len(args) == 1 {
				if cmd.Flags().Changed("in") {
					return fmt.Errorf("choose either --in or the positional Ossie file")
				}
				opts.input = args[0]
			}
			return runOssieImport(ctx, opts, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	importCommand.Flags().StringVar(&opts.sourceRoot, "source-root", opts.sourceRoot, "analytics source root")
	importCommand.Flags().StringVar(&opts.input, "in", "", "Ossie document path (or - for stdin)")

	exportCommand := &cobra.Command{
		Use:   "export [semantic-model]",
		Short: "Export a compiled SemanticModel as pinned Ossie",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("semantic-model ossie export accepts at most one semantic-model reference")
			}
			if len(args) == 1 {
				if cmd.Flags().Changed("semantic-model") {
					return fmt.Errorf("choose either --semantic-model or the positional semantic-model reference")
				}
				opts.semanticModel = args[0]
			}
			return runOssieExport(ctx, opts, cmd.OutOrStdout())
		},
	}
	exportCommand.Flags().StringVar(&opts.sourceRoot, "source-root", opts.sourceRoot, "analytics source root")
	exportCommand.Flags().StringVar(&opts.semanticModel, "semantic-model", "", "semantic-model name or stable resource ID")
	exportCommand.Flags().StringVar(&opts.format, "format", opts.format, "output format: json or yaml")
	exportCommand.Flags().StringVar(&opts.output, "out", "", "write output to a file instead of stdout")

	ossie.AddCommand(importCommand, exportCommand)
	root.AddCommand(ossie)
	return root
}

func runOssieImport(ctx context.Context, opts *ossieOptions, input io.Reader, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := readOssieInput(opts.input, input)
	if err != nil {
		return err
	}
	model, err := projectcompiler.ImportOssie(opts.sourceRoot, data)
	if err != nil {
		return err
	}
	document := nativeSemanticModelDocument{
		APIVersion: "leapview.dev/v1",
		Kind:       "SemanticModel",
		Metadata: nativeSemanticModelMetadata{
			ID:          "semantic-model:" + model.Name,
			Name:        model.Name,
			DisplayName: model.Title,
			Description: model.Description,
		},
		AIContext: nativeAI(model.AIContext),
		Spec: nativeSemanticModelSpec{
			Datasets:      nativeDatasets(model),
			Relationships: nativeRelationships(model.StructuredRelationships),
			Dimensions:    semanticDimensionSpecs(model.Dimensions, true),
			Filters:       nativeFilters(model.Filters),
			Metrics:       nativeMetrics(model.Metrics, true),
		},
	}
	encoder := yaml.NewEncoder(output)
	defer encoder.Close()
	return encoder.Encode(document)
}

func runOssieExport(ctx context.Context, opts *ossieOptions, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(opts.semanticModel) == "" {
		return fmt.Errorf("semantic-model reference is required")
	}
	var (
		data []byte
		err  error
	)
	switch strings.ToLower(strings.TrimSpace(opts.format)) {
	case "json":
		data, err = projectcompiler.ExportOssie(opts.sourceRoot, opts.semanticModel)
	case "yaml", "yml":
		data, err = projectcompiler.ExportOssieYAML(opts.sourceRoot, opts.semanticModel)
	default:
		return fmt.Errorf("unsupported Ossie output format %q", opts.format)
	}
	if err != nil {
		return err
	}
	if opts.output == "" {
		_, err = output.Write(data)
		return err
	}
	if err := os.WriteFile(opts.output, data, 0o644); err != nil {
		return fmt.Errorf("write Ossie document %q: %w", opts.output, err)
	}
	return nil
}

func readOssieInput(path string, input io.Reader) ([]byte, error) {
	if path == "" || path == "-" {
		data, err := io.ReadAll(input)
		if err != nil {
			return nil, fmt.Errorf("read Ossie stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Ossie document %q: %w", path, err)
	}
	return data, nil
}

type nativeSemanticModelDocument struct {
	APIVersion string                      `yaml:"apiVersion"`
	Kind       string                      `yaml:"kind"`
	Metadata   nativeSemanticModelMetadata `yaml:"metadata"`
	AIContext  *nativeAIContext            `yaml:"aiContext,omitempty"`
	Spec       nativeSemanticModelSpec     `yaml:"spec"`
}

type nativeSemanticModelMetadata struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	DisplayName string `yaml:"displayName,omitempty"`
	Description string `yaml:"description,omitempty"`
}

type nativeSemanticModelSpec struct {
	Datasets      map[string]nativeDataset      `yaml:"datasets"`
	Relationships map[string]nativeRelationship `yaml:"relationships,omitempty"`
	Dimensions    map[string]nativeDimension    `yaml:"dimensions,omitempty"`
	Filters       map[string]nativeFilter       `yaml:"filters,omitempty"`
	Metrics       map[string]nativeMetric       `yaml:"metrics,omitempty"`
}

type nativeDataset struct {
	Model                string                     `yaml:"model"`
	DefaultTimeDimension string                     `yaml:"defaultTimeDimension,omitempty"`
	DisplayName          string                     `yaml:"displayName,omitempty"`
	Description          string                     `yaml:"description,omitempty"`
	AIContext            *nativeAIContext           `yaml:"aiContext,omitempty"`
	Dimensions           map[string]nativeDimension `yaml:"dimensions,omitempty"`
	Metrics              map[string]nativeMetric    `yaml:"metrics,omitempty"`
}

type nativeRelationship struct {
	From        nativeRelationshipEndpoint `yaml:"from"`
	To          nativeRelationshipEndpoint `yaml:"to"`
	Description string                     `yaml:"description,omitempty"`
	AIContext   *nativeAIContext           `yaml:"aiContext,omitempty"`
}

type nativeRelationshipEndpoint struct {
	Dataset string   `yaml:"dataset"`
	Entity  string   `yaml:"entity,omitempty"`
	Fields  []string `yaml:"fields,omitempty"`
}

type nativeDimension struct {
	Label       string                            `yaml:"label,omitempty"`
	Description string                            `yaml:"description,omitempty"`
	AIContext   *nativeAIContext                  `yaml:"aiContext,omitempty"`
	Datatype    semanticmodel.LogicalDataType     `yaml:"datatype,omitempty"`
	Time        *nativeTime                       `yaml:"time,omitempty"`
	Field       string                            `yaml:"field,omitempty"`
	Bindings    map[string]nativeDimensionBinding `yaml:"bindings,omitempty"`
}

type nativeDimensionBinding struct {
	Field string   `yaml:"field"`
	Path  []string `yaml:"path,omitempty"`
}

type nativeFilter struct {
	Field     string           `yaml:"field,omitempty"`
	Operator  string           `yaml:"operator,omitempty"`
	Value     any              `yaml:"value,omitempty"`
	Path      []string         `yaml:"path,omitempty"`
	All       []nativeFilter   `yaml:"all,omitempty"`
	Any       []nativeFilter   `yaml:"any,omitempty"`
	Not       *nativeFilter    `yaml:"not,omitempty"`
	AIContext *nativeAIContext `yaml:"aiContext,omitempty"`
}

type nativeMetric struct {
	Type          string           `yaml:"type"`
	Agg           string           `yaml:"agg,omitempty"`
	Field         string           `yaml:"field,omitempty"`
	Where         []string         `yaml:"where,omitempty"`
	Empty         string           `yaml:"empty,omitempty"`
	TimeDimension string           `yaml:"timeDimension,omitempty"`
	Expression    string           `yaml:"expression,omitempty"`
	Numerator     string           `yaml:"numerator,omitempty"`
	Denominator   string           `yaml:"denominator,omitempty"`
	Label         string           `yaml:"label,omitempty"`
	Description   string           `yaml:"description,omitempty"`
	Unit          string           `yaml:"unit,omitempty"`
	Format        string           `yaml:"format,omitempty"`
	Hidden        bool             `yaml:"hidden,omitempty"`
	AIContext     *nativeAIContext `yaml:"aiContext,omitempty"`
}

type nativeAIContext struct {
	Instructions string   `yaml:"instructions,omitempty"`
	Synonyms     []string `yaml:"synonyms,omitempty"`
	Examples     []string `yaml:"examples,omitempty"`
}

type nativeTime struct {
	NativeGrain string   `yaml:"nativeGrain"`
	Grains      []string `yaml:"grains"`
	Calendar    string   `yaml:"calendar,omitempty"`
	Timezone    string   `yaml:"timezone,omitempty"`
}

func nativeAI(value *semanticmodel.AIContext) *nativeAIContext {
	if value == nil {
		return nil
	}
	return &nativeAIContext{Instructions: value.Instructions, Synonyms: append([]string(nil), value.Synonyms...), Examples: append([]string(nil), value.Examples...)}
}

func nativeDatasets(model *semanticmodel.Model) map[string]nativeDataset {
	result := make(map[string]nativeDataset, len(model.Datasets))
	for name, value := range model.Datasets {
		dataset := nativeDataset{Model: value.Model, DefaultTimeDimension: value.DefaultTimeDimension, DisplayName: value.DisplayName, Description: value.Description, AIContext: nativeAI(value.AIContext)}
		for member, dimension := range model.Dimensions {
			if len(dimension.Bindings) != 1 {
				continue
			}
			binding, ok := dimension.Bindings[name]
			if !ok || len(binding.Path) != 0 || !strings.HasPrefix(binding.Field, name+".") {
				continue
			}
			if dataset.Dimensions == nil {
				dataset.Dimensions = map[string]nativeDimension{}
			}
			local := semanticDimensionSpecs(map[string]semanticmodel.SemanticDimension{member: dimension}, false)[member]
			local.Bindings = nil
			field := strings.TrimPrefix(binding.Field, name+".")
			if field != member {
				local.Field = field
			}
			dataset.Dimensions[member] = local
		}
		for member, metric := range model.Metrics {
			if metric.Type != "aggregate" || metric.Dataset != name || metric.Input == nil {
				continue
			}
			if dataset.Metrics == nil {
				dataset.Metrics = map[string]nativeMetric{}
			}
			local := nativeMetrics(map[string]semanticmodel.Metric{member: metric}, false)[member]
			local.Type, local.Agg = "simple", metric.Aggregation
			local.Field = strings.TrimPrefix(metric.Input.Field, name+".")
			if local.Field == member {
				local.Field = ""
			}
			dataset.Metrics[member] = local
		}
		result[name] = dataset
	}
	return result
}

func nativeRelationships(values map[string]semanticmodel.RelationshipSpec) map[string]nativeRelationship {
	result := make(map[string]nativeRelationship, len(values))
	for name, value := range values {
		result[name] = nativeRelationship{
			From:        nativeRelationshipEndpoint{Dataset: value.From.Dataset, Entity: value.From.Entity, Fields: append([]string(nil), value.From.Fields...)},
			To:          nativeRelationshipEndpoint{Dataset: value.To.Dataset, Entity: value.To.Entity, Fields: append([]string(nil), value.To.Fields...)},
			Description: value.Description, AIContext: nativeAI(value.AIContext),
		}
	}
	return result
}

func semanticDimensionSpecs(values map[string]semanticmodel.SemanticDimension, topLevel bool) map[string]nativeDimension {
	result := make(map[string]nativeDimension, len(values))
	for name, dimension := range values {
		local := false
		if topLevel && len(dimension.Bindings) == 1 {
			for dataset, binding := range dimension.Bindings {
				if len(binding.Path) == 0 && strings.HasPrefix(binding.Field, dataset+".") {
					local = true
				}
			}
		}
		if local {
			continue
		}
		bindings := make(map[string]nativeDimensionBinding, len(dimension.Bindings))
		for dataset, binding := range dimension.Bindings {
			bindings[dataset] = nativeDimensionBinding{Field: binding.Field, Path: append([]string(nil), binding.Path...)}
		}
		spec := nativeDimension{Label: dimension.Label, Description: dimension.Description, AIContext: nativeAI(dimension.AIContext), Datatype: dimension.Datatype, Bindings: bindings}
		if len(dimension.Grains) > 0 || dimension.NativeGrain != "" {
			spec.Time = &nativeTime{NativeGrain: dimension.NativeGrain, Grains: append([]string(nil), dimension.Grains...), Calendar: dimension.Calendar, Timezone: dimension.Timezone}
		}
		result[name] = spec
	}
	return result
}

func nativeFilters(values map[string]semanticmodel.SemanticFilterSpec) map[string]nativeFilter {
	result := make(map[string]nativeFilter, len(values))
	for name, value := range values {
		result[name] = nativeFilterSpec(value)
	}
	return result
}

func nativeFilterSpec(value semanticmodel.SemanticFilterSpec) nativeFilter {
	result := nativeFilter{Field: value.Field, Operator: value.Operator, Value: value.Value, Path: append([]string(nil), value.Path...), AIContext: nativeAI(value.AIContext)}
	if len(value.All) > 0 {
		result.All = make([]nativeFilter, len(value.All))
		for index, child := range value.All {
			result.All[index] = nativeFilterSpec(child)
		}
	}
	if len(value.Any) > 0 {
		result.Any = make([]nativeFilter, len(value.Any))
		for index, child := range value.Any {
			result.Any[index] = nativeFilterSpec(child)
		}
	}
	if value.Not != nil {
		child := nativeFilterSpec(*value.Not)
		result.Not = &child
	}
	return result
}

func nativeMetrics(values map[string]semanticmodel.Metric, topLevel bool) map[string]nativeMetric {
	result := make(map[string]nativeMetric, len(values))
	for name, value := range values {
		if topLevel && value.Type == "aggregate" {
			continue
		}
		metric := nativeMetric{
			Type: value.Type, Where: append([]string(nil), value.Where...), Empty: value.Empty, TimeDimension: value.TimeDimension,
			Expression: value.Expression, Numerator: value.Numerator, Denominator: value.Denominator,
			Label: value.Label, Description: value.Description, Unit: value.Unit, Format: value.Format, Hidden: value.Hidden, AIContext: nativeAI(value.AIContext),
		}
		result[name] = metric
	}
	return result
}
