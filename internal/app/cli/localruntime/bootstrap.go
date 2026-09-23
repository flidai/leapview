package localruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

func (controller *Controller) startPostgres(ctx context.Context, statePath, envPath string, state *State) error {
	for attempt := 0; attempt < maxPortAttempts; attempt++ {
		err := controller.compose(ctx, envPath, nil, "up", "-d", "postgres")
		if err == nil {
			return controller.waitPostgres(ctx, envPath)
		}
		if !isPortConflict(err) || attempt == maxPortAttempts-1 {
			return fmt.Errorf("start local PostgreSQL: %w", err)
		}
		// Compose may have created a stopped container before the publication
		// conflict. Removal remains restricted to this verified project.
		if cleanupErr := controller.compose(ctx, envPath, nil, "rm", "--stop", "--force", "leapview", "postgres"); cleanupErr != nil {
			return fmt.Errorf("reconcile owned containers after port conflict: %w", cleanupErr)
		}
		port, portErr := availablePort()
		if portErr != nil {
			return portErr
		}
		values, readErr := readEnvironment(envPath)
		if readErr != nil {
			return readErr
		}
		values["LEAPVIEW_LOCAL_APP_PORT"] = strconv.Itoa(port)
		state.Network = networkID{AppPort: port, URL: "http://127.0.0.1:" + strconv.Itoa(port)}
		state.Session = sessionID{}
		if writeErr := writeEnvironment(envPath, values); writeErr != nil {
			return writeErr
		}
		if saveErr := saveState(statePath, *state); saveErr != nil {
			return saveErr
		}
	}
	return errors.New("unable to select an available local application port")
}

func isPortConflict(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "port is already allocated") ||
		strings.Contains(message, "address already in use") ||
		strings.Contains(message, "bind: address in use")
}

func (controller *Controller) waitPostgres(ctx context.Context, envPath string) error {
	for attempt := 0; attempt < defaultChecks; attempt++ {
		err := controller.compose(ctx, envPath, nil, "exec", "-T", "postgres", "pg_isready", "--host", "127.0.0.1", "--username", "leapview_control_runtime", "--dbname", "leapview_control", "--quiet")
		if err == nil {
			return nil
		}
		if sleepErr := controller.sleep(ctx, time.Second); sleepErr != nil {
			return sleepErr
		}
	}
	return errors.New("local PostgreSQL did not become ready")
}

