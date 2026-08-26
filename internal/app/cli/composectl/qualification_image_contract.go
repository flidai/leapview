package composectl

import (
	"archive/zip"
	"context"
	"crypto/x509"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const (
	qualificationRuntimeCAPath       = "/etc/ssl/certs/ca-certificates.crt"
	qualificationRuntimeZoneinfoPath = "/usr/local/share/leapview/zoneinfo.zip"
)

var qualificationRequiredRuntimeLibraries = []string{
	"libc.so.6",
	"libgcc_s.so.1",
	"libm.so.6",
	"libstdc++.so.6",
}

func (c *Controller) qualifyProductionImageContract(
	ctx context.Context,
	image string,
) (runErr error) {
	if _, err := c.qualificationDocker(ctx, nil, "pull", image); err != nil {
		return fmt.Errorf("pull production image for contract inspection: %w", err)
	}
	if _, _, err := c.qualificationImageIdentity(ctx, image); err != nil {
		return err
	}
	if err := c.qualifyProductionImageEnvironment(ctx, image); err != nil {
		return err
	}

	container := normalizedQualificationName(
		"leapview-image-contract-" + strconv.Itoa(os.Getpid()),
	)
	if _, err := c.qualificationDocker(
		ctx, nil,
		"create", "--name", container,
		image,
		"version", "--json",
	); err != nil {
		return fmt.Errorf("create production image inspection container: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), qualificationCleanupTimeout)
		defer cancel()
		_, cleanupErr := c.qualificationDocker(cleanupCtx, nil, "rm", "--force", container)
		runErr = errors.Join(runErr, ignoreQualificationNotFound(cleanupErr))
	}()

	for _, forbidden := range []string{
		"/bin/sh",
		"/bin/bash",
		"/bin/busybox",
		"/sbin/apk",
		"/usr/bin/apk",
		"/usr/bin/apt-get",
		"/usr/bin/curl",
		"/usr/bin/wget",
	} {
		exists, err := c.qualificationContainerPathExists(ctx, container, forbidden)
		if err != nil {
			return fmt.Errorf("inspect forbidden runtime path %s: %w", forbidden, err)
		}
		if exists {
			return fmt.Errorf("production image contains forbidden runtime path %s", forbidden)
		}
	}

	zoneinfo, cleanupZoneinfo, err := c.qualificationCopyFromContainer(
		ctx, container, qualificationRuntimeZoneinfoPath,
	)
	if err != nil {
		return fmt.Errorf("copy production timezone database: %w", err)
	}
	if err := verifyQualificationZoneinfo(zoneinfo); err != nil {
		cleanupZoneinfo()
		return err
	}
	if err := verifyQualificationFileMode(zoneinfo, 0o444); err != nil {
		cleanupZoneinfo()
		return fmt.Errorf("verify production timezone database mode: %w", err)
	}
	cleanupZoneinfo()

	certificates, cleanupCertificates, err := c.qualificationCopyFromContainer(
		ctx, container, qualificationRuntimeCAPath,
	)
	if err != nil {
		return fmt.Errorf("copy production CA bundle: %w", err)
	}
	if err := verifyQualificationCertificates(certificates); err != nil {
		cleanupCertificates()
		return err
	}
	if err := verifyQualificationFileMode(certificates, 0o444); err != nil {
		cleanupCertificates()
		return fmt.Errorf("verify production CA bundle mode: %w", err)
	}
	cleanupCertificates()

	neededLibraries := map[string]struct{}{}
	for _, binary := range []string{
		"/usr/local/bin/leapview",
		"/usr/local/libexec/leapviewctl",
	} {
		localBinary, cleanupBinary, err := c.qualificationCopyFromContainer(
			ctx, container, binary,
		)
		if err != nil {
			return fmt.Errorf("copy production binary %s: %w", binary, err)
		}
		if err := verifyQualificationFileMode(localBinary, 0o555); err != nil {
			cleanupBinary()
			return fmt.Errorf("verify production binary mode %s: %w", binary, err)
		}
		libraries, interpreter, err := inspectQualificationELF(localBinary)
		cleanupBinary()
		if err != nil {
			return fmt.Errorf("inspect production binary %s: %w", binary, err)
		}
		for _, library := range libraries {
			neededLibraries[library] = struct{}{}
		}
		if interpreter == "" {
			return fmt.Errorf("production binary %s does not declare a dynamic loader", binary)
		}
		exists, err := c.qualificationContainerPathExists(ctx, container, interpreter)
		if err != nil {
			return fmt.Errorf("inspect dynamic loader %s: %w", interpreter, err)
		}
		if !exists {
			return fmt.Errorf("production image is missing dynamic loader %s", interpreter)
		}
	}
	for _, required := range qualificationRequiredRuntimeLibraries {
		if _, ok := neededLibraries[required]; !ok {
			return fmt.Errorf("production binaries do not declare required shared library %s", required)
		}
	}
	for library := range neededLibraries {
		exists, err := c.qualificationContainerPathExists(
			ctx, container, "/usr/lib/"+library,
		)
		if err != nil {
			return fmt.Errorf("inspect runtime library %s: %w", library, err)
		}
		if !exists {
			return fmt.Errorf("production image is missing runtime library %s", library)
		}
	}

	output, err := c.qualificationDocker(
		ctx, nil,
		"run", "--rm",
		"--entrypoint", "/usr/local/libexec/leapviewctl",
		image,
		"version", "--json",
	)
	if err != nil {
		return fmt.Errorf("execute production leapviewctl: %w", err)
	}
	var identity struct {
		Product string `json:"product"`
	}
	if err := json.Unmarshal(output, &identity); err != nil {
		return fmt.Errorf("decode production leapviewctl identity: %w", err)
	}
	if identity.Product != "leapviewctl" {
		return fmt.Errorf("production leapviewctl identity is %q", identity.Product)
	}
	return nil
}

func verifyQualificationFileMode(path string, wanted os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != wanted {
		return fmt.Errorf("mode is %04o, want %04o", info.Mode().Perm(), wanted)
	}
	return nil
}

func (c *Controller) qualifyProductionImageEnvironment(ctx context.Context, image string) error {
	output, err := c.qualificationDocker(
		ctx, nil,
		"image", "inspect", image,
		"--format", "{{range .Config.Env}}{{println .}}{{end}}",
	)
	if err != nil {
		return fmt.Errorf("inspect production image environment: %w", err)
	}
	environment := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && name != "" {
			environment[name] = value
		}
	}
	for name, wanted := range map[string]string{
		"SSL_CERT_FILE": qualificationRuntimeCAPath,
		"ZONEINFO":      qualificationRuntimeZoneinfoPath,
	} {
		if environment[name] != wanted {
			return fmt.Errorf("production image %s=%q, want %q", name, environment[name], wanted)
		}
	}
	return nil
}

func verifyQualificationZoneinfo(path string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("open production timezone database: %w", err)
	}
	defer archive.Close()
	required := map[string]bool{
		"America/New_York":  false,
		"Europe/Copenhagen": false,
		"UTC":               false,
	}
	for _, file := range archive.File {
		if _, ok := required[file.Name]; ok && file.UncompressedSize64 > 0 {
			required[file.Name] = true
		}
	}
	for name, present := range required {
		if !present {
			return fmt.Errorf("production timezone database is missing %s", name)
		}
	}
	return nil
}

