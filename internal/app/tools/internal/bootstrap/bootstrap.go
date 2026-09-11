package bootstrap

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/config/spec"
	"github.com/flidai/leapview/internal/app/tools/internal/sharedassets"
)

// Options describes the dataset-specific parts of a bootstrap operation.
// Downloading, caching, target preparation, and the user-facing flow stay
// shared so the individual dataset tools cannot drift apart.
type Options struct {
	Client         *http.Client
	Out            string
	Name           string
	ArchiveName    string
	DownloadURL    string
	DownloadLabel  string
	CacheName      string
	AlreadyMessage string
	MissingLabel   string
	FileLabel      string
	Force          bool
	Missing        func(string) []string
	Extract        func(string, string) (int, error)
	Verify         func(string) error
	VerifyArchive  func(string) error
}

// Run executes the common download, verification, extraction, and reporting
// flow for a dataset bootstrap tool.
func Run(options Options) error {
	target, err := TargetDir(options.Out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create data directory %s: %w", target, err)
	}

	missing := options.Missing(target)
	if len(missing) == 0 && !options.Force {
		if options.Verify == nil {
			fmt.Printf("%s in %s\n", options.AlreadyMessage, target)
			return nil
		}
		if err := options.Verify(target); err == nil {
			fmt.Printf("%s in %s\n", options.AlreadyMessage, target)
			return nil
		} else {
			fmt.Printf("Checksum refresh required: %v\n", err)
		}
	}

	cache, err := CacheDir(options.CacheName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return fmt.Errorf("create bootstrap cache %s: %w", cache, err)
	}

	archivePath := filepath.Join(cache, options.ArchiveName)
	if options.Force || !FileExists(archivePath) {
		client := options.Client
		if client == nil {
			client = &http.Client{Timeout: 10 * time.Minute}
		}
		downloadLabel := options.DownloadLabel
		if downloadLabel == "" {
			downloadLabel = options.Name
		}
		if err := DownloadArchive(client, archivePath, options.DownloadURL, downloadLabel); err != nil {
			return err
		}
	}
	if options.VerifyArchive != nil {
		if err := options.VerifyArchive(archivePath); err != nil {
			return err
		}
	}

	copied, err := options.Extract(archivePath, target)
	if err != nil {
		return err
	}
	if options.Verify != nil {
		if err := options.Verify(target); err != nil {
			return err
		}
	}

	if len(missing) > 0 {
		label := options.MissingLabel
		if label == "" {
			label = "files"
		}
		fmt.Printf("Missing %s: %s\n", label, strings.Join(missing, ", "))
	}
	if options.Force {
		fmt.Println("Force refresh requested")
	}
	fmt.Printf("Bootstrapped %s\n", options.Name)
	fmt.Printf("Source archive: %s\n", archivePath)
	fileLabel := options.FileLabel
	if fileLabel == "" {
		fileLabel = "files"
	}
	fmt.Printf("Copied %d %s to %s\n", copied, fileLabel, target)
	return nil
}

// SharedOptions describes the dataset-specific shared-cache hooks.
type SharedOptions struct {
	Out       string
	CacheName string
	Name      string
	Force     bool
	Ready     func(string) error
	Populate  func(string) error
	Refresh   func(string) error
	CacheRoot string
}

// RunShared links a local output directory to the immutable shared cache and
// populates it once when needed.
func RunShared(options SharedOptions) error {
	target, err := TargetDir(options.Out)
	if err != nil {
		return err
	}
	root, err := sharedassets.CacheRoot(options.CacheRoot)
	if err != nil {
		return err
	}
	shared := filepath.Join(root, "datasets", options.CacheName)
	hadReadyAssets := options.Ready(shared) == nil
	if info, statErr := os.Lstat(target); statErr == nil && info.IsDir() && options.Ready(target) == nil {
		hadReadyAssets = true
	}
	if err := sharedassets.Ensure(sharedassets.Options{
		Local:    target,
		Shared:   shared,
		Ready:    options.Ready,
		Populate: options.Populate,
	}); err != nil {
		return err
	}
	if options.Force && hadReadyAssets && options.Refresh != nil {
		if err := options.Refresh(target); err != nil {
			return err
		}
	}
	fmt.Printf("Using shared %s assets at %s (linked from %s)\n", options.Name, shared, target)
	return nil
}

func TargetDir(out string) (string, error) {
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("out is required")
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("resolve output directory %s: %w", out, err)
	}
	return abs, nil
}

func CacheDir(name string) (string, error) {
	if dir := os.Getenv(configspec.EnvLEAPVIEW_BOOTSTRAP_CACHE_DIR); dir != "" {
		return filepath.Abs(dir)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find user cache directory: %w", err)
	}
	return filepath.Join(base, "leapview", name), nil
}

func FileExists(file string) bool {
	info, err := os.Stat(file)
	return err == nil && !info.IsDir()
}

func Truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func DownloadArchive(client *http.Client, archivePath, downloadURL, name string) error {
	tmp, err := os.CreateTemp(filepath.Dir(archivePath), filepath.Base(archivePath)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary archive: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("create %s request: %w", name, err)
	}
	req.Header.Set("User-Agent", "LeapView bootstrap")

	resp, err := client.Do(req)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = tmp.Close()
		return fmt.Errorf("download %s: %s: %s", name, resp.Status, strings.TrimSpace(string(body)))
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s archive: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s archive: %w", name, err)
	}
	if err := os.Rename(tmpPath, archivePath); err != nil {
		return fmt.Errorf("store %s archive at %s: %w", name, archivePath, err)
	}
	return nil
}

func ExtractZipFile(file *zip.File, destination, name string) error {
	source, err := file.Open()
	if err != nil {
		return fmt.Errorf("open %s from %s archive: %w", file.Name, name, err)
	}
	defer source.Close()

	tmp, err := os.CreateTemp(filepath.Dir(destination), filepath.Base(destination)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary CSV %s: %w", destination, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, source); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy %s from %s archive: %w", file.Name, name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary CSV %s: %w", destination, err)
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return fmt.Errorf("store CSV %s: %w", destination, err)
	}
	return nil
}
