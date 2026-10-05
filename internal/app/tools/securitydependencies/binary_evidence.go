package main

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

const (
	maxBinaryBytes        = 256 << 20
	maxBinaryReportBytes  = 32 << 20
	maxBinarySummaryBytes = 2 << 20
	binaryEvidenceMaxAge  = 120 * time.Hour
	siteMainPackage       = "github.com/flidai/leapview/cmd/leapview-site"
)

type binaryEvidence struct {
	SchemaVersion int              `json:"schemaVersion"`
	Scope         string           `json:"scope"`
	BinarySHA256  string           `json:"binarySHA256"`
	BuildInfo     *debug.BuildInfo `json:"buildInfo"`
	Scanner       govulnConfig     `json:"scanner"`
	ReportSHA256  string           `json:"reportSHA256"`
	ScannedAt     string           `json:"scannedAt"`
}

var releaseGoVersion = regexp.MustCompile(`^go1\.[0-9]+\.[0-9]+$`)

func binaryDigest(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }

func readRegularBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("input must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errors.New("input changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if len(data) > int(limit) {
		return nil, errors.New("input exceeds byte limit")
	}
	return data, err
}

// Read metadata and hash from the same bytes. Never execute the candidate or
// allow govulncheck's serialized-Bin input to masquerade as a shipped binary.
func readExactBinary(path, program, platform string) (*debug.BuildInfo, string, error) {
	data, err := readRegularBounded(path, maxBinaryBytes)
	if err != nil {
		return nil, "", err
	}
	return inspectExactBinary(data, program, platform)
}

func inspectExactBinary(data []byte, program, platform string) (*debug.BuildInfo, string, error) {
	if program == "" || strings.TrimSpace(program) != program {
		return nil, "", errors.New("expected main package is required")
	}
	file, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("candidate must be an ELF Go binary: %w", err)
	}
	defer file.Close()
	machine := map[string]elf.Machine{"linux/amd64": elf.EM_X86_64, "linux/arm64": elf.EM_AARCH64}
	want, supported := machine[platform]
	if !supported || file.Machine != want || file.Class != elf.ELFCLASS64 || file.Data != elf.ELFDATA2LSB {
		return nil, "", errors.New("unsupported or mismatched ELF platform")
	}
	if program == siteMainPackage {
		for _, segment := range file.Progs {
			if segment.Type == elf.PT_INTERP || segment.Type == elf.PT_DYNAMIC {
				return nil, "", errors.New("site binary must be statically linked")
			}
		}
	}
	info, err := buildinfo.Read(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	if info.Path != program || info.Main.Path == "" || !releaseGoVersion.MatchString(info.GoVersion) {
		return nil, "", errors.New("binary main package, module or released Go version is missing or mismatched")
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		if _, exists := settings[setting.Key]; exists {
			return nil, "", errors.New("duplicate binary build setting")
		}
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"]+"/"+settings["GOARCH"] != platform {
		return nil, "", errors.New("binary build settings differ from expected platform")
	}
	if program == siteMainPackage && settings["CGO_ENABLED"] != "0" {
		return nil, "", errors.New("site binary must be built with CGO_ENABLED=0")
	}
	return info, binaryDigest(data), nil
}

func binaryModules(info *debug.BuildInfo) []govulnModule {
	modules := []govulnModule{{Path: info.Main.Path, Version: info.Main.Version}, {Path: "stdlib", Version: info.GoVersion}}
	for _, module := range info.Deps {
		if module.Replace != nil {
			module = module.Replace
		}
		modules = append(modules, govulnModule{Path: module.Path, Version: module.Version})
	}
	return modules
}

