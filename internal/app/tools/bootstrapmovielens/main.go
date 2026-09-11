package main

import (
	"archive/zip"
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/config/spec"
	"github.com/flidai/leapview/internal/app/tools/internal/bootstrap"
)

const (
	datasetName = "MovieLens 32M"
	archiveName = "ml-32m.zip"
	downloadURL = "https://files.grouplens.org/datasets/movielens/" + archiveName
)

type expectedFile struct {
	Name string
	MD5  string
}

var expectedFiles = []expectedFile{
	{Name: "links.csv", MD5: "8f033867bcb4e6be8792b21468b4fa6e"},
	{Name: "movies.csv", MD5: "0df90835c19151f9d819d0822e190797"},
	{Name: "ratings.csv", MD5: "cf12b74f9ad4b94a011f079e26d4270a"},
	{Name: "tags.csv", MD5: "963bf4fa4de6b8901868fddd3eb54567"},
}

func main() {
	out := flag.String("out", "", "directory for downloaded MovieLens CSV files")
	sharedCache := flag.Bool("shared-cache", false, "store immutable CSVs in the user cache and link the output directory")
	flag.Parse()
	client := &http.Client{Timeout: 30 * time.Minute}
	var err error
	if *sharedCache {
		err = runShared(client, *out)
	} else {
		err = run(client, *out)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap movielens: %v\n", err)
		os.Exit(1)
	}
}

func run(client *http.Client, out string) error {
	return runWithForce(client, out, bootstrap.Truthy(os.Getenv(configspec.EnvLEAPVIEW_BOOTSTRAP_FORCE)))
}

func runWithForce(client *http.Client, out string, force bool) error {
	return bootstrap.Run(bootstrap.Options{
		Client:         client,
		Out:            out,
		Name:           datasetName,
		ArchiveName:    archiveName,
		DownloadURL:    downloadURL,
		DownloadLabel:  "MovieLens dataset",
		CacheName:      "movielens",
		AlreadyMessage: datasetName + " CSVs already available",
		MissingLabel:   "CSVs",
		FileLabel:      "CSV files",
		Force:          force,
		Missing:        missingFiles,
		Extract:        extractExpectedFiles,
		Verify:         verifyExpectedFileChecksums,
	})
}

func runShared(client *http.Client, out string) error {
	return bootstrap.RunShared(bootstrap.SharedOptions{
		Out:       out,
		CacheName: filepath.Join("movielens", "ml-32m"),
		Name:      "MovieLens",
		Force:     bootstrap.Truthy(os.Getenv(configspec.EnvLEAPVIEW_BOOTSTRAP_FORCE)),
		Ready:     verifyExpectedFileChecksums,
		Populate: func(directory string) error {
			return runWithForce(client, directory, false)
		},
		Refresh: func(directory string) error {
			return runWithForce(client, directory, true)
		},
		CacheRoot: os.Getenv(configspec.EnvLEAPVIEW_DEV_ASSET_CACHE_DIR),
	})
}

func missingFiles(target string) []string {
	var missing []string
	for _, file := range expectedFiles {
		if !bootstrap.FileExists(filepath.Join(target, file.Name)) {
			missing = append(missing, file.Name)
		}
	}
	return missing
}

func extractExpectedFiles(archivePath, target string) (int, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return 0, fmt.Errorf("open MovieLens archive %s: %w", archivePath, err)
	}
	defer reader.Close()

	remaining := make(map[string]struct{}, len(expectedFiles))
	for _, file := range expectedFiles {
		remaining[file.Name] = struct{}{}
	}

	copied := 0
	for _, file := range reader.File {
		name := expectedArchiveFileName(file)
		if _, ok := remaining[name]; !ok {
			continue
		}
		safeName, err := bootstrap.SafeZipEntryPath(name)
		if err != nil {
			return copied, fmt.Errorf("unsafe MovieLens archive entry %q: %w", file.Name, err)
		}
		if err := bootstrap.ExtractZipFile(file, target, safeName, "MovieLens"); err != nil {
			return copied, err
		}
		delete(remaining, name)
		copied++
	}

	var missing []string
	for _, file := range expectedFiles {
		if _, ok := remaining[file.Name]; ok {
			missing = append(missing, file.Name)
		}
	}
	if len(missing) > 0 {
		return copied, fmt.Errorf("expected CSVs missing from downloaded dataset: %s", strings.Join(missing, ", "))
	}
	return copied, nil
}

func expectedArchiveFileName(file *zip.File) string {
	if file.FileInfo().IsDir() {
		return ""
	}
	if _, err := bootstrap.SafeZipEntryPath(file.Name); err != nil {
		return ""
	}
	name := path.Clean(file.Name)
	if strings.HasPrefix(name, "../") || name == ".." || path.IsAbs(name) {
		return ""
	}
	return path.Base(name)
}

func verifyExpectedFileChecksums(target string) error {
	for _, file := range expectedFiles {
		got, err := md5File(filepath.Join(target, file.Name))
		if err != nil {
			return err
		}
		if got != file.MD5 {
			return fmt.Errorf("%s checksum = %s, want %s", file.Name, got, file.MD5)
		}
	}
	return nil
}

func md5File(file string) (string, error) {
	in, err := os.Open(file)
	if err != nil {
		return "", fmt.Errorf("open %s for checksum: %w", file, err)
	}
	defer in.Close()

	hash := md5.New()
	if _, err := io.Copy(hash, in); err != nil {
		return "", fmt.Errorf("read %s for checksum: %w", file, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
