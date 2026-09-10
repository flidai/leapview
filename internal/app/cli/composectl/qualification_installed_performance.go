package composectl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func (c *Controller) runQualificationPerformance(
	ctx context.Context,
	browserContainer string,
	appContainer string,
	evidenceDir string,
	imageReference string,
	metricsToken string,
) error {
	diskBefore, err := c.qualificationDiskUsage(
		ctx,
		appContainer,
		"performance disk before",
	)
	if err != nil {
		return err
	}
	var policy qualificationPerformancePolicy
	if err := readQualificationJSON(
		c.path(filepath.Join("qualification", "performance-policy.json")),
		&policy,
	); err != nil {
		return err
	}
	if policy.Assumptions.Samples.ColdDashboardLoads <= 0 {
		return fmt.Errorf("performance policy requires cold dashboard samples")
	}
	if failures := validateQualificationPerformancePolicy(policy); len(failures) > 0 {
		return fmt.Errorf(
			"invalid performance policy: %s",
			strings.Join(failures, "; "),
		)
	}
	coldPaths := make([]string, 0, policy.Assumptions.Samples.ColdDashboardLoads)
	for index := 1; index <= policy.Assumptions.Samples.ColdDashboardLoads; index++ {
		if _, err := c.qualificationContainers.Existing(appContainer).Restart(ctx); err != nil {
			return err
		}
		if err := c.waitQualificationHealthy(ctx, appContainer, "cold performance sample"); err != nil {
			return err
		}
		path := fmt.Sprintf("/evidence/performance-cold-%d.json", index)
		coldPaths = append(coldPaths, path)
		if _, err := c.qualificationContainers.Existing(browserContainer).Exec(
			ctx, nil,
			"env",
			"QUALIFICATION_METRICS_TOKEN="+metricsToken,
			"node", "/work/performance.mjs", "cold", path,
		); err != nil {
			return qualificationContainerOperationError(
				ctx,
				c.qualificationContainers.Existing(browserContainer),
				"capture cold performance sample",
				err,
			)
		}
	}
	coldJSON, _ := json.Marshal(coldPaths)
	if _, err := c.qualificationContainers.Existing(browserContainer).Exec(
		ctx, nil,
		"env",
		"QUALIFICATION_METRICS_TOKEN="+metricsToken,
		"QUALIFICATION_COLD_RESULTS="+string(coldJSON),
		"node", "/work/performance.mjs", "workload", "/evidence/performance-report.json",
	); err != nil {
		return qualificationContainerOperationError(
			ctx,
			c.qualificationContainers.Existing(browserContainer),
			"capture performance workload",
			err,
		)
	}
	diskAfter, err := c.qualificationDiskUsage(
		ctx,
		appContainer,
		"performance disk after",
	)
	if err != nil {
		return err
	}
	serverVersion, err := c.qualificationDocker(
		ctx, nil, "version", "--format", "{{.Server.Version}}",
	)
	if err != nil {
		return err
	}
	serverVersionValue := strings.TrimSpace(string(serverVersion))
	if serverVersionValue == "" {
		return fmt.Errorf("Docker server version is missing")
	}
	cpuOutput, err := c.qualificationDocker(ctx, nil, "info", "--format", "{{.NCPU}}")
	if err != nil {
		return err
	}
	cpus, err := firstQualificationInteger(cpuOutput, "Docker CPUs")
	if err != nil {
		return err
	}
	memoryOutput, err := c.qualificationDocker(ctx, nil, "info", "--format", "{{.MemTotal}}")
	if err != nil {
		return err
	}
	memory, err := firstQualificationInteger(memoryOutput, "Docker memory")
	if err != nil {
		return err
	}
	rowsOutput, err := c.qualificationContainers.Existing(appContainer).Exec(
		ctx, nil,
		"wc", "-l", "/app/"+qualificationPerformanceFixturePath,
	)
	if err != nil {
		return err
	}
	rows, err := firstQualificationInteger(rowsOutput, "evaluation order rows")
	if err != nil {
		return err
	}
	if rows <= 1 {
		return fmt.Errorf("evaluation order fixture must contain a header and at least one row")
	}
	appLimitsOutput, err := c.qualificationDocker(
		ctx, nil, "inspect", "--format",
		"{{.HostConfig.NanoCpus}} {{.HostConfig.CpuQuota}} {{.HostConfig.CpuPeriod}} {{.HostConfig.Memory}}",
		appContainer,
	)
	if err != nil {
		return err
	}
	effectiveCPULimit, effectiveMemoryLimitBytes, err := qualificationParseEffectiveAppLimits(string(appLimitsOutput), cpus)
	if err != nil {
		return fmt.Errorf("identify effective application limits: %w", err)
	}
	cpuModelOutput, err := c.qualificationContainers.Existing(appContainer).Exec(
		ctx, nil, "cat", "/proc/cpuinfo",
	)
	if err != nil {
		return qualificationContainerOperationError(
			ctx, c.qualificationContainers.Existing(appContainer),
			"identify performance CPU model", err,
		)
	}
	cpuModel, err := qualificationCPUIdentity(string(cpuModelOutput))
	if err != nil {
		return fmt.Errorf("identify performance CPU model: %w", err)
	}
	kernelOutput, err := c.qualificationContainers.Existing(appContainer).Exec(
		ctx, nil, "uname", "-sr",
	)
	if err != nil {
		return qualificationContainerOperationError(
			ctx, c.qualificationContainers.Existing(appContainer),
			"identify performance kernel", err,
		)
	}
	kernel := strings.TrimSpace(string(kernelOutput))
	if kernel == "" {
		return fmt.Errorf("performance kernel identity is missing")
	}
	environmentJSON, _ := json.Marshal(map[string]any{
		"runtime":                   "Docker Engine " + serverVersionValue,
		"cpuModel":                  cpuModel,
		"kernel":                    kernel,
		"logicalCPUs":               cpus,
		"memoryBytes":               memory,
		"effectiveCPULimit":         effectiveCPULimit,
		"effectiveMemoryLimitBytes": effectiveMemoryLimitBytes,
		"dataset":                   map[string]int64{"orders": rows - 1},
	})
	fixtureOutput, err := c.qualificationContainers.Existing(appContainer).Exec(
		ctx, nil, "sh", "-ec",
		"set -eu; for root in /app/"+qualificationPerformanceProjectRoot+" /app/"+qualificationPerformanceDataRoot+"; do test -d \"$root\"; done; find /app/"+qualificationPerformanceProjectRoot+" /app/"+qualificationPerformanceDataRoot+" -type f -print0 | sort -z | xargs -0 -r sha256sum --",
	)
	if err != nil {
		return qualificationContainerOperationError(
			ctx, c.qualificationContainers.Existing(appContainer),
			"identify performance fixture", err,
		)
	}
	fixtureDigest, err := qualificationFixtureManifestDigest(fixtureOutput)
	if err != nil {
		return fmt.Errorf("performance fixture manifest is invalid: %w", err)
	}
	nodeOutput, err := c.qualificationContainers.Existing(browserContainer).Exec(
		ctx, nil, "node", "--version",
	)
	if err != nil {
		return qualificationContainerOperationError(
			ctx, c.qualificationContainers.Existing(browserContainer),
			"identify performance toolchain", err,
		)
	}
	packageHashOutput, err := c.qualificationContainers.Existing(browserContainer).Exec(
		ctx, nil, "sha256sum", "/work/package-lock.json",
	)
	if err != nil {
		return qualificationContainerOperationError(
			ctx, c.qualificationContainers.Existing(browserContainer),
			"identify installed performance package", err,
		)
	}
	packageHashFields := strings.Fields(string(packageHashOutput))
	packageHash, ok := "", false
	if len(packageHashFields) > 0 {
		packageHash, ok = qualificationNormalizeDigest(packageHashFields[0])
	}
	if !ok {
		return fmt.Errorf("installed performance package digest is missing or malformed")
	}
	harnessOutput, err := c.qualificationContainers.Existing(browserContainer).Exec(
		ctx, nil, "sha256sum", "/work/browser.mjs", "/work/performance.mjs", "/work/performance-resources.mjs",
	)
	if err != nil {
		return qualificationContainerOperationError(
			ctx, c.qualificationContainers.Existing(browserContainer),
			"identify performance harness", err,
		)
	}
	harnessLines := strings.Split(strings.TrimSpace(string(harnessOutput)), "\n")
	harnessDigests := make([]string, 0, len(harnessLines))
	for _, line := range harnessLines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return fmt.Errorf("performance harness digest is malformed")
		}
		digest, valid := qualificationNormalizeDigest(fields[0])
		if !valid {
			return fmt.Errorf("performance harness digest is malformed")
		}
		harnessDigests = append(harnessDigests, digest)
	}
	if len(harnessDigests) != 3 {
		return fmt.Errorf("performance harness digest is incomplete")
	}
	harnessDigest := qualificationDigest([]byte(strings.Join(harnessDigests, "\n")))
	policyDigest, err := qualificationDigestFile(c.path(filepath.Join("qualification", "performance-policy.json")))
	if err != nil {
		return fmt.Errorf("hash performance policy: %w", err)
	}
	var runtimeIdentity struct {
		Version     string `json:"version"`
		Revision    string `json:"revision"`
		BuildTime   string `json:"buildTime"`
		Dirty       bool   `json:"dirty"`
		Development bool   `json:"development"`
	}
	runtimeOutput, err := c.qualificationContainers.Existing(appContainer).Exec(
		ctx, nil, "leapview", "version", "--json",
	)
	if err != nil {
		return qualificationContainerOperationError(
			ctx, c.qualificationContainers.Existing(appContainer),
			"identify running performance binary", err,
		)
	}
	if err := json.Unmarshal(runtimeOutput, &runtimeIdentity); err != nil {
		return fmt.Errorf("decode running performance identity: %w", err)
	}
	if !qualificationPerformanceRevisionValid(runtimeIdentity.Revision) {
		return fmt.Errorf("performance runtime identity has no valid commit")
	}
	if runtimeIdentity.Dirty {
		return fmt.Errorf("performance runtime identity is dirty")
	}
	if err := writeQualificationJSON(filepath.Join(evidenceDir, "runtime-identity.json"), runtimeIdentity); err != nil {
		return fmt.Errorf("write performance runtime identity: %w", err)
	}
	browserImageID, err := c.qualificationDocker(
		ctx, nil, "image", "inspect", "--format", "{{.Id}}", qualificationBrowserImage,
	)
	if err != nil {
		return err
	}
	browserImage := strings.TrimSpace(string(browserImageID))
	if !strings.HasPrefix(browserImage, "sha256:") {
		return fmt.Errorf("performance browser image identity is missing")
	}
	playwrightVersionOutput, err := c.qualificationContainers.Existing(browserContainer).Exec(
		ctx, nil, "node", "-p", "require('/work/node_modules/playwright/package.json').version",
	)
	if err != nil {
		return qualificationContainerOperationError(
			ctx, c.qualificationContainers.Existing(browserContainer),
			"identify installed Playwright", err,
		)
	}
	playwrightVersion := strings.TrimSpace(string(playwrightVersionOutput))
	if playwrightVersion == "" {
		return fmt.Errorf("installed Playwright version is missing")
	}
	performanceMetadata := qualificationPerformanceMetadata{
		Commit:         strings.TrimSpace(runtimeIdentity.Revision),
		FixtureDigest:  fixtureDigest,
		PolicyDigest:   policyDigest,
		SampleProtocol: qualificationPerformanceSampleProtocolForPolicy(policy),
		Toolchain: qualificationPerformanceToolchain{
			BrowserImage: browserImage,
			Node:         strings.TrimSpace(string(nodeOutput)),
			Playwright:   playwrightVersion,
			PackageHash:  packageHash,
			HarnessHash:  harnessDigest,
		},
	}
	if err := finalizeQualificationPerformanceReport(
		filepath.Join(evidenceDir, "performance-report.json"),
		policy,
		diskBefore,
		diskAfter,
		environmentJSON,
		imageReference,
		runtime.GOARCH,
		qualificationPerformanceBaseline(),
		performanceMetadata,
	); err != nil {
		return err
	}
	for _, path := range coldPaths {
		_ = os.Remove(filepath.Join(evidenceDir, filepath.Base(path)))
	}
	return nil
}

func (c *Controller) qualificationDiskUsage(
	ctx context.Context,
	appContainer string,
	label string,
) (int64, error) {
	output, err := c.qualificationContainers.Existing(appContainer).Exec(
		ctx, nil,
		"du",
		"-sb",
		"--exclude=*.db-wal",
		"--exclude=*.db-shm",
		"/var/lib/leapview",
	)
	if err != nil {
		return 0, err
	}
	return firstQualificationInteger(output, label)
}
