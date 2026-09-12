package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/csv"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	configspec "github.com/flidai/leapview/internal/app/config/spec"
	"github.com/flidai/leapview/internal/app/tools/internal/sharedassets"
)

const (
	datasetName    = "Microsoft Financial Sample"
	fileName       = "financial-sample.xlsx"
	csvFileName    = "financial-sample.csv"
	fileDigest     = "c3f17156ab7c192571ecfc742e88c16a4b7243a4d4b4a420fcb68dec44e10196"
	downloadURL    = "https://download.microsoft.com/download/1/4/E/14EDED28-6C58-4055-A65C-23B4DA81C4DE/Financial%20Sample.xlsx"
	datasetVersion = "2014-12-16"
)

func main() {
	out := flag.String("out", "", "directory for the Microsoft Financial Sample workbook")
	sharedCache := flag.Bool("shared-cache", false, "store the immutable workbook in the user cache and link the output directory")
	flag.Parse()

	client := &http.Client{Timeout: 10 * time.Minute}
	var err error
	if *sharedCache {
		err = runShared(client, *out)
	} else {
		err = run(client, *out)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap finance demo: %v\n", err)
		os.Exit(1)
	}
}

func run(client *http.Client, out string) error {
	return runWithForce(client, out, truthy(os.Getenv(configspec.EnvLEAPVIEW_BOOTSTRAP_FORCE)))
}

func runWithForce(client *http.Client, out string, force bool) error {
	target, err := targetDir(out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create finance demo data directory %s: %w", target, err)
	}

	destination := filepath.Join(target, fileName)
	csvDestination := filepath.Join(target, csvFileName)
	if !force {
		if err := verifyFileDigest(destination, fileDigest); err == nil && verifyNormalizedCSV(csvDestination) == nil {
			fmt.Printf("%s already available in %s\n", datasetName, target)
			return nil
		}
	}

	if force || verifyFileDigest(destination, fileDigest) != nil {
		if err := downloadFile(client, destination); err != nil {
			return err
		}
	}
	if err := verifyFileDigest(destination, fileDigest); err != nil {
		return err
	}
	if err := normalizeWorkbook(destination, csvDestination); err != nil {
		return err
	}
	if err := verifyNormalizedCSV(csvDestination); err != nil {
		return err
	}

	if force {
		fmt.Println("Force refresh requested")
	}
	return printSummary(destination)
}

func runShared(client *http.Client, out string) error {
	target, err := targetDir(out)
	if err != nil {
		return err
	}
	root, err := sharedassets.CacheRoot(os.Getenv(configspec.EnvLEAPVIEW_DEV_ASSET_CACHE_DIR))
	if err != nil {
		return err
	}
	shared := filepath.Join(root, "datasets", "microsoft-financial-sample", datasetVersion+"-"+fileDigest)
	ready := func(directory string) error {
		if err := verifyFileDigest(filepath.Join(directory, fileName), fileDigest); err != nil {
			return err
		}
		return verifyNormalizedCSV(filepath.Join(directory, csvFileName))
	}
	hadReadyAssets := ready(shared) == nil
	if info, statErr := os.Lstat(target); statErr == nil && info.IsDir() && ready(target) == nil {
		hadReadyAssets = true
	}
	if err := sharedassets.Ensure(sharedassets.Options{
		Local:  target,
		Shared: shared,
		Ready:  ready,
		Populate: func(directory string) error {
			return runWithForce(client, directory, false)
		},
	}); err != nil {
		return err
	}
	if truthy(os.Getenv(configspec.EnvLEAPVIEW_BOOTSTRAP_FORCE)) && hadReadyAssets {
		if err := runWithForce(client, target, true); err != nil {
			return err
		}
	}
	fmt.Printf("Using shared %s assets at %s (linked from %s)\n", datasetName, shared, target)
	return nil
}

func targetDir(out string) (string, error) {
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("out is required")
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("resolve output directory %s: %w", out, err)
	}
	return abs, nil
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func verifyFileDigest(file, expected string) error {
	input, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("open finance demo workbook for verification %s: %w", file, err)
	}
	defer input.Close()

	digest := sha256.New()
	if _, err := io.Copy(digest, input); err != nil {
		return fmt.Errorf("hash finance demo workbook %s: %w", file, err)
	}
	actual := fmt.Sprintf("%x", digest.Sum(nil))
	if actual != expected {
		return fmt.Errorf("finance demo workbook digest mismatch: got sha256:%s, want sha256:%s", actual, expected)
	}
	return nil
}

func downloadFile(client *http.Client, destination string) error {
	return downloadFileFrom(client, destination, downloadURL)
}

func downloadFileFrom(client *http.Client, destination, sourceURL string) error {
	tmp, err := os.CreateTemp(filepath.Dir(destination), filepath.Base(destination)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary finance demo workbook: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	req, err := http.NewRequest(http.MethodGet, sourceURL, nil)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("create Microsoft Financial Sample request: %w", err)
	}
	req.Header.Set("User-Agent", "LeapView bootstrap")

	resp, err := client.Do(req)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("download Microsoft Financial Sample: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = tmp.Close()
		return fmt.Errorf("download Microsoft Financial Sample: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write Microsoft Financial Sample: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close Microsoft Financial Sample: %w", err)
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return fmt.Errorf("store Microsoft Financial Sample at %s: %w", destination, err)
	}
	return nil
}

var normalizedHeaders = []string{
	"segment",
	"country",
	"product",
	"discount_band",
	"units_sold",
	"manufacturing_price",
	"sale_price",
	"gross_sales",
	"discounts",
	"net_sales",
	"cogs",
	"profit",
	"date_serial",
	"month_number",
	"month_name",
	"year",
}

