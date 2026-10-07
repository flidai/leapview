//go:build linux

package managedmaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCapacityMeasuresExistingLinuxFilesystem(t *testing.T) {
	root := t.TempDir()
	m, err := measureFilesystemCapacity(root)
	if err != nil || m.Device == "" || m.FreeBytes == 0 || m.FreeInodes == 0 {
		t.Fatalf("actual filesystem capacity: %+v, %v", m, err)
	}
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	n, err := measureFilesystemCapacity(child)
	if err != nil || n.Device != m.Device {
		t.Fatalf("shared device: %+v, %v", n, err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, filepath.Join(root, "missing")} {
		if _, err := measureFilesystemCapacity(path); err == nil {
			t.Fatalf("accepted unmeasurable path %s", path)
		}
	}
}

func TestCapacityRejectsUnknownAndOverflowingStatfs(t *testing.T) {
	for _, variant := range []string{"valid", "zero-inodes", "bad-inodes", "bad-blocks", "bad-available", "bad-blocksize", "overflow", "exhausted", "inode-sentinel", "readonly"} {
		t.Run(variant, func(t *testing.T) {
			fs := unix.Statfs_t{Blocks: 100, Bfree: 90, Bavail: 80, Bsize: 4096, Frsize: 4096, Files: 50, Ffree: 30}
			switch variant {
			case "zero-inodes":
				fs.Files = 0
			case "bad-inodes":
				fs.Ffree = 51
			case "bad-blocks":
				fs.Bfree = 101
			case "bad-available":
				fs.Bavail = 91
			case "bad-blocksize":
				fs.Frsize = -1
			case "overflow":
				fs.Blocks = math.MaxUint64
			case "exhausted":
				fs.Bavail, fs.Ffree = 0, 0
			case "inode-sentinel":
				fs.Files, fs.Ffree = math.MaxUint64, math.MaxUint64
			case "readonly":
				fs.Flags |= unix.ST_RDONLY
			}
			m, err := capacityFromStat(1, fs)
			if (err == nil) != (variant == "valid" || variant == "exhausted") {
				t.Fatalf("measurement: %+v, %v", m, err)
			}
		})
	}
}

func TestCapacityPolicyPreservesLegacyProfileFingerprint(t *testing.T) {
	k, _, _ := adapterFixture(t)
	k.Profile.Capacity = nil
	// This is the exact pre-capacity JSON layout; omitempty must preserve it.
	legacy := struct {
		Version       int    `json:"version"`
		Target        string `json:"target"`
		Root          string `json:"root"`
		StateRoot     string `json:"stateRoot"`
		Home          string `json:"home"`
		Socket        string `json:"socket"`
		Service       string `json:"service"`
		Hostname      string `json:"hostname"`
		ProxyImage    string `json:"proxyImage"`
		AdmissionRoot string `json:"admissionRoot"`
	}{k.Profile.Version, k.Profile.Target, k.Profile.Root, k.Profile.StateRoot, k.Profile.Home, k.Profile.Socket, k.Profile.Service, k.Profile.Hostname, k.Profile.ProxyImage, k.Profile.AdmissionRoot}
	oldBytes, _ := json.Marshal(legacy)
	currentBytes, _ := json.Marshal(k.Profile)
	if string(oldBytes) != string(currentBytes) {
		t.Fatal("legacy canonical profile bytes changed")
	}
	data := map[string]json.RawMessage{"profile": oldBytes}
	for _, name := range profileFiles {
		path := filepath.Join(k.Profile.Root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("legacy"), 0600); err != nil {
			t.Fatal(err)
		}
		data[name], _ = json.Marshal([]byte("legacy"))
	}
	encoded, _ := json.Marshal(data)
	digest, err := ProfileDigest(k.Profile)
	if err != nil || digest != byteIdentity(encoded) {
		t.Fatalf("legacy fingerprint changed: %s %v", digest, err)
	}
	if err := k.checkCapacity(t.Context()); err == nil {
		t.Fatal("new run admitted missing policy")
	}
	k.recovering = true
	if err := k.checkCapacity(t.Context()); err != nil {
		t.Fatalf("legacy recovery cannot retain enrolled semantics: %v", err)
	}
	k.Profile.Capacity = &CapacityPolicy{DockerRootDir: "/var/lib/docker", Home: CapacityReserve{1, 1}, StateRoot: CapacityReserve{1, 1}, Docker: CapacityReserve{1, 1}}
	updated, err := ProfileDigest(k.Profile)
	if err != nil || updated == digest {
		t.Fatalf("new policy not fingerprint-bound: %v", err)
	}
}

func TestBootRechecksCapacityAndAllRollbackImagesBeforeMutation(t *testing.T) {
	for _, variant := range []string{"pressure", "missing-predecessor", "missing-candidate", "missing-proxy"} {
		t.Run(variant, func(t *testing.T) {
			k, _, proxy := adapterFixture(t)
			d := &inventoryDouble{t: t, items: []containerInfo{proxy}, dockerRoot: k.Profile.Capacity.DockerRootDir}
			k.run = func(_ context.Context, bin string, args, _ []string, _ string) ([]byte, error) {
				if bin != "docker" {
					t.Fatalf("boot mutated before failed guard: %s %v", bin, args)
				}
				ref := args[len(args)-1]
				if (variant == "missing-predecessor" && ref == k.Request.Predecessor.Image) || (variant == "missing-candidate" && ref == k.Request.Candidate.Image) || (variant == "missing-proxy" && ref == k.Profile.ProxyImage) {
					return nil, errors.New("retained image absent")
				}
				return d.docker(args)
			}
			if _, err := k.Capacity(t.Context()); err != nil {
				t.Fatal(err)
			}
			if variant == "pressure" {
				k.capacityProbe = func(string) (filesystemCapacity, error) { return filesystemCapacity{Device: "1"}, nil }
			}
			if err := k.StartPrepared(t.Context(), k.Request.Candidate); err == nil {
				t.Fatal("boot accepted missing prerequisite")
			}
			for _, command := range d.commands {
				if strings.Contains(command, "stop") || strings.Contains(command, "pull") {
					t.Fatal("guard mutated runtime")
				}
			}
		})
	}
}