func (controller *Controller) initializeInstance(ctx context.Context, root, envPath string) error {
	credentialsPath := filepath.Join(root, credentialsFileName)
	if encoded, err := securefs.ReadPrivateFile(credentialsPath); err == nil {
		if _, err := adminoffline.DecodeInitialCredentials(encoded); err != nil {
			return fmt.Errorf("validate retained initialization credentials: %w", err)
		}
		if err := controller.compose(ctx, envPath, nil, "run", "--rm", "--no-deps", "leapview", "admin", "initialize", "--acknowledge-credentials"); err != nil {
			return fmt.Errorf("acknowledge retained initialization credentials: %w", err)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read retained initialization credentials: %w", err)
	}
	migratorEnvironment, err := controlMigratorEnvironment(envPath)
	if err != nil {
		return err
	}
	output, err := controller.composeOutput(ctx, envPath, migratorEnvironment,
		"run", "--rm", "--no-deps",
		"--env", "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL",
		"--env", "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE",
		"leapview", "admin", "initialize", "--format", "json",
	)
	if err != nil {
		return fmt.Errorf("initialize local instance (bootstrap remains incomplete; correct the migration error and rerun leapview dev): %w", err)
	}
	if _, err := adminoffline.DecodeInitialCredentials(output); err != nil {
		return fmt.Errorf("local instance returned invalid initialization credentials: %w", err)
	}
	if err := securefs.WritePrivateFileAtomic(credentialsPath, output); err != nil {
		return fmt.Errorf("retain local initialization credentials: %w", err)
	}
	if err := controller.compose(ctx, envPath, nil, "run", "--rm", "--no-deps", "leapview", "admin", "initialize", "--acknowledge-credentials"); err != nil {
		return fmt.Errorf("credentials are retained but acknowledgement failed; rerun dev to recover: %w", err)
	}
	return nil
}

func controlMigratorEnvironment(envPath string) ([]string, error) {
	values, err := readEnvironment(envPath)
	if err != nil {
		return nil, err
	}
	password := values["LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD"]
	if password == "" {
		return nil, errors.New("local control migrator credential is unavailable")
	}
	return []string{
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL=postgresql://leapview_control_migrator:" + password + "@127.0.0.1:5432/leapview_control?sslmode=disable",
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE=leapview_control_migrator",
	}, nil
}

func (controller *Controller) qualifyAndAdmitPool(ctx context.Context, root, statePath, envPath string, state *State) error {
	qualificationPath := filepath.Join(root, qualificationFileName)
	poolPath, evidencePath := filepath.Join(root, poolFileName), filepath.Join(root, evidenceFileName)
	var artifacts adminoffline.QualificationPoolArtifacts
	if encoded, readErr := readRegularArtifact(qualificationPath); readErr == nil {
		var err error
		artifacts, err = adminoffline.UnmarshalQualificationPoolArtifacts(encoded)
		if err != nil {
			return fmt.Errorf("validate retained physical-pool qualification: %w", err)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read retained physical-pool qualification: %w", readErr)
	} else {
		output, err := controller.composeOutput(ctx, envPath, nil, "run", "--rm", "--no-deps", "leapview", "admin", "delivery", "pool", "qualify")
		if err != nil {
			return fmt.Errorf("qualify local physical pool: %w", err)
		}
		artifacts, err = adminoffline.UnmarshalQualificationPoolArtifacts(output)
		if err != nil {
			return fmt.Errorf("validate local physical-pool qualification: %w", err)
		}
		encoded, err := adminoffline.MarshalQualificationPoolArtifacts(artifacts)
		if err != nil {
			return err
		}
		if err := writeReadableArtifactAtomic(qualificationPath, append(encoded, '\n')); err != nil {
			return err
		}
	}
	// The single retained envelope is authoritative. Mounted split artifacts
	// are regenerated from it, so interruption between their writes is safe.
	poolBytes, err := json.MarshalIndent(artifacts.Pool, "", "  ")
	if err != nil {
		return err
	}
	evidenceBytes, err := json.MarshalIndent(artifacts.Evidence, "", "  ")
	if err != nil {
		return err
	}
	if err := writeReadableArtifactAtomic(poolPath, append(poolBytes, '\n')); err != nil {
		return err
	}
	if err := writeReadableArtifactAtomic(evidencePath, append(evidenceBytes, '\n')); err != nil {
		return err
	}
	compatibilityDigest, err := artifacts.Evidence.Evidence.Compatibility.Digest()
	if err != nil {
		return err
	}
	pool, err := physicalpool.NewPhysicalPool(artifacts.Pool)
	if err != nil {
		return err
	}
	nextAuthority := authorityID{
		Environment: state.Authority.Environment, IssuerID: state.Authority.IssuerID,
		ProjectUID: state.Authority.ProjectUID, InstanceID: state.Authority.InstanceID,
		PoolID: pool.ID.String(), CompatibilityDigest: compatibilityDigest,
		EvidenceDigest: artifacts.Evidence.Evidence.Digest, ConformanceVersion: artifacts.Evidence.Evidence.ConformanceVersion,
	}
	retainedPoolIntent := state.Authority.PoolID != "" || state.Authority.CompatibilityDigest != "" || state.Authority.EvidenceDigest != "" || state.Authority.ConformanceVersion != ""
	if retainedPoolIntent && state.Authority != nextAuthority {
		return errors.New("retained physical-pool qualification differs from durable admission intent; refusing recapture")
	}
	state.Authority = nextAuthority
	// Persist the exact non-secret admission intent before the first admission
	// mutation. A lost acknowledgement can then only replay these artifacts.
	if err := saveState(statePath, *state); err != nil {
		return err
	}
	values, err := readEnvironment(envPath)
	if err != nil {
		return err
	}
	ducklakePassword := values["LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD"]
	if ducklakePassword == "" {
		return errors.New("local DuckLake migrator credential is unavailable")
	}
	ducklakeURL := "postgresql://leapview_ducklake_migrator:" + ducklakePassword + "@127.0.0.1:5432/leapview_ducklake?sslmode=disable"
	extraEnvironment, err := controlMigratorEnvironment(envPath)
	if err != nil {
		return err
	}
	extraEnvironment = append(extraEnvironment, "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL="+ducklakeURL)
	arguments := []string{
		"run", "--rm", "--no-deps",
		"--volume", poolPath + ":/run/leapview/physical-pool.json:ro",
		"--volume", evidencePath + ":/run/leapview/physical-pool-evidence.json:ro",
		"--env", "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL",
		"--env", "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE",
		"--env", "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL",
		"leapview", "admin", "delivery", "pool", "bootstrap",
		"--pool", "/run/leapview/physical-pool.json",
		"--evidence", "/run/leapview/physical-pool-evidence.json",
	}
	if output, err := controller.composeOutput(ctx, envPath, extraEnvironment, arguments...); err != nil {
		return fmt.Errorf("validate local physical-pool admission: %w", err)
	} else if err := validatePoolBootstrapOutput(output, pool.ID.String(), compatibilityDigest, artifacts.Evidence.Evidence.Digest, artifacts.Evidence.Evidence.ConformanceVersion, false); err != nil {
		return err
	}
	arguments = append(arguments, "--apply")
	output, err := controller.composeOutput(ctx, envPath, extraEnvironment, arguments...)
	if err != nil {
		return fmt.Errorf("admit local physical pool: %w", err)
	}
	if err := validatePoolBootstrapOutput(output, pool.ID.String(), compatibilityDigest, artifacts.Evidence.Evidence.Digest, artifacts.Evidence.Evidence.ConformanceVersion, true); err != nil {
		return err
	}
	values["LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID"] = pool.ID.String()
	values["LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST"] = compatibilityDigest
	if err := writeEnvironment(envPath, values); err != nil {
		return err
	}
	return nil
}

func readRegularArtifact(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("artifact %q must be a non-writable regular file", path)
	}
	return os.ReadFile(path)
}

func writeReadableArtifactAtomic(path string, contents []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func validatePoolBootstrapOutput(output []byte, poolID, compatibility, evidence, conformance string, applied bool) error {
	want := map[string]string{
		"pool_id": poolID, "compatibility_digest": compatibility,
		"evidence_digest": evidence, "conformance_version": conformance,
		"applied": strconv.FormatBool(applied),
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		name, value, ok := strings.Cut(line, ": ")
		if !ok || name == "" {
			return errors.New("physical-pool bootstrap returned malformed identity evidence")
		}
		if _, exists := got[name]; exists {
			return errors.New("physical-pool bootstrap returned duplicate identity evidence")
		}
		got[name] = value
	}
	if len(got) != len(want) {
		return errors.New("physical-pool bootstrap returned incomplete identity evidence")
	}
	for name, value := range want {
		if got[name] != value {
			return fmt.Errorf("physical-pool bootstrap returned different %s", name)
		}
	}
	return nil
}

func (controller *Controller) waitReady(ctx context.Context, uri string) error {
	for attempt := 0; attempt < defaultChecks; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			return err
		}
		response, err := controller.httpClient.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
		}
		if err := controller.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	return errors.New("local LeapView did not become ready; bootstrap remains incomplete and can be retried with leapview dev")
}

