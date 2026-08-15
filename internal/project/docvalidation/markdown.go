// Package docvalidation validates executable examples embedded in Markdown.
package docvalidation

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/project/schema"
	"gopkg.in/yaml.v3"
)

var yamlErrorLine = regexp.MustCompile(`\bline ([0-9]+)\b`)

var schemaKinds = map[string]configschema.Kind{
	"Project":              configschema.KindProject,
	"Connection":           configschema.KindConnection,
	"Source":               configschema.KindSource,
	"Group":                configschema.KindGroup,
	"RoleBinding":          configschema.KindRoleBinding,
	"Grant":                configschema.KindGrant,
	"DataPolicy":           configschema.KindDataPolicy,
	"Pipeline":             configschema.KindPipeline,
	"Model":                configschema.KindModel,
	"SemanticModel":        configschema.KindSemanticModel,
	"Dashboard":            configschema.KindDashboard,
	"DashboardPublication": configschema.KindDashboardPublication,
}

// Issue identifies an invalid Markdown example at its authored location.
type Issue struct {
	File    string
	Line    int
	Column  int
	Message string
}

func (i Issue) String() string {
	location := fmt.Sprintf("%s:%d", i.File, i.Line)
	if i.Column > 0 {
		location += fmt.Sprintf(":%d", i.Column)
	}
	return location + ": " + i.Message
}

type markdownFence struct {
	marker byte
	length int
	info   string
}

// ValidateMarkdown syntax-checks every YAML fence and schema-checks complete
// LeapView resources containing an apiVersion and kind envelope.
func ValidateMarkdown(filename string, content []byte) []Issue {
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	var issues []Issue
	for index := 0; index < len(lines); index++ {
		fence, ok := openingFence(lines[index])
		if !ok {
			continue
		}
		closing := -1
		for candidate := index + 1; candidate < len(lines); candidate++ {
			if closesFence(lines[candidate], fence) {
				closing = candidate
				break
			}
		}
		if closing < 0 {
			if yamlFence(fence.info) {
				issues = append(issues, Issue{File: filename, Line: index + 1, Message: "unclosed YAML code fence"})
			}
			break
		}
		if yamlFence(fence.info) {
			block := strings.Join(lines[index+1:closing], "\n") + "\n"
			issues = append(issues, validateYAMLBlock(filename, index+2, []byte(block))...)
		}
		index = closing
	}
	return issues
}

func openingFence(line string) (markdownFence, bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return markdownFence{}, false
	}
	length := markerLength(trimmed, trimmed[0])
	if length < 3 {
		return markdownFence{}, false
	}
	info := strings.TrimSpace(trimmed[length:])
	if trimmed[0] == '`' && strings.ContainsRune(info, '`') {
		return markdownFence{}, false
	}
	return markdownFence{marker: trimmed[0], length: length, info: info}, true
}

func closesFence(line string, fence markdownFence) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || markerLength(trimmed, fence.marker) < fence.length {
		return false
	}
	return strings.TrimSpace(trimmed[markerLength(trimmed, fence.marker):]) == ""
}

func markerLength(value string, marker byte) int {
	length := 0
	for length < len(value) && value[length] == marker {
		length++
	}
	return length
}

func yamlFence(info string) bool {
	fields := strings.Fields(info)
	if len(fields) == 0 {
		return false
	}
	language := strings.ToLower(fields[0])
	return language == "yaml" || language == "yml"
}

func validateYAMLBlock(filename string, contentLine int, content []byte) []Issue {
	root, err := decodeSingleYAMLDocument(content)
	if err != nil {
		return []Issue{{
			File:    filename,
			Line:    contentLine + relativeYAMLErrorLine(err) - 1,
			Message: "invalid YAML example: " + err.Error(),
		}}
	}
	apiVersion, apiVersionLine := mappingScalar(root, "apiVersion")
	kind, kindLine := mappingScalar(root, "kind")
	if (apiVersion != "") != (kind != "") {
		relativeLine := apiVersionLine
		if relativeLine == 0 {
			relativeLine = kindLine
		}
		return []Issue{{
			File:    filename,
			Line:    contentLine + relativeLine - 1,
			Message: "LeapView resource examples must include both apiVersion and kind",
		}}
	}
	if apiVersion == "" {
		return nil
	}
	if !strings.HasPrefix(apiVersion, "leapview.dev/") {
		return nil
	}
	schemaKind, ok := schemaKinds[kind]
	if !ok {
		return []Issue{{File: filename, Line: contentLine + kindLine - 1, Message: fmt.Sprintf("unsupported LeapView resource kind %q", kind)}}
	}
	if err := configschema.ValidateBytes(schemaKind, filename, content); err != nil {
		diagnostics := configschema.Diagnostics(err)
		issues := make([]Issue, 0, len(diagnostics))
		for _, diagnostic := range diagnostics {
			line := contentLine
			if diagnostic.Line > 0 {
				line += diagnostic.Line - 1
			}
			message := diagnostic.Message
			if diagnostic.FieldPath != "" {
				message = diagnostic.FieldPath + ": " + message
			}
			issues = append(issues, Issue{File: filename, Line: line, Column: diagnostic.Column, Message: "invalid LeapView " + kind + " example: " + message})
		}
		return issues
	}
	return nil
}

func decodeSingleYAMLDocument(content []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(string(content)))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("multiple YAML documents are not supported in one code fence")
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("empty YAML document")
	}
	return root.Content[0], nil
}

func mappingScalar(root *yaml.Node, key string) (string, int) {
	if root.Kind != yaml.MappingNode {
		return "", 0
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		name, value := root.Content[index], root.Content[index+1]
		if name.Value == key && value.Kind == yaml.ScalarNode {
			return value.Value, name.Line
		}
	}
	return "", 0
}

func relativeYAMLErrorLine(err error) int {
	match := yamlErrorLine.FindStringSubmatch(err.Error())
	if len(match) != 2 {
		return 1
	}
	line, parseErr := strconv.Atoi(match[1])
	if parseErr != nil || line < 1 {
		return 1
	}
	return line
}
