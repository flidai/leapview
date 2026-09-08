package main

import (
	"encoding/json"
	"fmt"

	ciadapter "github.com/flidai/leapview/internal/app/tools/ciadapter"
	platformci "github.com/flidai/leapview/internal/platform/ci"
)

// marshalHealthReport keeps the report's public JSON contract at the CLI
// boundary. The platform health model uses neutral lane IDs, while existing
// report consumers still expect workflow IDs and the historical plan field.
func marshalHealthReport(report platformci.HealthReport) ([]byte, error) {
	data, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	for _, field := range []string{"selection", "jobs"} {
		if err := rewriteJobMap(document, field); err != nil {
			return nil, err
		}
	}
	if err := rewriteStringArray(document, "alerts"); err != nil {
		return nil, err
	}

	var runs []map[string]json.RawMessage
	if raw, ok := document["runs"]; ok {
		if err := json.Unmarshal(raw, &runs); err != nil {
			return nil, err
		}
		if len(runs) != len(report.Runs) {
			return nil, fmt.Errorf("health report run count changed during serialization")
		}
		for index, run := range runs {
			plan, err := ciadapter.MarshalPlan(report.Runs[index].Plan)
			if err != nil {
				return nil, fmt.Errorf("marshal run %d plan: %w", index, err)
			}
			run["plan"] = plan
			if err := rewriteJobMap(run, "results"); err != nil {
				return nil, err
			}
			for _, field := range []string{"planned_jobs", "expected_jobs", "executed_jobs", "skipped_jobs", "unknown_jobs"} {
				if err := rewriteStringArray(run, field); err != nil {
					return nil, err
				}
			}
			if err := rewriteStringArray(run, "problems"); err != nil {
				return nil, err
			}
		}
		document["runs"], err = json.Marshal(runs)
		if err != nil {
			return nil, err
		}
	}
	return json.MarshalIndent(document, "", "  ")
}

func rewriteJobMap(document map[string]json.RawMessage, field string) error {
	raw, ok := document[field]
	if !ok {
		return nil
	}
	if string(raw) == "null" {
		return nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("decode %s: %w", field, err)
	}
	rewritten := make(map[string]json.RawMessage, len(values))
	for name, value := range values {
		wireName := ciadapter.WorkflowJobID(name)
		if _, exists := rewritten[wireName]; exists {
			return fmt.Errorf("job ID collision while serializing %s: %q", field, wireName)
		}
		rewritten[wireName] = value
	}
	encoded, err := json.Marshal(rewritten)
	if err != nil {
		return err
	}
	document[field] = encoded
	return nil
}

func rewriteStringArray(document map[string]json.RawMessage, field string) error {
	raw, ok := document[field]
	if !ok {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("decode %s: %w", field, err)
	}
	for index, value := range values {
		if field == "alerts" || field == "problems" {
			values[index] = wireJobText(value)
		} else {
			values[index] = ciadapter.WorkflowJobID(value)
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	document[field] = encoded
	return nil
}

func wireJobText(value string) string {
	return ciadapter.WorkflowText(value)
}