func (controller *Controller) claimProject(ctx context.Context, root string, state *State) error {
	envPath := filepath.Join(root, runtimeEnvFileName)
	output, err := controller.composeOutput(ctx, envPath, nil,
		"run", "--rm", "--no-deps", "leapview", "admin", "project-claim",
		"--project", state.Authority.ProjectUID,
		"--issuer", state.Authority.IssuerID,
		"--environment", state.Authority.Environment,
		"--operation", state.OperationID,
	)
	if err != nil {
		return fmt.Errorf("bootstrap local Project claim through native authority: %w", err)
	}
	var response struct {
		InstanceID  string `json:"instanceId"`
		ProjectUID  string `json:"projectUid"`
		Environment string `json:"environment"`
		ClaimedBy   string `json:"claimedBy"`
		ClaimedAt   string `json:"claimedAt"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return fmt.Errorf("decode native local Project claim: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("decode native local Project claim: trailing JSON value")
	}
	response.InstanceID, response.ProjectUID = strings.TrimSpace(response.InstanceID), strings.TrimSpace(response.ProjectUID)
	if response.InstanceID == "" || response.ProjectUID != state.Authority.ProjectUID || response.Environment != state.Authority.Environment || strings.TrimSpace(response.ClaimedBy) == "" || strings.TrimSpace(response.ClaimedBy) != response.ClaimedBy {
		return errors.New("local Project claim returned different durable identity")
	}
	claimedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(response.ClaimedAt))
	if err != nil || claimedAt.IsZero() {
		return errors.New("local Project claim returned invalid acknowledgement time")
	}
	state.Authority.InstanceID = response.InstanceID
	return nil
}
