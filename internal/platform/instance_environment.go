package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const instanceEnvironmentSetting = "instance.environment"

// BindInstanceEnvironment permanently associates a platform store with one
// serving environment. Existing active pointers may be adopted only when they
// all match the requested environment.
func (s *Store) BindInstanceEnvironment(ctx context.Context, environment string) error {
	environment = strings.TrimSpace(environment)
	if environment == "" {
		return fmt.Errorf("instance environment is required")
	}
	current, err := s.GetSetting(ctx, instanceEnvironmentSetting)
	if err == nil {
		if current != environment {
			return environmentMismatch(current, environment)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read instance environment: %w", err)
	}

	environments, err := s.activeEnvironments(ctx)
	if err != nil {
		return err
	}
	for _, active := range environments {
		if active != environment {
			return fmt.Errorf("cannot bind instance to environment %q: existing active state belongs to %q; use a separate instance home or redeploy the project", environment, active)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO platform_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`, instanceEnvironmentSetting, environment); err != nil {
		return fmt.Errorf("bind instance environment: %w", err)
	}
	current, err = s.GetSetting(ctx, instanceEnvironmentSetting)
	if err != nil {
		return fmt.Errorf("verify instance environment: %w", err)
	}
	if current != environment {
		return environmentMismatch(current, environment)
	}
	return nil
}

func (s *Store) InstanceEnvironment(ctx context.Context) (string, error) {
	value, err := s.GetSetting(ctx, instanceEnvironmentSetting)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

// ValidateDatabaseInstanceEnvironment verifies a database-only backup before
// it can replace an instance control database. Legacy databases without a
// binding may be adopted only when their active pointers are empty or match.
func ValidateDatabaseInstanceEnvironment(ctx context.Context, path, expected string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return fmt.Errorf("expected instance environment is required")
	}
	if err := validateBackupDatabase(ctx, path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=query_only(1)")
	if err != nil {
		return err
	}
	defer db.Close()
	var bound string
	err = db.QueryRowContext(ctx, `SELECT value FROM platform_settings WHERE key = ?`, instanceEnvironmentSetting).Scan(&bound)
	if err == nil {
		bound = strings.TrimSpace(bound)
		if bound != expected {
			return environmentMismatch(bound, expected)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read backup instance environment: %w", err)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT environment FROM project_active_serving_states
		UNION
		SELECT environment FROM managed_data_environment_pointers
	`)
	if err != nil {
		return fmt.Errorf("inspect backup active environments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var active string
		if err := rows.Scan(&active); err != nil {
			return err
		}
		active = strings.TrimSpace(active)
		if active != "" && active != expected {
			return fmt.Errorf("cannot restore database into environment %q: backup active state belongs to %q; use a separate instance home", expected, active)
		}
	}
	return rows.Err()
}

func (s *Store) activeEnvironments(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT environment FROM project_active_serving_states
		UNION
		SELECT environment FROM managed_data_environment_pointers
	`)
	if err != nil {
		return nil, fmt.Errorf("inspect active instance environments: %w", err)
	}
	defer rows.Close()
	var environments []string
	for rows.Next() {
		var environment string
		if err := rows.Scan(&environment); err != nil {
			return nil, err
		}
		if environment = strings.TrimSpace(environment); environment != "" {
			environments = append(environments, environment)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(environments)
	return environments, nil
}

func environmentMismatch(bound, requested string) error {
	return fmt.Errorf("instance is bound to environment %q, not %q; use a separate instance home for another environment", bound, requested)
}