func validateBinaryStream(data []byte, info *debug.BuildInfo) (govulnStream, error) {
	if len(data) > maxBinaryReportBytes {
		return govulnStream{}, errors.New("binary report exceeds byte limit")
	}
	stream, err := parseGovulnStream(data, "binary")
	if err != nil {
		return govulnStream{}, err
	}
	if stream.config.Database != "https://vuln.go.dev" {
		return govulnStream{}, errors.New("binary scanner must use the Go vulnerability database")
	}
	if _, err := time.Parse(time.RFC3339Nano, stream.config.DBLastModified); err != nil {
		return govulnStream{}, errors.New("binary database modification time is invalid")
	}
	if stream.sbom.GoVersion != info.GoVersion || !reflect.DeepEqual(stream.sbom.Roots, []string{info.Main.Path}) {
		return govulnStream{}, errors.New("binary SBOM root or Go version differs from build metadata")
	}
	expected := binaryModules(info)
	actual := append([]govulnModule(nil), stream.sbom.Modules...)
	less := func(modules []govulnModule) func(int, int) bool {
		return func(i, j int) bool {
			if modules[i].Path == modules[j].Path {
				return modules[i].Version < modules[j].Version
			}
			return modules[i].Path < modules[j].Path
		}
	}
	sort.Slice(expected, less(expected))
	sort.Slice(actual, less(actual))
	if !reflect.DeepEqual(actual, expected) {
		return govulnStream{}, errors.New("binary SBOM module versions differ from build metadata")
	}
	if len(stream.findings) != 0 {
		return govulnStream{}, fmt.Errorf("binary has vulnerable symbols: %s", stream.findings[0].OSV)
	}
	return stream, nil
}

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func (r *runner) scanBinaryEvidence(path, program, platform, directory string) error {
	data, err := readRegularBounded(path, maxBinaryBytes)
	if err != nil {
		return err
	}
	info, digest, err := inspectExactBinary(data, program, platform)
	if err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return fmt.Errorf("create new binary evidence directory: %w", err)
	}
	// The scanner reads a private snapshot of the exact bytes whose metadata and
	// digest we inspected. A concurrent A -> B -> A swap of the original cannot
	// substitute scanner input while preserving the receipt's binary digest.
	snapshotDir, err := os.MkdirTemp("", "leapview-binary-scan-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(snapshotDir) }()
	snapshot := filepath.Join(snapshotDir, "binary")
	if err := os.WriteFile(snapshot, data, 0400); err != nil {
		return err
	}
	if r.govulncheckPath == "" {
		binary, cleanup, err := r.prepareGovulncheck()
		if err != nil {
			return err
		}
		defer cleanup()
		r.govulncheckPath = binary
		defer func() { r.govulncheckPath = "" }()
	}
	args := []string{"-mode=binary", "-scan=symbol", "-json", snapshot}
	var result commandResult
	if r.govulnCommand != nil {
		result = r.govulnCommand(r.root, r.govulncheckPath, args...)
	} else {
		result = r.commandWithEnvLimited(r.root, r.govulncheckPath, nil, maxBinaryReportBytes, maxDiagnosticBytes, args...)
	}
	// Failed scans retain bounded diagnostics, but can never leave a success receipt.
	if len(result.stdout) > maxBinaryReportBytes {
		return errors.New("binary scanner output exceeds byte limit")
	}
	if err := writeExclusive(filepath.Join(directory, "govulncheck.json"), result.stdout); err != nil {
		return err
	}
	if len(result.stderr) != 0 {
		if err := writeExclusive(filepath.Join(directory, "stderr.txt"), commandOutput(result.stderr)); err != nil {
			return err
		}
		return errors.New("binary scanner emitted diagnostics on stderr")
	}
	if isCommandLifecycleError(result) || result.status != 0 || result.err != nil {
		return errors.New("binary scanner did not complete successfully")
	}
	stream, err := validateBinaryStream(result.stdout, info)
	if err != nil {
		return err
	}
	_, after, err := readExactBinary(path, program, platform)
	if err != nil {
		return err
	}
	if digest != after {
		return errors.New("binary changed during scan")
	}
	scanned := r.nowUTC()
	modified, _ := time.Parse(time.RFC3339Nano, stream.config.DBLastModified)
	if modified.After(scanned) {
		return errors.New("binary database modification time is later than scan")
	}
	receipt := binaryEvidence{SchemaVersion: 1, Scope: "go-binary-only", BinarySHA256: digest, BuildInfo: info, Scanner: stream.config, ReportSHA256: binaryDigest(result.stdout), ScannedAt: scanned.Format(time.RFC3339Nano)}
	data, err = json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxBinarySummaryBytes {
		return errors.New("binary summary exceeds byte limit")
	}
	return writeExclusive(filepath.Join(directory, "summary.json"), append(data, '\n'))
}

func (r *runner) verifyBinaryEvidence(path, program, platform, directory string) error {
	info, digest, err := readExactBinary(path, program, platform)
	if err != nil {
		return err
	}
	data, err := readRegularBounded(filepath.Join(directory, "summary.json"), maxBinarySummaryBytes)
	if err != nil {
		return err
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt binaryEvidence
	if err := decoder.Decode(&receipt); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("binary summary has trailing JSON")
	}
	if receipt.SchemaVersion != 1 || receipt.Scope != "go-binary-only" || receipt.BinarySHA256 != digest || !reflect.DeepEqual(receipt.BuildInfo, info) {
		return errors.New("binary evidence identity differs from candidate")
	}
	scanned, err := time.Parse(time.RFC3339Nano, receipt.ScannedAt)
	if err != nil {
		return err
	}
	age := r.nowUTC().Sub(scanned)
	if age < 0 || age >= binaryEvidenceMaxAge {
		return errors.New("binary evidence scan is stale or from the future")
	}
	report, err := readRegularBounded(filepath.Join(directory, "govulncheck.json"), maxBinaryReportBytes)
	if err != nil {
		return err
	}
	if binaryDigest(report) != receipt.ReportSHA256 {
		return errors.New("binary raw report changed since scan")
	}
	stream, err := validateBinaryStream(report, info)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(receipt.Scanner, stream.config) {
		return errors.New("binary scanner identity changed since scan")
	}
	modified, _ := time.Parse(time.RFC3339Nano, stream.config.DBLastModified)
	if modified.After(scanned) {
		return errors.New("binary database modification time is later than scan")
	}
	return nil
}
