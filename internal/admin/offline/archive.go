package offline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
)

func (service *Service) Backup(ctx context.Context, request BackupRequest, out io.Writer) error {
	if request.Out == "" {
		return fmt.Errorf("admin backup requires --out")
	}
	lock, err := service.acquire(ctx)
	if err != nil {
		return err
	}
	defer lock.Release()
	options := BackupOptions{Path: request.Out, Environment: service.configuredEnvironment()}
	if request.Out == "-" {
		options.Path = ""
		options.Writer = out
	}
	if request.DatabaseOnly {
		if err := service.deps.Archive.BackupDatabase(ctx, options); err != nil {
			return err
		}
		if request.Out != "-" {
			fmt.Fprintf(out, "database backup written: %s\n", request.Out)
		}
		return nil
	}
	if err := service.validateFullInstanceArchiveLayout(); err != nil {
		return err
	}
	options.ExcludeRelativePaths, err = service.FullInstanceDerivedPaths()
	if err != nil {
		return err
	}
	options.StorageTopology, err = service.backupStorageTopology(request.ExternalRecoveryPoints)
	if err != nil {
		return err
	}
	if err := service.deps.Archive.BackupInstance(ctx, options); err != nil {
		return err
	}
	if request.Out != "-" {
		fmt.Fprintf(out, "instance backup written: %s\n", request.Out)
	}
	return nil
}

func (service *Service) Restore(ctx context.Context, request RestoreRequest, in io.Reader, out io.Writer) error {
	if request.From == "" {
		return fmt.Errorf("admin restore requires --from")
	}
	if !request.Confirm && !request.PreflightOnly {
		return fmt.Errorf("admin restore requires --confirm")
	}
	if request.DatabaseOnly && request.PreflightOnly {
		return fmt.Errorf("restore preflight is available only for full-instance archives")
	}
	if request.From == "-" && in == nil {
		return fmt.Errorf("admin restore --from - requires standard input")
	}
	lock, err := service.acquire(ctx)
	if err != nil {
		return err
	}
	defer lock.Release()
	environment, err := service.restoreTargetEnvironment(ctx)
	if err != nil {
		return err
	}
	options := RestoreOptions{
		Path:                request.From,
		CurrentBackup:       request.CurrentBackup,
		ExpectedEnvironment: environment,
		ExternalEvidence:    request.ExternalEvidence,
	}
	if request.From == "-" {
		options.Path = ""
		options.Reader = in
	}
	if request.CurrentBackup == "-" {
		options.CurrentBackup = ""
		options.DiscardCurrentBackup = true
	}
	label := request.From
	if label == "-" {
		label = "stdin"
	}
	if request.DatabaseOnly {
		if err := service.deps.Archive.RestoreDatabase(ctx, options); err != nil {
			return err
		}
		fmt.Fprintf(out, "database restored from: %s\n", label)
		if request.CurrentBackup != "" && request.CurrentBackup != "-" {
			fmt.Fprintf(out, "previous database backup: %s\n", request.CurrentBackup)
		}
		return nil
	}
	if err := service.validateFullInstanceArchiveLayout(); err != nil {
		return err
	}
	options.ResetRelativePaths, err = service.FullInstanceDerivedPaths()
	if err != nil {
		return err
	}
	if request.PreflightOnly {
		result, preflightErr := service.deps.Archive.PreflightInstance(ctx, options)
		if len(result.Document) > 0 {
			if _, err := out.Write(result.Document); err != nil {
				return err
			}
			if result.Document[len(result.Document)-1] != '\n' {
				if _, err := io.WriteString(out, "\n"); err != nil {
					return err
				}
			}
		}
		return preflightErr
	}
	if err := service.deps.Archive.RestoreInstance(ctx, options); err != nil {
		return err
	}
	fmt.Fprintf(out, "instance restored from: %s\n", label)
	if request.CurrentBackup != "" && request.CurrentBackup != "-" {
		fmt.Fprintf(out, "previous instance backup: %s\n", request.CurrentBackup)
	}
	return nil
}

