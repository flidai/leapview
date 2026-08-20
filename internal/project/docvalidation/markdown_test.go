package docvalidation

import (
	"strings"
	"testing"
)

func TestValidateMarkdownChecksYAMLSyntaxAndResourceSchemas(t *testing.T) {
	t.Parallel()

	markdown := strings.Join([]string{
		"# Examples",
		"",
		"```yaml",
		"filters:",
		"  state: [",
		"```",
		"",
		"```yaml",
		"apiVersion: leapview.dev/v1",
		"kind: Project",
		"metadata:",
		"  id: project:commerce",
		"  name: commerce",
		"spec:",
		"  unsupported: true",
		"```",
	}, "\n")

	issues := ValidateMarkdown("docs/example.md", []byte(markdown))
	if len(issues) != 2 {
		t.Fatalf("issues = %#v, want syntax and schema issues", issues)
	}
	if issues[0].File != "docs/example.md" || issues[0].Line != 5 || !strings.Contains(issues[0].Message, "YAML") {
		t.Errorf("syntax issue = %#v", issues[0])
	}
	if issues[1].File != "docs/example.md" || issues[1].Line != 15 || !strings.Contains(issues[1].Message, "unsupported") {
		t.Errorf("schema issue = %#v", issues[1])
	}
}

func TestValidateMarkdownAcceptsPartialAndValidResourceExamples(t *testing.T) {
	t.Parallel()

	markdown := `# Examples

~~~yaml
filters:
  state:
    label: State
    field: customer_state
    predicates:
      - kind: set
        operators: [in]
~~~

` + "```yaml\n" + `apiVersion: leapview.dev/v1
kind: Project
metadata:
  id: project:commerce
  name: commerce
spec:
  connections:
    include: [connections/*.yaml]
  sources:
    include: [sources/*.yaml]
  models:
    include: [models/*.yaml]
  semanticModels:
    include: [semantic-models/*.yaml]
  pipelines:
    include: [pipelines/*.yaml]
  dashboards:
    include: [dashboards/*.yaml]
  access:
    include: [access/*.yaml]
` + "```\n\n```yaml\n" + `apiVersion: apps/v1
kind: Deployment
metadata:
  name: leapview
` + "```\n"

	if issues := ValidateMarkdown("docs/example.md", []byte(markdown)); len(issues) != 0 {
		t.Fatalf("issues = %#v, want none", issues)
	}
}

func TestValidateMarkdownAcceptsPipelineResources(t *testing.T) {
	t.Parallel()

	markdown := "```yaml\n" + `apiVersion: leapview.dev/v1
kind: Pipeline
metadata:
  id: pipeline:sales-refresh
  name: sales-refresh
spec:
  selection:
    type: semanticModel
    semanticModel: sales
  triggers:
    - id: weekdays-0600
      type: schedule
      cron: "0 6 * * *"
      timezone: Europe/Copenhagen
      missedOccurrences: latest
  runPolicy:
    overlap: replace
` + "```\n"

	if issues := ValidateMarkdown("docs/example.md", []byte(markdown)); len(issues) != 0 {
		t.Fatalf("issues = %#v, want none", issues)
	}
}

func TestValidateMarkdownRejectsIncompleteResourceEnvelopesAndUnclosedFences(t *testing.T) {
	t.Parallel()

	markdown := `# Examples

` + "```yaml\n" + `apiVersion: leapview.dev/v1
metadata:
  name: commerce
` + "```\n\n```yaml\nkey: value\n"

	issues := ValidateMarkdown("docs/example.md", []byte(markdown))
	if len(issues) != 2 {
		t.Fatalf("issues = %#v, want envelope and fence issues", issues)
	}
	if issues[0].Line != 4 || !strings.Contains(issues[0].Message, "apiVersion and kind") {
		t.Errorf("envelope issue = %#v", issues[0])
	}
	if issues[1].Line != 9 || !strings.Contains(issues[1].Message, "unclosed") {
		t.Errorf("fence issue = %#v", issues[1])
	}
}