func verifyQualificationCertificates(path string) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read production CA bundle: %w", err)
	}
	if len(payload) == 0 || !x509.NewCertPool().AppendCertsFromPEM(payload) {
		return fmt.Errorf("production CA bundle contains no trusted certificates")
	}
	return nil
}

func inspectQualificationELF(path string) ([]string, string, error) {
	file, err := elf.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	wantedMachine := elf.EM_X86_64
	if runtime.GOARCH == "arm64" {
		wantedMachine = elf.EM_AARCH64
	}
	if file.Machine != wantedMachine {
		return nil, "", fmt.Errorf("ELF machine %s does not match %s", file.Machine, runtime.GOARCH)
	}
	libraries, err := file.ImportedLibraries()
	if err != nil {
		return nil, "", err
	}
	interpreter := ""
	for _, program := range file.Progs {
		if program.Type != elf.PT_INTERP {
			continue
		}
		if program.Filesz == 0 || program.Filesz > 4096 {
			return nil, "", fmt.Errorf("invalid ELF interpreter size %d", program.Filesz)
		}
		payload := make([]byte, program.Filesz)
		if _, err := program.ReadAt(payload, 0); err != nil && !errors.Is(err, io.EOF) {
			return nil, "", err
		}
		interpreter = strings.TrimRight(string(payload), "\x00")
		break
	}
	return libraries, interpreter, nil
}
