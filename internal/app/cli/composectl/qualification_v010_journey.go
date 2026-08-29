package composectl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
)

const (
	v010AdminEmail        = "fai-517-admin@qualification.invalid"
	v010UserEmail         = "fai-517-user@qualification.invalid"
	v010ProjectID         = "compatibility"
	v010Environment       = "fai-517"
	v010DashboardID       = "preservation"
	v010SemanticModelID   = "compatibility"
	v010DatasetID         = "orders"
	v010ReadinessTimeout  = 90 * time.Second
	v010DiagnosticTimeout = 10 * time.Second
)

var v010PublishedPattern = regexp.MustCompile(`(?m)^published compatibility publish=([^ ]+) environment=fai-517 digest=([^ ]+) localDigest=([^ ]+) status=([^ ]+)$`)

//go:embed qualification_v010_project
var v010QualificationProject embed.FS

func prepareV010QualificationProject(runDirectory string) error {
	return fs.WalkDir(v010QualificationProject, "qualification_v010_project", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel("qualification_v010_project", path)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(runDirectory, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		document, err := v010QualificationProject.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, document, 0o444); err != nil {
			return fmt.Errorf("write v0.1 qualification fixture %s: %w", relative, err)
		}
		return nil
	})
}

type v010JourneyResult struct {
	evidence compatibility.V010ApplicationJourney
	token    string
}

