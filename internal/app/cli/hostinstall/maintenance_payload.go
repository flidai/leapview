package hostinstall

import (
	"bytes"
	"fmt"
	"reflect"

	"gopkg.in/yaml.v3"
)

// The installed configuration must still be the immutable predecessor payload.
// Only the application's healthcheck executable may evolve during maintenance;
// the candidate must use the current product command. All other topology and
// healthcheck settings remain equal before any capture or migration begins.
func validateMaintenancePayloadTransition(installed, predecessor, candidate map[string][]byte) error {
	for _, name := range []string{"compose.yaml", "compose.https.yaml", "Caddyfile", "deployment.env.example"} {
		if len(predecessor[name]) == 0 || len(candidate[name]) == 0 || !bytes.Equal(installed[name], predecessor[name]) {
			return fmt.Errorf("installed deployment differs from predecessor payload: %s", name)
		}
		if name == "compose.yaml" {
			if err := validateMaintenanceCompose(predecessor[name], candidate[name]); err != nil {
				return fmt.Errorf("deployment topology changed: %s: %w", name, err)
			}
		} else if !bytes.Equal(predecessor[name], candidate[name]) {
			return fmt.Errorf("deployment topology changed: %s", name)
		}
	}
	return nil
}

func validateMaintenanceCompose(predecessor, candidate []byte) error {
	var before, after map[string]any
	if err := yaml.Unmarshal(predecessor, &before); err != nil {
		return fmt.Errorf("invalid predecessor Compose: %w", err)
	}
	if err := yaml.Unmarshal(candidate, &after); err != nil {
		return fmt.Errorf("invalid candidate Compose: %w", err)
	}
	healthcheck := func(document map[string]any) (map[string]any, error) {
		for _, key := range []string{"services", "leapview", "healthcheck"} {
			child, ok := document[key].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("missing application healthcheck mapping: %s", key)
			}
			document = child
		}
		return document, nil
	}
	oldHealth, err := healthcheck(before)
	if err != nil {
		return err
	}
	newHealth, err := healthcheck(after)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(newHealth["test"], []any{"CMD", "/usr/local/bin/leapview", "healthcheck"}) {
		return fmt.Errorf("candidate must use the canonical application healthcheck")
	}
	delete(oldHealth, "test")
	delete(newHealth, "test")
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("configuration differs outside the application healthcheck command")
	}
	return nil
}
