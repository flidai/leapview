package managedmaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
)

func capacityFixture() *KamalEffects {
	return &KamalEffects{Profile: HostProfile{Home: "/home/app", StateRoot: "/journal", Capacity: &CapacityPolicy{
		DockerRootDir: "/docker", Home: CapacityReserve{10, 2}, StateRoot: CapacityReserve{20, 3}, Docker: CapacityReserve{30, 4},
	}}, run: func(_ context.Context, bin string, args, _ []string, _ string) ([]byte, error) {
		if bin != "docker" || !reflect.DeepEqual(args, []string{"--host", "unix:///var/run/docker.sock", "info", "--format", "{{json .DockerRootDir}}"}) {
			return nil, errors.New("unexpected or mutating capacity command")
		}
		return []byte(`"/docker"`), nil
	}, capacityProbe: func(string) (filesystemCapacity, error) {
		return filesystemCapacity{Device: "1", FreeBytes: 60, FreeInodes: 9}, nil
	}}
}

func TestCapacityAddsRoleReservesOncePerSharedFilesystem(t *testing.T) {
	k := capacityFixture()
	report, err := k.Capacity(t.Context())
	if err != nil || !report.Passed || len(report.Filesystems) != 1 {
		t.Fatalf("shared device report: %+v, %v", report, err)
	}
	fs := report.Filesystems[0]
	if fs.RequiredBytes != 60 || fs.RequiredInodes != 9 || len(fs.Paths) != 3 {
		t.Fatalf("role reserves were not combined: %+v", fs)
	}
	k.capacityProbe = func(path string) (filesystemCapacity, error) {
		return filesystemCapacity{Device: path, FreeBytes: 30, FreeInodes: 4}, nil
	}
	report, err = k.Capacity(t.Context())
	if err != nil || len(report.Filesystems) != 3 {
		t.Fatalf("independent devices report: %+v, %v", report, err)
	}
}

func TestCapacityFailsClosedWithoutMutation(t *testing.T) {
	for _, variant := range []string{"bytes", "inodes", "unavailable", "unknown-device", "missing-policy", "zero-reserve", "sum-overflow", "docker-drift", "invalid-docker-root", "canceled"} {
		t.Run(variant, func(t *testing.T) {
			k := capacityFixture()
			ctx := t.Context()
			switch variant {
			case "bytes", "inodes", "unavailable", "unknown-device":
				k.capacityProbe = func(path string) (filesystemCapacity, error) {
					m := filesystemCapacity{Device: "1", FreeBytes: 60, FreeInodes: 9}
					if path == k.Profile.StateRoot {
						switch variant {
						case "bytes":
							m.FreeBytes = 59
						case "inodes":
							m.FreeInodes = 8
						case "unavailable":
							return m, errors.New("statfs unavailable")
						case "unknown-device":
							m.Device = ""
						}
					}
					return m, nil
				}
			case "missing-policy":
				k.Profile.Capacity = nil
			case "zero-reserve":
				k.Profile.Capacity.Home.FreeInodes = 0
			case "sum-overflow":
				k.Profile.Capacity.Home.FreeBytes = math.MaxUint64
			case "docker-drift":
				k.Profile.Capacity.DockerRootDir = "/other"
			case "invalid-docker-root":
				k.run = func(context.Context, string, []string, []string, string) ([]byte, error) {
					return []byte(`"relative"`), nil
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			report, err := k.Capacity(ctx)
			if err == nil || report.Passed || report.Error == "" {
				t.Fatalf("invalid capacity admitted: %+v, %v", report, err)
			}
			if _, err := json.Marshal(report); err != nil {
				t.Fatalf("diagnostic report not serializable: %v", err)
			}
		})
	}
}