func (c *Controller) runV010ApplicationJourney(
	ctx context.Context,
	container string,
) (v010JourneyResult, error) {
	journey := compatibility.V010ApplicationJourney{
		StartedAt: c.now().UTC(), ProjectID: v010ProjectID, Environment: v010Environment,
		AdminEmail: v010AdminEmail, UserEmail: v010UserEmail,
	}
	if err := c.waitV010Readiness(ctx, container); err != nil {
		return v010JourneyResult{}, err
	}
	journey.ReadyAt = c.now().UTC()

	bootstrapOutput, err := c.v010ContainerCLI(ctx, container, "", "admin", "bootstrap")
	if err != nil {
		return v010JourneyResult{}, fmt.Errorf("bootstrap v0.1 application through supported admin CLI: %w", err)
	}
	token, err := parseV010BootstrapToken(bootstrapOutput)
	if err != nil {
		return v010JourneyResult{}, err
	}
	principalOutput, err := c.v010ContainerCLI(ctx, container, token,
		"api", "call", "getCurrentPrincipal", "--target", "http://127.0.0.1:8080")
	if err != nil {
		return v010JourneyResult{}, fmt.Errorf("authenticate to v0.1 API with bootstrap credential: %w", err)
	}
	var admin struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := decodeV010JourneyJSON(principalOutput, &admin); err != nil || admin.ID == "" || admin.Email != v010AdminEmail {
		return v010JourneyResult{}, fmt.Errorf("v0.1 authentication response did not identify the bootstrapped administrator")
	}

	createdOutput, err := c.v010ContainerCLI(ctx, container, token,
		"api", "call", "createPrincipal", "--target", "http://127.0.0.1:8080",
		"--body-json", `{"email":"`+v010UserEmail+`","displayName":"FAI-517 User"}`)
	if err != nil {
		return v010JourneyResult{}, fmt.Errorf("create deterministic v0.1 user through supported API: %w", err)
	}
	var created struct {
		Principal struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"principal"`
		TemporaryPassword string `json:"temporaryPassword"`
	}
	if err := decodeV010JourneyJSON(createdOutput, &created); err != nil || created.Principal.ID == "" ||
		created.Principal.Email != v010UserEmail || strings.TrimSpace(created.TemporaryPassword) == "" {
		return v010JourneyResult{}, fmt.Errorf("v0.1 user bootstrap response was incomplete")
	}
	journey.AdminPrincipalID = admin.ID
	journey.UserPrincipalID = created.Principal.ID
	journey.AuthenticationVerified = true

	publishOutput, err := c.v010ContainerCLI(ctx, container, token,
		"publish", "--target", "http://127.0.0.1:8080",
		"--project", v010QualificationMount+"/libredash.yaml",
		"--environment", v010Environment, "--auto-approve")
	if err != nil {
		return v010JourneyResult{}, fmt.Errorf("publish deterministic v0.1 project through supported CLI: %w", err)
	}
	match := v010PublishedPattern.FindSubmatch(bytes.TrimSpace(publishOutput))
	if len(match) != 5 || string(match[4]) != "active" {
		return v010JourneyResult{}, fmt.Errorf("v0.1 project publish did not report an active deterministic project")
	}
	journey.PublishID = string(match[1])
	journey.ActivatedDigest = string(match[2])
	journey.ProjectDigest = string(match[3])
	journey.ProjectActivated = true

	dashboardsOutput, err := c.v010ContainerCLI(ctx, container, token,
		"api", "call", "listDashboards", "--target", "http://127.0.0.1:8080",
		"--path", "work"+"space="+v010ProjectID)
	if err != nil {
		return v010JourneyResult{}, fmt.Errorf("list activated v0.1 dashboards: %w", err)
	}
	if err := validateV010DashboardList(dashboardsOutput); err != nil {
		return v010JourneyResult{}, err
	}

	semanticOutput, err := c.v010ContainerCLI(ctx, container, token,
		"api", "call", "querySemanticDataset", "--target", "http://127.0.0.1:8080",
		"--path", "work"+"space="+v010ProjectID, "--path", "model="+v010SemanticModelID,
		"--path", "dataset="+v010DatasetID,
		"--body-json", `{"dimensions":[{"field":"orders.status","alias":"status"}],"measures":[{"field":"order_count"}],"sort":[{"field":"status","direction":"asc"}]}`)
	if err != nil {
		return v010JourneyResult{}, fmt.Errorf("query deterministic v0.1 semantic workload: %w", err)
	}
	semanticChecksum, err := validateV010SemanticResult(semanticOutput)
	if err != nil {
		return v010JourneyResult{}, err
	}

	dashboardOutput, err := c.v010ContainerCLI(ctx, container, token,
		"api", "call", "queryDashboardVisualData", "--target", "http://127.0.0.1:8080",
		"--path", "work"+"space="+v010ProjectID, "--path", "dashboard="+v010DashboardID,
		"--path", "page=overview", "--path", "visual=total", "--body-json", `{}`)
	if err != nil {
		return v010JourneyResult{}, fmt.Errorf("query deterministic v0.1 dashboard workload: %w", err)
	}
	dashboardChecksum, err := validateV010DashboardResult(dashboardOutput)
	if err != nil {
		return v010JourneyResult{}, err
	}
	journey.ManagedDataRows = 3
	journey.SemanticResultSHA256 = semanticChecksum
	journey.DashboardResultSHA256 = dashboardChecksum
	journey.ManagedDataVerified = true
	journey.WorkloadVerified = true
	journey.CompletedAt = c.now().UTC()
	return v010JourneyResult{evidence: journey, token: token}, nil
}

func (c *Controller) waitV010Readiness(ctx context.Context, container string) error {
	readyCtx, cancel := qualificationContext(ctx, v010ReadinessTimeout)
	defer cancel()
	var lastErr error
	err := qualificationWait(readyCtx, time.Second, func(requestCtx context.Context) (bool, error) {
		output, checkErr := c.v010ContainerCLI(requestCtx, container, "", "healthcheck", "--timeout", "5s")
		if checkErr != nil {
			lastErr = checkErr
			return false, nil
		}
		if strings.TrimSpace(string(output)) != "ready" {
			lastErr = fmt.Errorf("healthcheck returned an unexpected response")
			return false, nil
		}
		return true, nil
	})
	if err == nil {
		return nil
	}
	diagnosticCtx, diagnosticCancel := context.WithTimeout(context.WithoutCancel(ctx), v010DiagnosticTimeout)
	defer diagnosticCancel()
	logs, logErr := c.qualificationDocker(diagnosticCtx, nil, "logs", "--tail", "80", container)
	if logErr != nil {
		logs = []byte("container logs unavailable")
	}
	return fmt.Errorf("v0.1 readiness was not reached within %s: %v; diagnostics: %s",
		v010ReadinessTimeout, errors.Join(err, lastErr), strings.TrimSpace(string(redactQualificationLog(logs, 80))))
}

func (c *Controller) v010ContainerCLI(ctx context.Context, container, token string, arguments ...string) ([]byte, error) {
	dockerArguments := []string{"exec"}
	environment := map[string]string{}
	if token != "" {
		dockerArguments = append(dockerArguments, "--env", "LIBREDASH_API_TOKEN")
		environment["LIBREDASH_API_TOKEN"] = token
	}
	dockerArguments = append(dockerArguments, container, "libredash")
	dockerArguments = append(dockerArguments, arguments...)
	return c.qualificationDockerWithEnvironment(ctx, environment, dockerArguments...)
}

func (c *Controller) qualificationDockerWithEnvironment(ctx context.Context, values map[string]string, arguments ...string) ([]byte, error) {
	environment := append([]string(nil), os.Environ()...)
	for name, value := range values {
		prefix := name + "="
		for index := len(environment) - 1; index >= 0; index-- {
			if strings.HasPrefix(environment[index], prefix) {
				environment = append(environment[:index], environment[index+1:]...)
			}
		}
		environment = append(environment, prefix+value)
	}
	return qualificationProcess{dir: c.root, executable: c.dockerBin, environment: environment}.
		Run(ctx, nil, c.qualificationExecutor, arguments...)
}

func parseV010BootstrapToken(document []byte) (string, error) {
	token := strings.TrimSpace(string(document))
	if token == "" || len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
		return "", fmt.Errorf("v0.1 admin bootstrap did not return one bounded API credential")
	}
	return token, nil
}

func decodeV010JourneyJSON(document []byte, target any) error {
	if len(document) == 0 || len(document) > 1<<20 {
		return fmt.Errorf("v0.1 journey response is empty or exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("v0.1 journey response contains trailing JSON")
	}
	return nil
}

func validateV010DashboardList(document []byte) error {
	var response struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := decodeV010JourneyJSON(document, &response); err != nil {
		return fmt.Errorf("decode v0.1 dashboard list: %w", err)
	}
	for _, item := range response.Items {
		if item.ID == v010DashboardID {
			return nil
		}
	}
	return fmt.Errorf("activated v0.1 project omitted the deterministic dashboard")
}

func validateV010SemanticResult(document []byte) (string, error) {
	var response struct {
		Items []map[string]any `json:"items"`
	}
	if err := decodeV010JourneyJSON(document, &response); err != nil {
		return "", fmt.Errorf("decode v0.1 semantic result: %w", err)
	}
	rows := make([]string, 0, len(response.Items))
	for _, item := range response.Items {
		status, _ := item["status"].(string)
		count, ok := v010JSONInteger(item["order_count"])
		if !ok || status == "" {
			return "", fmt.Errorf("v0.1 semantic workload returned an invalid row")
		}
		rows = append(rows, fmt.Sprintf("%s=%d", status, count))
	}
	sort.Strings(rows)
	if strings.Join(rows, ",") != "delivered=2,shipped=1" {
		return "", fmt.Errorf("v0.1 semantic workload result did not match deterministic managed data")
	}
	return v010ResultDigest(strings.Join(rows, "\n")), nil
}

func validateV010DashboardResult(document []byte) (string, error) {
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := decodeV010JourneyJSON(document, &response); err != nil {
		return "", fmt.Errorf("decode v0.1 dashboard result: %w", err)
	}
	if len(response.Data) != 1 {
		return "", fmt.Errorf("v0.1 dashboard workload returned an unexpected result shape")
	}
	value, ok := v010JSONInteger(response.Data[0]["value"])
	if !ok || value != 3 {
		return "", fmt.Errorf("v0.1 dashboard workload result did not match deterministic managed data")
	}
	return v010ResultDigest("total=3"), nil
}

func v010JSONInteger(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	return parsed, err == nil
}

func v010ResultDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
