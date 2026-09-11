package main

import (
	"archive/zip"
	"crypto/sha256"
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
	datasetHandle  = "olistbr/brazilian-ecommerce"
	datasetVersion = "2"
	archiveName    = "olistbr-brazilian-ecommerce-v2.zip"
	archiveDigest  = "967e41e04fc306fe604e2a693f488995a8b41e5047418f8a5c8e4abd6deca784"
	downloadURL    = "https://www.kaggle.com/api/v1/datasets/download/" + datasetHandle + "?dataset_version_number=" + datasetVersion
)

var expectedCSVs = []string{
	"olist_orders_dataset.csv",
	"olist_order_items_dataset.csv",
	"olist_order_payments_dataset.csv",
	"olist_products_dataset.csv",
	"olist_customers_dataset.csv",
	"olist_geolocation_dataset.csv",
	"olist_sellers_dataset.csv",
	"olist_order_reviews_dataset.csv",
	"product_category_name_translation.csv",
}

func main() {
	out := flag.String("out", "", "directory for downloaded Olist CSV files")
	sharedCache := flag.Bool("shared-cache", false, "store immutable CSVs in the user cache and link the output directory")
	flag.Parse()
	client := &http.Client{Timeout: 10 * time.Minute}
	var err error
	if *sharedCache {
		err = runShared(client, *out)
	} else {
		err = run(client, *out)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap olist: %v\n", err)
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
		Name:           datasetHandle + " version " + datasetVersion,
		ArchiveName:    archiveName,
		DownloadURL:    downloadURL,
		DownloadLabel:  "Olist dataset",
		CacheName:      "olist",
		AlreadyMessage: "Olist CSVs already available",
		MissingLabel:   "CSVs",
		FileLabel:      "CSV files",
		Force:          force,
		Missing:        missingCSVs,
		Extract:        extractExpectedCSVs,
		VerifyArchive: func(archivePath string) error {
			return verifyArchiveDigest(archivePath, archiveDigest)
		},
	})
}

func runShared(client *http.Client, out string) error {
	return bootstrap.RunShared(bootstrap.SharedOptions{
		Out:       out,
		CacheName: filepath.Join("olist", "v"+datasetVersion+"-"+archiveDigest),
		Name:      "Olist",
		Force:     bootstrap.Truthy(os.Getenv(configspec.EnvLEAPVIEW_BOOTSTRAP_FORCE)),
		Ready: func(directory string) error {
			missing := missingCSVs(directory)
			if len(missing) > 0 {
				return fmt.Errorf("missing Olist CSVs: %s", strings.Join(missing, ", "))
			}
			return nil
		},
		Populate: func(directory string) error {
			return runWithForce(client, directory, false)
		},
		Refresh: func(directory string) error {
			return runWithForce(client, directory, true)
		},
		CacheRoot: os.Getenv(configspec.EnvLEAPVIEW_DEV_ASSET_CACHE_DIR),
	})
}

func verifyArchiveDigest(archivePath, expected string) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open Olist archive for verification %s: %w", archivePath, err)
	}
	defer archive.Close()

	digest := sha256.New()
	if _, err := io.Copy(digest, archive); err != nil {
		return fmt.Errorf("hash Olist archive %s: %w", archivePath, err)
	}
	actual := fmt.Sprintf("%x", digest.Sum(nil))
	if actual != expected {
		return fmt.Errorf("Olist archive digest mismatch: got sha256:%s, want sha256:%s", actual, expected)
	}
	return nil
}

func missingCSVs(target string) []string {
	var missing []string
	for _, filename := range expectedCSVs {
		if !bootstrap.FileExists(filepath.Join(target, filename)) {
			missing = append(missing, filename)
		}
	}
	return missing
}

func extractExpectedCSVs(archivePath, target string) (int, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return 0, fmt.Errorf("open Olist archive %s: %w", archivePath, err)
	}
	defer reader.Close()

	remaining := make(map[string]struct{}, len(expectedCSVs))
	for _, filename := range expectedCSVs {
		remaining[filename] = struct{}{}
	}

	copied := 0
	for _, file := range reader.File {
		name := path.Clean(file.Name)
		if _, ok := remaining[name]; !ok || file.FileInfo().IsDir() {
			continue
		}
		if err := bootstrap.ExtractZipFile(file, filepath.Join(target, name), "Olist"); err != nil {
			return copied, err
		}
		delete(remaining, name)
		copied++
	}

	var missing []string
	for _, filename := range expectedCSVs {
		if _, ok := remaining[filename]; ok {
			missing = append(missing, filename)
		}
	}
	if len(missing) > 0 {
		return copied, fmt.Errorf("expected CSVs missing from downloaded dataset: %s", strings.Join(missing, ", "))
	}
	return copied, nil
}