func (service *Service) backupStorageTopology(points []ExternalRecoveryPoint) (BackupStorageTopology, error) {
	topology := BackupStorageTopology{ControlPlane: "local", ManagedData: "local", DuckLake: "local", ExternalStores: []BackupExternalStoreReference{}}
	backend := strings.TrimSpace(service.config.ManagedDataBackend)
	switch backend {
	case "", "local":
		if len(points) != 0 {
			return BackupStorageTopology{}, fmt.Errorf("external recovery points were supplied for local managed-data storage")
		}
		return topology, nil
	case "s3":
		bucket := strings.TrimSpace(service.config.ManagedDataS3Bucket)
		if bucket == "" {
			return BackupStorageTopology{}, fmt.Errorf("S3 managed-data backup requires a configured bucket")
		}
		var managed *ExternalRecoveryPoint
		for index := range points {
			if points[index].Role != "managed-data" {
				return BackupStorageTopology{}, fmt.Errorf("unsupported external recovery role %q", points[index].Role)
			}
			if managed != nil {
				return BackupStorageTopology{}, fmt.Errorf("managed-data external recovery point is duplicated")
			}
			managed = &points[index]
		}
		if managed == nil || strings.TrimSpace(managed.RecoveryPoint) == "" || strings.TrimSpace(managed.EvidenceKey) == "" {
			return BackupStorageTopology{}, fmt.Errorf("S3 managed-data backup requires an exact external recovery point and evidence key")
		}
		namespace := bucket
		if prefix := strings.Trim(strings.TrimSpace(service.config.ManagedDataS3Prefix), "/"); prefix != "" {
			namespace = path.Join(bucket, prefix)
		}
		topology.ManagedData = "external"
		topology.ExternalStores = []BackupExternalStoreReference{{
			Role: "managed-data", Backend: "s3", Namespace: namespace,
			RecoveryPoint: strings.TrimSpace(managed.RecoveryPoint), EvidenceKey: strings.TrimSpace(managed.EvidenceKey),
		}}
		return topology, nil
	default:
		return BackupStorageTopology{}, fmt.Errorf("unsupported managed-data backend %q", backend)
	}
}

func (service *Service) restoreTargetEnvironment(ctx context.Context) (string, error) {
	environment, exists, err := service.deps.State.ExistingEnvironment(ctx)
	if exists && errors.Is(err, ErrStateNotFound) {
		// Restore preflight is read-only. An unbound target is validated against
		// the configured environment and is replaced only after the archive plan
		// has passed; the restored database carries its own durable binding.
		return service.configuredEnvironment(), nil
	}
	if err != nil {
		return "", err
	}
	if exists {
		requested := strings.TrimSpace(service.config.Environment)
		if requested != "" && requested != environment {
			return "", fmt.Errorf("LeapView instance is bound to environment %q, not %q", environment, requested)
		}
		return environment, nil
	}
	return service.configuredEnvironment(), nil
}

func (service *Service) validateFullInstanceArchiveLayout() error {
	homeAbs, err := filepath.Abs(service.config.HomeDir)
	if err != nil {
		return err
	}
	paths := map[string]string{
		"DuckLake catalog": service.config.DuckLakeCatalog,
		"DuckLake data":    service.config.DuckLakeData,
		"artifact":         service.config.ArtifactDir,
		"runtime":          service.config.RuntimeDir,
	}
	if service.config.ManagedDataBackend == "local" || service.config.ManagedDataBackend == "" {
		paths["managed-data"] = service.config.ManagedDataDir
	}
	for label, path := range paths {
		pathAbs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(homeAbs, pathAbs)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("full instance backup/restore requires %s path inside LEAPVIEW_HOME; got %s outside %s", label, path, service.config.HomeDir)
		}
	}
	return nil
}

func (service *Service) FullInstanceDerivedPaths() ([]string, error) {
	homeAbs, err := filepath.Abs(service.config.HomeDir)
	if err != nil {
		return nil, err
	}
	managedDataAbs, err := filepath.Abs(service.config.ManagedDataDir)
	if err != nil {
		return nil, err
	}
	backend := strings.TrimSpace(service.config.ManagedDataBackend)
	var derivedPath string
	switch backend {
	case "", "local":
		derivedPath = filepath.Join(managedDataAbs, "objects", "revisions")
	case "s3":
		derivedPath = filepath.Join(managedDataAbs, "runtime")
	default:
		return nil, fmt.Errorf("unsupported managed-data backend %q", backend)
	}
	relative, err := filepath.Rel(homeAbs, derivedPath)
	if err != nil {
		return nil, err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		if backend == "s3" {
			return nil, nil
		}
		return nil, fmt.Errorf("local managed-data derived path %s is outside %s", derivedPath, service.config.HomeDir)
	}
	return []string{filepath.ToSlash(relative)}, nil
}
