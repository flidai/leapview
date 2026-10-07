// Package clidoc builds the public command contract without executing commands.
package clidoc

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	SchemaVersion          = 2
	EffectAnnotation       = "leapview.dev/effect"
	ConfirmationAnnotation = "leapview.dev/confirmation"
	HelpGroupAnnotation    = "leapview.dev/help-group"
	OutputAnnotation       = "leapview.dev/output"
	FramingAnnotation      = "leapview.dev/framing"
)

type Manifest struct {
	SchemaVersion int       `json:"schemaVersion"`
	Commands      []Command `json:"commands"`
}

type Command struct {
	ID               string   `json:"id"`
	Path             []string `json:"path"`
	Title            string   `json:"title"`
	Summary          string   `json:"summary"`
	Description      string   `json:"description"`
	Usage            string   `json:"usage"`
	Runnable         bool     `json:"runnable"`
	Effect           string   `json:"effect"`
	Confirmation     string   `json:"confirmation"`
	Arguments        []string `json:"arguments"`
	Options          []Option `json:"options"`
	InheritedOptions []Option `json:"inheritedOptions"`
	Examples         []string `json:"examples"`
	Subcommands      []string `json:"subcommands"`
	Output           Output   `json:"output"`
}

type Option struct {
	Name        string `json:"name"`
	Shorthand   string `json:"shorthand,omitempty"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// Output describes the default result encoding and the supported stream framing.
type Output struct {
	DefaultFormat string       `json:"defaultFormat"`
	Modes         []OutputMode `json:"modes"`
}
type OutputMode struct {
	Format  string `json:"format"`
	Framing string `json:"framing"`
}

// Build reads the finalized Cobra tree. It performs no I/O or command execution.
func Build(root *cobra.Command) (Manifest, error) {
	manifest := Manifest{SchemaVersion: SchemaVersion, Commands: []Command{}}
	var visit func(*cobra.Command, []string) error
	visit = func(command *cobra.Command, path []string) error {
		if command.Hidden {
			return nil
		}
		entry, err := commandFrom(command, path)
		if err != nil {
			return err
		}
		manifest.Commands = append(manifest.Commands, entry)
		for _, child := range command.Commands() {
			if err := visit(child, append(append([]string(nil), path...), child.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root, []string{}); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}
func commandID(path []string) string {
	if len(path) == 0 {
		return "root"
	}
	return strings.Join(path, "-")
}
func visibleChildren(command *cobra.Command) []*cobra.Command {
	children := []*cobra.Command{}
	for _, child := range command.Commands() {
		if !child.Hidden {
			children = append(children, child)
		}
	}
	return children
}
func commandOutput(command *cobra.Command) Output {
	formats := []string{"text"}
	declared := command.Annotations[OutputAnnotation]
	if declared != "" {
		formats = strings.Split(declared, ",")
	}
	defaultFormat := formats[0]
	if flag := command.Flags().Lookup("format"); flag != nil {
		defaultFormat = flag.DefValue
		if declared == "" {
			formats = []string{defaultFormat}
			if defaultFormat == "text" {
				formats = []string{"text", "json"}
			}
		}
	}
	output := Output{DefaultFormat: defaultFormat, Modes: []OutputMode{}}
	for _, format := range formats {
		framing := "document"
		switch format {
		case "text":
			framing = "lines"
		case "raw":
			framing = "bytes"
		}
		if declaredFraming := command.Annotations[FramingAnnotation]; declaredFraming != "" && format != "text" {
			framing = declaredFraming
		}
		output.Modes = append(output.Modes, OutputMode{Format: format, Framing: framing})
	}
	if command.Parent() == nil && command.LocalFlags().Lookup("llms") != nil {
		output.Modes = append(output.Modes, OutputMode{Format: "markdown", Framing: "document"})
	}
	return output
}

func commandFrom(command *cobra.Command, path []string) (Command, error) {
	// Merge persistent flags before deriving usage; Cobra does this lazily.
	options := optionsFromFlags(command.LocalFlags())
	inherited := optionsFromFlags(command.InheritedFlags())
	effect, confirmation := "none", "never"
	runnable := Runnable(command)
	if runnable {
		effect = command.Annotations[EffectAnnotation]
		if effect == "" {
			return Command{}, fmt.Errorf("command %q is runnable but missing %s annotation", command.CommandPath(), EffectAnnotation)
		}
		confirmation = command.Annotations[ConfirmationAnnotation]
		if confirmation == "" {
			confirmation = defaultConfirmation(effect)
		}
	}
	description := strings.TrimSpace(command.Long)
	if description == "" {
		description = command.Short
	}
	children := visibleChildren(command)
	subcommands := make([]string, 0, len(children))
	for _, child := range children {
		subcommands = append(subcommands, strings.Join(append(append([]string(nil), path...), child.Name()), "-"))
	}
	examples := []string{}
	for _, line := range strings.Split(strings.TrimSpace(command.Example), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			examples = append(examples, line)
		}
	}
	return Command{
		ID:               commandID(path),
		Path:             append([]string{}, path...),
		Title:            command.CommandPath(),
		Summary:          command.Short,
		Description:      description,
		Usage:            command.UseLine(),
		Runnable:         runnable,
		Effect:           effect,
		Confirmation:     confirmation,
		Arguments:        commandArguments(command.Use),
		Options:          options,
		InheritedOptions: inherited,
		Examples:         examples,
		Subcommands:      subcommands,
		Output:           commandOutput(command),
	}, nil
}

func Runnable(command *cobra.Command) bool {
	return command.Runnable() && command.Annotations[HelpGroupAnnotation] != "true"
}

func defaultConfirmation(effect string) string {
	switch effect {
	case "destructive":
		return "required"
	case "write", "idempotent-write":
		return "conditional"
	default:
		return "never"
	}
}

func commandArguments(use string) []string {
	fields := strings.Fields(use)
	arguments := []string{}
	for _, field := range fields[1:] {
		if (strings.HasPrefix(field, "<") && strings.HasSuffix(field, ">")) ||
			(strings.HasPrefix(field, "[") && strings.HasSuffix(field, "]")) {
			arguments = append(arguments, strings.Trim(field, "<>[]"))
		}
	}
	return arguments
}

func optionsFromFlags(flags *pflag.FlagSet) []Option {
	options := []Option{}
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden {
			return
		}
		options = append(options, Option{
			Name:        flag.Name,
			Shorthand:   flag.Shorthand,
			Type:        flag.Value.Type(),
			Default:     flag.DefValue,
			Description: flag.Usage,
			Required:    len(flag.Annotations[cobra.BashCompOneRequiredFlag]) > 0,
		})
	})
	return options
}
