package composectl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func (c *Controller) qualifyProductionImageRuntime(
	ctx context.Context,
	image string,
) (runErr error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return errors.New("production image is required")
	}
	if _, err := c.qualificationDocker(ctx, nil, "pull", image); err != nil {
		return fmt.Errorf("pull production image: %w", err)
	}
	runtimeUID, err := c.qualificationImageIdentity(ctx, image, "-u")
	if err != nil {
		return err
	}
	runtimeGID, err := c.qualificationImageIdentity(ctx, image, "-g")
	if err != nil {
		return err
	}
	metricsToken, err := qualificationRandomHex(16)
	if err != nil {
		return err
	}
	csrfKey, err := qualificationRandomHex(16)
	if err != nil {
		return err
	}
	container := normalizedQualificationName("leapview-image-smoke-" + strconv.Itoa(os.Getpid()))
	if _, err := c.qualificationDocker(
		ctx, nil,
		"run", "--detach", "--name", container,
		"--read-only",
		"--network", "none",
		"--tmpfs", "/var/lib/leapview:rw,exec,nosuid,nodev,mode=0700,uid="+runtimeUID+",gid="+runtimeGID+",size=128m",
		"--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777,size=64m",
		"--env", "LEAPVIEW_POSTGRES_CONTROL_URL=",
		"--env", "LEAPVIEW_API_TOKEN_ONLY_AUTH=1",
		"--env", "LEAPVIEW_CSRF_KEY="+csrfKey,
		"--env", "LEAPVIEW_METRICS_BEARER_TOKEN="+metricsToken,
		"--env", "LEAPVIEW_ALLOWED_HOSTS=127.0.0.1,localhost",
		"--env", "LEAPVIEW_PUBLIC_URL=https://localhost",
		image,
	); err != nil {
		return fmt.Errorf("start production image: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), qualificationCleanupTimeout)
		defer cancel()
		_, cleanupErr := c.qualificationDocker(cleanupCtx, nil, "rm", "--force", container)
		runErr = errors.Join(runErr, ignoreQualificationNotFound(cleanupErr))
	}()

	if err := c.waitQualificationContainerValue(ctx, container, "{{.State.Status}}", "exited", time.Minute); err != nil {
		return err
	}
	runtimeContainer := c.qualificationContainers.Existing(container)
	exitCode, err := runtimeContainer.Inspect(ctx, "{{.State.ExitCode}}")
	if err != nil {
		return fmt.Errorf("inspect fail-closed production image exit: %w", err)
	}
	if strings.TrimSpace(string(exitCode)) == "0" {
		return errors.New("production image accepted a missing PostgreSQL control URL")
	}
	logs, err := runtimeContainer.Logs(ctx, 100)
	if err != nil {
		return fmt.Errorf("read fail-closed production image logs: %w", err)
	}
	if !bytes.Contains(logs, []byte("production serve requires LEAPVIEW_POSTGRES_CONTROL_URL")) {
		return errors.New("production image did not fail closed on a missing PostgreSQL control URL")
	}
	return nil
}

