package clidoc

import (
	"fmt"
	"io"
	"strings"
)

// WriteAgentGuide renders the same catalog used by the public documentation.
func WriteAgentGuide(w io.Writer, manifest Manifest, version string) error {
	var out strings.Builder
	fmt.Fprintf(&out, "# LeapView CLI agent guide\n\nBinary version: `%s`. CLI catalog schema: %d.\n\n", version, manifest.SchemaVersion)
	out.WriteString(`LeapView is dashboards as code. Start with command help to choose the workflow; bare ` + "`leapview`" + ` prints help. Start a server explicitly with ` + "`leapview serve`" + `.

## Authoring flow

` + "```sh\nleapview init ./my-analytics\ncd ./my-analytics\nleapview doctor\nleapview dev\nleapview validate --format json\n```" + `

Edit analytics resources under dashboards/. Local development uses the selected development profile and a verified local Docker endpoint. It ignores ambient production targets. Contributor ` + "`task dev`" + ` runs the repository development server; analytics authors use ` + "`leapview dev`" + `.

## Target and deployment flow

Choose an explicit target before remote work. Saved names resolve through the CLI profile file; a URL must identify the intended instance. Treat instance and environment mismatches as failures. Local doctor runs locally unless --target is supplied.

` + "```sh\nleapview login https://target.example --no-browser\nleapview doctor --target https://target.example --format json\nleapview deploy --target production --new --format json --no-input\n```" + `

Use a supported API token or workload identity for unattended authoring. Device login produces a challenge that a person must approve; JSON output does not make login unattended. Doctor only checks authentication when an API token is supplied and never refreshes saved OAuth credentials.

Deploy is a guided operation with a durable handle. Retain the returned operation handle and exact plan. Follow the command's next action with --resume and the retained handle; do not create a new operation to retry the same work. Confirm the exact plan with --confirm-plan only after inspecting it. Server approval is independent of client confirmation. Plan creates target state; it is not a read-only diagnostic. Build and publish completion does not prove activation. Deploy succeeds only after confirmed activation.

## Output and interaction

Commands with selectable result output use --format text|json. There is no --json alias and no global --format flag. Schema and Ossie export formats describe artifact encoding. Fixed JSON and raw commands are identified in the catalog below.

Results go to stdout; progress, prompts, and diagnostics go to stderr. Finite JSON output is one document. Login JSON output is newline-delimited events; development watch is an event stream, while --once emits a finite result. --no-input and JSON output disable prompts and automatic browser opening. When confirmation is required, follow the reported next action rather than piping a guessed answer.

Exit codes: 0 success; 1 execution, validation, authentication, or required diagnostic failure; 2 invalid invocation or command selection; 3 deployment awaiting confirmation or approval; 4 indeterminate deployment; 130 client interruption by SIGINT; 143 client interruption by SIGTERM. Successfully drained serve shutdown exits 0. A structured domain failure already written to stdout is not followed by a second result.

## Recovery

Use doctor for prerequisite and target inspection, validate for source errors, and dev status/logs for an existing local runtime. Doctor does not repair, start containers, pull images, query upstream data, or create deployment plans. A skipped check means unverified. Preserve operation handles when publication is pending or indeterminate and inspect/resume the retained operation. Do not assume a timed-out client means the server failed.

## Public command catalog

The catalog is generated from the installed command tree. Inspect ` + "`leapview <command> --help`" + ` for flag details and examples. The website exposes /docs/cli/manifest.json, /docs/cli/commands/{id}.json and .md, and the read-only documentation MCP at /mcp.

| Command | Effect | Confirmation | Output (framing) |
| --- | --- | --- | --- |
`)
	for _, command := range manifest.Commands {
		modes := []string{}
		for _, mode := range command.Output.Modes {
			modes = append(modes, mode.Format+" ("+mode.Framing+")")
		}
		fmt.Fprintf(&out, "| `%s` | %s | %s | %s |\n", escapeTable(command.Usage), command.Effect, command.Confirmation, escapeTable(strings.Join(modes, ", ")))
	}
	out.WriteString("\n## Root options\n\n")
	if len(manifest.Commands) > 0 {
		for _, option := range manifest.Commands[0].Options {
			fmt.Fprintf(&out, "- `--%s` (%s, default `%s`): %s\n", option.Name, option.Type, option.Default, option.Description)
		}
	}
	_, err := io.WriteString(w, out.String())
	return err
}
func escapeTable(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}
