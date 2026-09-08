// Package ciadapter contains the workflow and artifact compatibility boundary
// for the neutral CI planner and health model.
package ciadapter

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	platformci "github.com/flidai/leapview/internal/platform/ci"
)

//go:embed integration.json
var integrationJSON []byte

type integrationBinding struct {
	PR struct {
		NeutralField string `json:"neutral_field"`
		WireField    string `json:"wire_field"`
	} `json:"pr"`
	Lane struct {
		NeutralID  string `json:"neutral_id"`
		WorkflowID string `json:"workflow_id"`
		Display    string `json:"display"`
	} `json:"lane"`
}

var binding = loadBinding()

var workflowTextTokenRE = regexp.MustCompile(`[[:alnum:]_./-]+`)

func loadBinding() integrationBinding {
	var loaded integrationBinding
	if err := json.Unmarshal(integrationJSON, &loaded); err != nil {
		panic(fmt.Sprintf("load CI integration binding: %v", err))
	}
	if loaded.PR.NeutralField == "" || loaded.PR.WireField == "" ||
		loaded.PR.NeutralField == loaded.PR.WireField || loaded.Lane.NeutralID == "" ||
		loaded.Lane.WorkflowID == "" || loaded.Lane.Display == "" {
		panic("invalid CI integration binding")
	}
	return loaded
}

// MarshalPlan encodes a plan using the stable artifact schema. The neutral
// warehouse field is translated to its historical artifact key only inside
// the two current PR job projections.
func MarshalPlan(plan platformci.Plan) ([]byte, error) {
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, err
	}
	return rewritePRJobField(data, binding.PR.NeutralField, binding.PR.WireField)
}

// DecodePlan decodes a plan artifact strictly. It rejects unknown fields,
// duplicate object keys, collisions between neutral and wire fields, and any
// non-whitespace trailing value.
func DecodePlan(reader io.Reader) (platformci.Plan, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return platformci.Plan{}, err
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return platformci.Plan{}, err
	}
	data, err = rewritePRJobField(data, binding.PR.WireField, binding.PR.NeutralField)
	if err != nil {
		return platformci.Plan{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan platformci.Plan
	if err := decoder.Decode(&plan); err != nil {
		return platformci.Plan{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return platformci.Plan{}, errors.New("CI plan contains trailing data")
		}
		return platformci.Plan{}, fmt.Errorf("CI plan contains trailing data: %w", err)
	}
	return plan, nil
}

// WorkflowJobID converts a neutral lane ID into the current workflow ID.
func WorkflowJobID(neutral string) string {
	if neutral == binding.Lane.NeutralID {
		return binding.Lane.WorkflowID
	}
	return neutral
}

// InternalJobID converts a workflow lane ID into the neutral core ID.
func InternalJobID(workflow string) string {
	if workflow == binding.Lane.WorkflowID {
		return binding.Lane.NeutralID
	}
	return workflow
}

// HealthJobName normalizes GitHub display names at the workflow boundary and
// returns only neutral core lane IDs.
func HealthJobName(display string) string {
	if display == binding.Lane.Display {
		return binding.Lane.NeutralID
	}
	for _, tier := range []string{"PR", "merge queue", "nightly"} {
		if display == binding.Lane.Display+" ("+tier+")" {
			return binding.Lane.NeutralID
		}
	}
	return platformci.HealthJobName(display)
}

// WorkflowText translates neutral lane IDs in generated diagnostics and
// summaries to the names expected by the workflow integration.
func WorkflowText(text string) string {
	return workflowTextTokenRE.ReplaceAllStringFunc(text, WorkflowJobID)
}

// InternalResults converts workflow result keys to neutral lane IDs. Unknown
// keys remain visible so the core gate can reject unexpected evidence.
func InternalResults(workflowResults map[string]string) (map[string]string, error) {
	internal := make(map[string]string, len(workflowResults))
	for workflow, result := range workflowResults {
		if workflow == binding.Lane.NeutralID {
			return nil, fmt.Errorf("workflow results contain neutral lane %q", workflow)
		}
		neutral := InternalJobID(workflow)
		if _, exists := internal[neutral]; exists {
			return nil, fmt.Errorf("workflow results collide for lane %q", neutral)
		}
		internal[neutral] = result
	}
	return internal, nil
}

func rewritePRJobField(data []byte, from, to string) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	prKey, rawPR, exists, err := findJSONField(root, "pr")
	if err != nil {
		return nil, err
	}
	if !exists || bytes.Equal(bytes.TrimSpace(rawPR), []byte("null")) {
		return data, nil
	}
	var pr map[string]json.RawMessage
	if err := json.Unmarshal(rawPR, &pr); err != nil {
		return nil, err
	}
	for _, projection := range []string{"nominal", "effective"} {
		projectionKey, rawJobs, exists, err := findJSONField(pr, projection)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		var jobs map[string]json.RawMessage
		if err := json.Unmarshal(rawJobs, &jobs); err != nil {
			return nil, err
		}
		sourceKey, value, sourceExists, err := findJSONField(jobs, from)
		if err != nil {
			return nil, err
		}
		destinationKey, _, destinationExists, err := findJSONField(jobs, to)
		if err != nil {
			return nil, err
		}
		if from == binding.PR.WireField && destinationExists {
			return nil, fmt.Errorf("CI plan PR %s contains neutral field %q; expected wire field %q", projection, destinationKey, from)
		}
		if !sourceExists {
			continue
		}
		if destinationExists {
			return nil, fmt.Errorf("CI plan PR %s contains both %q and %q", projection, sourceKey, destinationKey)
		}
		jobs[to] = value
		delete(jobs, sourceKey)
		encoded, err := json.Marshal(jobs)
		if err != nil {
			return nil, err
		}
		pr[projectionKey] = encoded
	}
	root[prKey], err = json.Marshal(pr)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(root, "", "  ")
}

func findJSONField(values map[string]json.RawMessage, wanted string) (string, json.RawMessage, bool, error) {
	var key string
	var value json.RawMessage
	for candidate, candidateValue := range values {
		if !strings.EqualFold(candidate, wanted) {
			continue
		}
		if key != "" {
			return "", nil, false, fmt.Errorf("JSON fields %q and %q collide", key, candidate)
		}
		key, value = candidate, candidateValue
	}
	return key, value, key != "", nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder, "$"); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("CI plan contains trailing data")
		}
		return fmt.Errorf("CI plan contains trailing data: %w", err)
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("invalid object key at %s", path)
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate JSON field %q at %s", key, path)
				}
				seen[key] = struct{}{}
				if err := scanJSONValue(decoder, path+"."+key); err != nil {
					return err
				}
			}
		case '[':
			for index := 0; decoder.More(); index++ {
				if err := scanJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q at %s", delimiter, path)
		}
		if _, err := decoder.Token(); err != nil {
			return err
		}
	}
	return nil
}
