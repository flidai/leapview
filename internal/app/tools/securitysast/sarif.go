package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type sarifMessage struct {
	Text     string `json:"text"`
	Markdown string `json:"markdown"`
	ID       string `json:"id"`
}
type sarifDescriptor struct {
	ID string `json:"id"`
}
type sarifComponent struct {
	Name          string            `json:"name"`
	Notifications []sarifDescriptor `json:"notifications"`
}
type sarifNotification struct {
	Level      *string      `json:"level"`
	Message    sarifMessage `json:"message"`
	Descriptor struct {
		ID            string `json:"id"`
		Index         *int   `json:"index"`
		ToolComponent *struct {
			Index *int   `json:"index"`
			Name  string `json:"name"`
		} `json:"toolComponent"`
	} `json:"descriptor"`
	Locations []struct {
		PhysicalLocation struct {
			ArtifactLocation struct {
				URI string `json:"uri"`
			} `json:"artifactLocation"`
			Region struct {
				StartLine int `json:"startLine"`
			} `json:"region"`
		} `json:"physicalLocation"`
	} `json:"locations"`
}
type sarifRun struct {
	Tool struct {
		Driver     sarifComponent   `json:"driver"`
		Extensions []sarifComponent `json:"extensions"`
	} `json:"tool"`
	AutomationDetails struct {
		ID string `json:"id"`
	} `json:"automationDetails"`
	Invocations []struct {
		ExecutionSuccessful *bool               `json:"executionSuccessful"`
		Execution           []sarifNotification `json:"toolExecutionNotifications"`
		Configuration       []sarifNotification `json:"toolConfigurationNotifications"`
	} `json:"invocations"`
}

func validateSARIF(data []byte, category string) error {
	queryPack := map[string]string{
		"/language:go":                    "codeql/go-queries",
		"/language:javascript-typescript": "codeql/javascript-queries",
	}[category]
	if queryPack == "" {
		return fmt.Errorf("unsupported CodeQL category %q", category)
	}
	var report struct {
		Version string     `json:"version"`
		Runs    []sarifRun `json:"runs"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("invalid raw SARIF: %w", err)
	}
	if report.Version != "2.1.0" || len(report.Runs) == 0 {
		return fmt.Errorf("expected SARIF 2.1.0 with CodeQL runs")
	}
	var failures []error
	for runIndex, run := range report.Runs {
		where := fmt.Sprintf("SARIF run %d", runIndex+1)
		if run.Tool.Driver.Name != "CodeQL" {
			failures = append(failures, fmt.Errorf("%s: expected raw CodeQL output", where))
		}
		// The category is a workflow-supplied label, not proof of which
		// language ran. The pinned analyzer records its actual query packs.
		foundQueryPack := false
		for _, extension := range run.Tool.Extensions {
			if extension.Name == queryPack {
				foundQueryPack = true
			}
		}
		if !foundQueryPack {
			failures = append(failures, fmt.Errorf("%s: missing language query pack %q", where, queryPack))
		}
		// SARIF uses the final slash-separated segment as the automation run ID.
		id := run.AutomationDetails.ID
		if !strings.HasPrefix(id, category+"/") || strings.Contains(strings.TrimPrefix(id, category+"/"), "/") {
			failures = append(failures, fmt.Errorf("%s: unexpected category/run ID %q", where, id))
		}
		if len(run.Invocations) == 0 {
			failures = append(failures, fmt.Errorf("%s: missing raw invocation diagnostics", where))
		}
		for invocationIndex, invocation := range run.Invocations {
			invocationWhere := fmt.Sprintf("%s invocation %d", where, invocationIndex+1)
			if invocation.ExecutionSuccessful == nil || !*invocation.ExecutionSuccessful {
				failures = append(failures, fmt.Errorf("%s: execution success is not established", invocationWhere))
			}
			for _, notifications := range [][]sarifNotification{invocation.Execution, invocation.Configuration} {
				for _, notification := range notifications {
					level := "warning" // SARIF 2.1.0 notification.level default.
					if notification.Level != nil {
						level = *notification.Level
					}
					id, err := notificationID(run, notification)
					if err != nil {
						failures = append(failures, fmt.Errorf("%s: %w", invocationWhere, err))
						continue
					}
					switch level {
					case "note", "none":
						continue
					case "warning", "error":
						message := notification.Message.Text
						if message == "" {
							message = notification.Message.Markdown
						}
						if message == "" {
							message = notification.Message.ID
						}
						var locations []string
						for _, loc := range notification.Locations {
							locations = append(locations, fmt.Sprintf("%s:%d", loc.PhysicalLocation.ArtifactLocation.URI, loc.PhysicalLocation.Region.StartLine))
						}
						failures = append(failures, fmt.Errorf("%s: %s %s: %s %s", invocationWhere, level, id, message, strings.Join(locations, ", ")))
					default:
						failures = append(failures, fmt.Errorf("%s: invalid diagnostic level %q", invocationWhere, level))
					}
				}
			}
		}
	}
	return errors.Join(failures...)
}

func notificationID(run sarifRun, notification sarifNotification) (string, error) {
	ref := notification.Descriptor
	component := run.Tool.Driver
	if ref.ToolComponent != nil {
		if ref.ToolComponent.Index != nil {
			index := *ref.ToolComponent.Index
			if index < 0 || index >= len(run.Tool.Extensions) {
				return "", fmt.Errorf("invalid diagnostic tool component index %d", index)
			}
			component = run.Tool.Extensions[index]
		} else if ref.ToolComponent.Name != "" && ref.ToolComponent.Name != component.Name {
			found := false
			for _, extension := range run.Tool.Extensions {
				if extension.Name == ref.ToolComponent.Name {
					component, found = extension, true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("unknown diagnostic tool component %q", ref.ToolComponent.Name)
			}
		}
		if ref.ToolComponent.Name != "" && ref.ToolComponent.Name != component.Name {
			return "", fmt.Errorf("diagnostic component name/index mismatch")
		}
	}
	if ref.Index != nil {
		index := *ref.Index
		if index < 0 || index >= len(component.Notifications) {
			return "", fmt.Errorf("invalid diagnostic descriptor index %d", index)
		}
		id := component.Notifications[index].ID
		if id == "" || (ref.ID != "" && ref.ID != id) {
			return "", fmt.Errorf("diagnostic descriptor id/index mismatch")
		}
		return id, nil
	}
	if ref.ID != "" {
		return ref.ID, nil
	}
	return "unidentified diagnostic", nil
}
