package composectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type qualificationLifecycleCredentialScope struct {
	ProjectID   string    `json:"projectID"`
	Environment string    `json:"environment"`
	TargetURL   string    `json:"targetURL"`
	IssuedAt    time.Time `json:"issuedAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Actions     []string  `json:"actions"`
}

type qualificationLifecycleCredential struct {
	qualificationLifecycleCredentialScope
	Token string `json:"token"`
}

func qualificationLifecycleActions() []access.Action {
	return []access.Action{access.ActionConnectionRead, access.ActionConnectionUse, access.ActionConnectionUpload, access.ActionSourceRead, access.ActionDashboardRead, access.ActionSemanticQuery, access.ActionSemanticConsume}
}

func validateQualificationLifecycleScope(retained *qualificationLifecycleCredentialScope, request qualificationFirstPublicationRequest) error {
	if retained != nil {
		if retained.ProjectID != request.ProjectID || retained.Environment != request.Environment || retained.TargetURL != request.TargetURL ||
			retained.IssuedAt.IsZero() || retained.ExpiresAt.Sub(retained.IssuedAt) != 2*time.Hour ||
			!slices.Equal(retained.Actions, qualificationActionNames(qualificationLifecycleActions())) {
			return errors.New("retained lifecycle workload credential has an unexpected scope or lifetime")
		}
	}
	return nil
}

func validateQualificationLifecycleOptions(options qualificationAuthoringOptions) error {
	if err := validateQualificationPreloadedImages(options.PreloadedClientImage, options.PreloadedBrowserImage); err != nil {
		return err
	}
	if options.LifecycleCredentialFile != "" {
		if !options.FirstPublicationOnly {
			return errors.New("lifecycle credential export requires the isolated first-publication journey")
		}
		return validateQualificationLifecycleOutput(options.LifecycleCredentialFile, options.EvidenceDir)
	}
	return nil
}

func validateQualificationPreloadedImages(client, browser string) error {
	if client == "" && browser == "" {
		return nil
	}
	if !qualificationSHA256Identity(client) || !qualificationSHA256Identity(browser) {
		return errors.New("preloaded qualification helpers require both exact local sha256 image IDs")
	}
	return nil
}

func prepareQualificationBrowserAssets(ctx context.Context, browser qualificationContainer, root string, preloaded bool) error {
	if _, err := browser.Exec(ctx, nil, "mkdir", "-p", "/work"); err != nil {
		return qualificationContainerOperationError(ctx, browser, "prepare authoring browser work directory", err)
	}
	for _, name := range []string{"package.json", "authoring-worker.mjs"} {
		if _, err := browser.CopyTo(ctx, filepath.Join(root, name), "/work/"+name); err != nil {
			return qualificationContainerOperationError(ctx, browser, "copy authoring browser asset "+name, err)
		}
	}
	if !preloaded {
		if _, err := browser.Exec(ctx, nil, "npm", "install", "--prefix", "/work", "--no-audit", "--no-fund", "--silent"); err != nil {
			return qualificationContainerOperationError(ctx, browser, "install authoring browser dependencies", err)
		}
	}
	return nil
}

func (c *Controller) verifyQualificationPreloadedImage(ctx context.Context, image string) error {
	if !qualificationSHA256Identity(image) {
		return errors.New("preloaded qualification helper must use an exact local image ID")
	}
	raw, err := c.qualificationDocker(ctx, nil, "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil {
		return fmt.Errorf("inspect preloaded qualification helper: %w", err)
	}
	if strings.TrimSpace(string(raw)) != image {
		return errors.New("preloaded qualification helper content differs from selected image ID")
	}
	return nil
}

func (c *Controller) prepareQualificationAuthoringImages(ctx context.Context, options qualificationAuthoringOptions, clientImage string, cleanup *qualificationCleanup) (string, string, error) {
	if err := validateQualificationPreloadedImages(options.PreloadedClientImage, options.PreloadedBrowserImage); err != nil {
		return "", "", err
	}
	if options.PreloadedClientImage != "" {
		for _, image := range []string{options.PreloadedClientImage, options.PreloadedBrowserImage} {
			if err := c.verifyQualificationPreloadedImage(ctx, image); err != nil {
				return "", "", err
			}
		}
		// These images and browser dependencies were prepared and pinned by the
		// caller before network isolation. They remain caller-owned after use.
		return options.PreloadedClientImage, options.PreloadedBrowserImage, nil
	}
	if _, err := c.qualificationDocker(ctx, nil, "build",
		"--file", filepath.Join(options.AssetsRoot, "Dockerfile.authoring-client"),
		"--build-arg", "LEAPVIEW_IMAGE="+options.ClientBaseImage,
		"--tag", clientImage, options.AssetsRoot); err != nil {
		return "", "", fmt.Errorf("build qualification client: %w", err)
	}
	cleanup.Add(func(cleanupCtx context.Context) error {
		_, err := c.qualificationDocker(cleanupCtx, nil, "image", "rm", "--force", clientImage)
		return err
	})
	if _, err := c.qualificationDocker(ctx, nil, "pull", qualificationBrowserImage); err != nil {
		return "", "", fmt.Errorf("pull qualification browser: %w", err)
	}
	return clientImage, qualificationBrowserImage, nil
}

func validateQualificationLifecycleOutput(path, evidenceDir string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." {
		return errors.New("lifecycle credential output requires a canonical absolute file path")
	}
	parent := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return errors.New("lifecycle credential output requires an existing non-symlink parent")
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("lifecycle credential parent must already have mode 0700")
	}
	if evidenceDir != "" {
		relative, err := filepath.Rel(evidenceDir, path)
		if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return errors.New("lifecycle credentials must remain outside retained evidence")
		}
	}
	if _, err := os.Lstat(path); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func writeQualificationLifecycleCredential(path string, credential qualificationLifecycleCredential) (result error) {
	if err := validateQualificationLifecycleOutput(path, ""); err != nil {
		return err
	}
	if credential.Token == "" {
		return errors.New("lifecycle workload credential is empty")
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".lifecycle-credential-*.tmp")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		if result != nil && published {
			result = errors.Join(result, os.Remove(path))
		}
	}()
	if _, err := temporary.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err := temporary.Chmod(0o400); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporary.Name(), path); err != nil {
		return err
	}
	published = true
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (c *Controller) issueQualificationLifecycleCredential(ctx context.Context, worker *qualificationJSONWorker, options qualificationAuthoringOptions) (*qualificationLifecycleCredentialScope, error) {
	if err := validateQualificationLifecycleOutput(options.LifecycleCredentialFile, options.EvidenceDir); err != nil {
		return nil, err
	}
	project, err := projectgraph.NewResourceID(options.ProjectID)
	if err != nil {
		return nil, err
	}
	permissions, err := access.ProjectPermissionPairsForActions(project, qualificationLifecycleActions())
	if err != nil {
		return nil, err
	}
	issuedAt := c.now().UTC().Truncate(time.Second)
	scope := qualificationLifecycleCredentialScope{
		ProjectID: options.ProjectID, Environment: options.Environment, TargetURL: options.Target,
		IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(2 * time.Hour),
		Actions: qualificationActionNames(qualificationLifecycleActions()),
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := worker.CallContext(ctx, "createAdministratorAPIToken", map[string]any{
		"name": "qualification-managed-lifecycle", "permissions": permissions,
		"expiresAt": scope.ExpiresAt.Format(time.RFC3339),
	}, &response, nil); err != nil {
		return nil, err
	}
	if err := writeQualificationLifecycleCredential(options.LifecycleCredentialFile, qualificationLifecycleCredential{qualificationLifecycleCredentialScope: scope, Token: response.Token}); err != nil {
		return nil, err
	}
	return &scope, nil
}