type xlsxSharedStrings struct {
	Items []xlsxSharedString `xml:"si"`
}

type xlsxSharedString struct {
	Text string        `xml:"t"`
	Runs []xlsxTextRun `xml:"r"`
}

type xlsxTextRun struct {
	Text string `xml:"t"`
}

type xlsxWorksheet struct {
	Rows []xlsxRow `xml:"sheetData>row"`
}

type xlsxRow struct {
	Cells []xlsxCell `xml:"c"`
}

type xlsxCell struct {
	Reference string `xml:"r,attr"`
	Type      string `xml:"t,attr"`
	Value     string `xml:"v"`
}

func normalizeWorkbook(workbook, destination string) error {
	reader, err := zip.OpenReader(workbook)
	if err != nil {
		return fmt.Errorf("open Microsoft Financial Sample workbook %s: %w", workbook, err)
	}
	defer reader.Close()

	shared, err := readSharedStrings(reader.File)
	if err != nil {
		return err
	}
	rows, err := readWorksheetRows(reader.File, shared)
	if err != nil {
		return err
	}
	if len(rows) != 701 {
		return fmt.Errorf("Microsoft Financial Sample row count = %d, want 701 including header", len(rows))
	}

	tmp, err := os.CreateTemp(filepath.Dir(destination), filepath.Base(destination)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary normalized finance CSV: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	writer := csv.NewWriter(tmp)
	if err := writer.Write(normalizedHeaders); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write normalized finance CSV header: %w", err)
	}
	for _, row := range rows[1:] {
		if err := writer.Write(row); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("write normalized finance CSV row: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("flush normalized finance CSV: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close normalized finance CSV: %w", err)
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return fmt.Errorf("store normalized finance CSV at %s: %w", destination, err)
	}
	return nil
}

func readSharedStrings(files []*zip.File) ([]string, error) {
	file := findZipFile(files, "xl/sharedStrings.xml")
	if file == nil {
		return nil, fmt.Errorf("Microsoft Financial Sample workbook has no shared strings")
	}
	input, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open workbook shared strings: %w", err)
	}
	defer input.Close()
	var document xlsxSharedStrings
	if err := xml.NewDecoder(input).Decode(&document); err != nil {
		return nil, fmt.Errorf("decode workbook shared strings: %w", err)
	}
	result := make([]string, 0, len(document.Items))
	for _, item := range document.Items {
		value := item.Text
		for _, run := range item.Runs {
			value += run.Text
		}
		result = append(result, value)
	}
	return result, nil
}

func readWorksheetRows(files []*zip.File, shared []string) ([][]string, error) {
	file := findZipFile(files, "xl/worksheets/sheet1.xml")
	if file == nil {
		return nil, fmt.Errorf("Microsoft Financial Sample workbook has no Sheet1 data")
	}
	input, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open workbook Sheet1: %w", err)
	}
	defer input.Close()
	var document xlsxWorksheet
	if err := xml.NewDecoder(input).Decode(&document); err != nil {
		return nil, fmt.Errorf("decode workbook Sheet1: %w", err)
	}
	rows := make([][]string, 0, len(document.Rows))
	for rowNumber, source := range document.Rows {
		row := make([]string, len(normalizedHeaders))
		for _, cell := range source.Cells {
			column, err := xlsxColumnIndex(cell.Reference)
			if err != nil {
				return nil, fmt.Errorf("decode workbook row %d: %w", rowNumber+1, err)
			}
			if column >= len(row) {
				continue
			}
			value := cell.Value
			if cell.Type == "s" {
				var index int
				if _, err := fmt.Sscanf(cell.Value, "%d", &index); err != nil || index < 0 || index >= len(shared) {
					return nil, fmt.Errorf("shared-string index %q is invalid", cell.Value)
				}
				value = shared[index]
			}
			row[column] = value
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func xlsxColumnIndex(reference string) (int, error) {
	index := 0
	letters := 0
	for _, character := range reference {
		if character < 'A' || character > 'Z' {
			break
		}
		index = index*26 + int(character-'A'+1)
		letters++
	}
	if letters == 0 {
		return 0, fmt.Errorf("cell reference %q has no column", reference)
	}
	return index - 1, nil
}

func findZipFile(files []*zip.File, name string) *zip.File {
	for _, file := range files {
		if file.Name == name {
			return file
		}
	}
	return nil
}

func verifyNormalizedCSV(file string) error {
	input, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("open normalized finance CSV %s: %w", file, err)
	}
	defer input.Close()
	reader := csv.NewReader(input)
	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read normalized finance CSV %s: %w", file, err)
	}
	if len(rows) != 701 {
		return fmt.Errorf("normalized finance CSV row count = %d, want 701 including header", len(rows))
	}
	if strings.Join(rows[0], ",") != strings.Join(normalizedHeaders, ",") {
		return fmt.Errorf("normalized finance CSV header is invalid")
	}
	return nil
}

func printSummary(destination string) error {
	info, err := os.Stat(destination)
	if err != nil {
		return fmt.Errorf("stat bootstrapped finance demo workbook: %w", err)
	}
	fmt.Printf("Bootstrapped %s (%s)\n", datasetName, datasetVersion)
	fmt.Printf("Source: %s\n", downloadURL)
	fmt.Printf("Verified: sha256:%s\n", fileDigest)
	fmt.Printf("Stored %d bytes at %s\n", info.Size(), destination)
	return nil
}