func (c *Controller) QualifySiteImage(
	ctx context.Context,
	options QualificationSiteImageOptions,
) (runErr error) {
	image := strings.TrimSpace(options.Image)
	if image == "" {
		image = "leapview-site:ci"
	}
	if _, err := c.qualificationDocker(ctx, nil, "pull", image); err != nil {
		return fmt.Errorf("pull public site image: %w", err)
	}
	userOutput, err := c.qualificationDocker(ctx, nil, "image", "inspect", image, "--format", "{{.Config.User}}")
	if err != nil {
		return fmt.Errorf("inspect public site image user: %w", err)
	}
	user := strings.TrimSpace(string(userOutput))
	principal := strings.SplitN(user, ":", 2)[0]
	if principal == "" || principal == "0" || strings.EqualFold(principal, "root") {
		return fmt.Errorf("public site image must declare a non-root runtime user")
	}
	container := normalizedQualificationName("leapview-site-image-smoke-" + strconv.Itoa(os.Getpid()))
	if _, err := c.qualificationDocker(
		ctx, nil,
		"run", "--detach", "--name", container,
		"--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777,size=32m",
		"--publish", "127.0.0.1::8081",
		image,
	); err != nil {
		return fmt.Errorf("start public site image: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), qualificationCleanupTimeout)
		defer cancel()
		_, cleanupErr := c.qualificationDocker(cleanupCtx, nil, "rm", "--force", container)
		runErr = errors.Join(runErr, ignoreQualificationNotFound(cleanupErr))
	}()

	baseURL, err := c.qualificationPublishedURL(ctx, container, "8081/tcp")
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if err := waitQualificationHTTP(ctx, client, baseURL+"/healthz", http.StatusOK); err != nil {
		return qualificationContainerOperationError(ctx, c.qualificationContainers.Existing(container), "wait for public site image health", err)
	}
	for _, path := range []string{"/readyz", "/docs"} {
		if err := expectQualificationHTTP(ctx, client, baseURL+path, "", http.StatusOK, ""); err != nil {
			return err
		}
	}
	if err := c.waitQualificationContainerValue(ctx, container, "{{.State.Running}}", "true", time.Minute); err != nil {
		return err
	}
	_, err = fmt.Fprintln(c.stdout, "public site image passed qualification")
	return err
}

func (c *Controller) qualificationImageIdentity(ctx context.Context, image, flag string) (string, error) {
	output, err := c.qualificationDocker(ctx, nil, "run", "--rm", "--entrypoint", "id", image, flag)
	if err != nil {
		return "", fmt.Errorf("inspect production image identity %s: %w", flag, err)
	}
	value := strings.TrimSpace(string(output))
	if _, err := strconv.ParseUint(value, 10, 32); err != nil {
		return "", fmt.Errorf("production image identity %s is invalid: %w", flag, err)
	}
	return value, nil
}

func (c *Controller) qualificationPublishedURL(ctx context.Context, container, port string) (string, error) {
	output, err := c.qualificationDocker(ctx, nil, "port", container, port)
	if err != nil {
		return "", fmt.Errorf("read published qualification port: %w", err)
	}
	address := strings.TrimSpace(strings.Split(string(output), "\n")[0])
	_, publishedPort, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("parse published qualification port %q: %w", address, err)
	}
	return "http://127.0.0.1:" + publishedPort, nil
}

func (c *Controller) waitQualificationContainerValue(ctx context.Context, container, format, wanted string, timeout time.Duration) error {
	qualificationContainer := c.qualificationContainers.Existing(container)
	if qualificationContainer == nil {
		return fmt.Errorf("qualification container %q is missing", container)
	}
	if err := waitQualificationContainerValue(
		ctx,
		qualificationContainer,
		format,
		wanted,
		timeout,
	); err != nil {
		return qualificationContainerOperationError(
			ctx,
			qualificationContainer,
			"wait for container state "+wanted,
			err,
		)
	}
	return nil
}

func waitQualificationContainerValue(
	ctx context.Context,
	container qualificationContainer,
	format string,
	wanted string,
	timeout time.Duration,
) error {
	if container == nil {
		return fmt.Errorf("qualification container is missing")
	}
	waitCtx, cancel := qualificationContext(ctx, timeout)
	defer cancel()
	err := qualificationWait(waitCtx, time.Second, func(requestCtx context.Context) (bool, error) {
		output, inspectErr := container.Inspect(requestCtx, format)
		if inspectErr != nil {
			return false, nil
		}
		return strings.TrimSpace(string(output)) == wanted, nil
	})
	return err
}

func waitQualificationHTTP(ctx context.Context, client *http.Client, endpoint string, status int) error {
	waitCtx, cancel := qualificationContext(ctx, 2*time.Minute)
	defer cancel()
	return qualificationWait(waitCtx, time.Second, func(requestCtx context.Context) (bool, error) {
		request, err := newQualificationLoopbackRequest(requestCtx, http.MethodGet, endpoint, nil)
		if err != nil {
			return false, err
		}
		response, err := client.Do(request)
		if err != nil {
			return false, nil
		}
		_ = response.Body.Close()
		return response.StatusCode == status, nil
	})
}

func expectQualificationHTTP(ctx context.Context, client *http.Client, endpoint, bearer string, status int, contains string) error {
	request, err := newQualificationLoopbackRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != status {
		return fmt.Errorf("%s returned %d, want %d", request.URL.Path, response.StatusCode, status)
	}
	if contains != "" && !bytes.Contains(body, []byte(contains)) {
		return fmt.Errorf("%s response omits %q", request.URL.Path, contains)
	}
	return nil
}

func qualificationRandomHex(bytesCount int) (string, error) {
	value := make([]byte, bytesCount)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate qualification secret: %w", err)
	}
	return hex.EncodeToString(value), nil
}
