package managedmaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

// CapacityReserve is an incremental free-space allowance for one role. Roles
// sharing a filesystem need the SUM of their allowances, not the largest one.
// Measurements are point-in-time guards; they do not reserve filesystem space.
type CapacityReserve struct {
	FreeBytes  uint64 `json:"freeBytes"`
	FreeInodes uint64 `json:"freeInodes"`
}

type CapacityPolicy struct {
	DockerRootDir string          `json:"dockerRootDir"`
	Home          CapacityReserve `json:"home"`
	StateRoot     CapacityReserve `json:"stateRoot"`
	Docker        CapacityReserve `json:"docker"`
}

func canonicalCapacityPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && !strings.ContainsAny(path, "\r\n")
}

func (p CapacityPolicy) Validate() error {
	if !canonicalCapacityPath(p.DockerRootDir) {
		return errors.New("capacity policy requires a canonical enrolled Docker data root")
	}
	for _, reserve := range []CapacityReserve{p.Home, p.StateRoot, p.Docker} {
		if reserve.FreeBytes == 0 || reserve.FreeInodes == 0 {
			return errors.New("capacity policy requires positive byte and inode reserves for every role")
		}
	}
	return nil
}

type CapacityPath struct {
	Role       string          `json:"role"`
	Path       string          `json:"path"`
	FreeBytes  uint64          `json:"freeBytes"`
	FreeInodes uint64          `json:"freeInodes"`
	Reserve    CapacityReserve `json:"reserve"`
}

type CapacityFilesystem struct {
	Device         string         `json:"device"`
	Paths          []CapacityPath `json:"paths"`
	FreeBytes      uint64         `json:"freeBytes"`
	FreeInodes     uint64         `json:"freeInodes"`
	RequiredBytes  uint64         `json:"requiredBytes"`
	RequiredInodes uint64         `json:"requiredInodes"`
}

type CapacityReport struct {
	Passed      bool                 `json:"passed"`
	Policy      *CapacityPolicy      `json:"policy"`
	Filesystems []CapacityFilesystem `json:"filesystems"`
	Error       string               `json:"error,omitempty"`
}

type filesystemCapacity struct {
	Device                string
	FreeBytes, FreeInodes uint64
}

// Capacity inspects only the dedicated daemon and existing directories. It
// needs neither artifact receipts nor an operation/journal and never creates
// missing paths. The CLI exposes the same guard used by deployment preflight.
func (k *KamalEffects) Capacity(ctx context.Context) (report CapacityReport, err error) {
	report.Policy = k.Profile.Capacity
	defer func() {
		if err != nil {
			report.Error = err.Error()
		}
	}()
	if err = ctx.Err(); err != nil {
		return report, err
	}
	if report.Policy == nil {
		return report, errors.New("enrolled capacity policy is required")
	}
	if err = report.Policy.Validate(); err != nil {
		return report, err
	}
	raw, err := k.docker(ctx, "info", "--format", "{{json .DockerRootDir}}")
	if err != nil {
		return report, fmt.Errorf("inspect Docker data root: %w", err)
	}
	var dockerRoot string
	if json.Unmarshal(raw, &dockerRoot) != nil || !canonicalCapacityPath(dockerRoot) || dockerRoot != report.Policy.DockerRootDir {
		return report, errors.New("Docker data root differs from enrolled capacity policy")
	}
	probe := k.capacityProbe
	if probe == nil {
		probe = measureFilesystemCapacity
	}
	indexes := map[string]int{}
	for _, role := range []CapacityPath{
		{Role: "home", Path: k.Profile.Home, Reserve: report.Policy.Home},
		{Role: "stateRoot", Path: k.Profile.StateRoot, Reserve: report.Policy.StateRoot},
		{Role: "docker", Path: dockerRoot, Reserve: report.Policy.Docker},
	} {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		if !canonicalCapacityPath(role.Path) {
			return report, fmt.Errorf("invalid capacity path for %s", role.Role)
		}
		measurement, probeErr := probe(role.Path)
		if probeErr != nil {
			return report, fmt.Errorf("measure %s capacity: %w", role.Role, probeErr)
		}
		if measurement.Device == "" {
			return report, fmt.Errorf("unknown filesystem identity for %s", role.Role)
		}
		role.FreeBytes, role.FreeInodes = measurement.FreeBytes, measurement.FreeInodes
		index, found := indexes[measurement.Device]
		if !found {
			index = len(report.Filesystems)
			indexes[measurement.Device] = index
			report.Filesystems = append(report.Filesystems, CapacityFilesystem{Device: measurement.Device, FreeBytes: measurement.FreeBytes, FreeInodes: measurement.FreeInodes})
		}
		fs := &report.Filesystems[index]
		fs.Paths = append(fs.Paths, role)
		// Paths on a shared device can be sampled at different instants. Use
		// the lowest availability rather than accidentally counting it twice.
		fs.FreeBytes = min(fs.FreeBytes, measurement.FreeBytes)
		fs.FreeInodes = min(fs.FreeInodes, measurement.FreeInodes)
		if role.Reserve.FreeBytes > math.MaxUint64-fs.RequiredBytes || role.Reserve.FreeInodes > math.MaxUint64-fs.RequiredInodes {
			return report, errors.New("combined capacity reserve overflows")
		}
		fs.RequiredBytes += role.Reserve.FreeBytes
		fs.RequiredInodes += role.Reserve.FreeInodes
	}
	for _, fs := range report.Filesystems {
		if fs.FreeBytes < fs.RequiredBytes || fs.FreeInodes < fs.RequiredInodes {
			err = errors.Join(err, fmt.Errorf("filesystem %s has insufficient free capacity: bytes %d/%d, inodes %d/%d", fs.Device, fs.FreeBytes, fs.RequiredBytes, fs.FreeInodes, fs.RequiredInodes))
		}
	}
	if err != nil {
		return report, err
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	report.Passed = true
	return report, nil
}

func (k *KamalEffects) checkCapacity(ctx context.Context) error {
	// An unfinished old journal binds the old profile bytes. Recovery alone
	// keeps that explicit legacy contract; new operations cannot omit policy.
	if k.recovering && k.Profile.Capacity == nil {
		return nil
	}
	_, err := k.Capacity(ctx)
	return err
}

func (k *KamalEffects) checkBootInputs(ctx context.Context) error {
	if err := k.checkCapacity(ctx); err != nil {
		return err
	}
	// Pressure or external pruning can change after preflight. Verify every
	// rollback input before boot, not just the selected image after mutation.
	for _, reference := range []string{k.Request.Predecessor.Image, k.Request.Candidate.Image, k.Profile.ProxyImage} {
		if _, err := k.image(ctx, reference); err != nil {
			return err
		}
	}
	return nil
}
