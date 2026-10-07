package managedmaintenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/platform/ociref"
)

// HostProfile is an operator-owned, private, host-local installation binding.
// The authority directory is populated by the authenticated release producer;
// this controller can consume admission records, never issue them.
type HostProfile struct {
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
}

func (p HostProfile) Validate() error {
	if p.Version != 1 || !targetPattern.MatchString(p.Target) || !targetPattern.MatchString(p.Service) || !targetPattern.MatchString(p.Hostname) {
		return errors.New("invalid managed host profile")
	}
	if _, err := ociref.ParseImmutable(p.ProxyImage); err != nil {
		return fmt.Errorf("proxy must be immutable: %w", err)
	}
	for _, v := range []string{p.Root, p.StateRoot, p.Home, p.Socket, p.AdmissionRoot} {
		if !filepath.IsAbs(v) || filepath.Clean(v) != v || v == "/" || strings.ContainsAny(v, "\r\n,:") {
			return errors.New("canonical absolute managed paths required")
		}
	}
	if !strings.HasPrefix(p.Socket, p.Home+"/") || strings.HasPrefix(p.StateRoot, p.Home+"/") || p.StateRoot == p.Home || strings.HasPrefix(p.Home, p.StateRoot+"/") {
		return errors.New("private socket must be in home; journal must be outside application state")
	}
	return nil
}

var profileFiles = []string{"deploy.yml", "Gemfile", "Gemfile.lock", "probe_host.rb", "maintenance_adapter.rb", "maintenance_config.rb", "ssh_config", ".kamal/secrets", "environment.json"}

// ProfileDigest binds every executable/configuration input, including private
// environment bytes. Paths are hashed with contents to avoid concatenation
// ambiguity. The generated admission gate is deliberately not an input.
func ProfileDigest(p HostProfile) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	data := map[string]json.RawMessage{}
	raw, _ := json.Marshal(p)
	data["profile"] = raw
	for _, name := range profileFiles {
		raw, err := readOperatorFile(filepath.Join(p.Root, name))
		if err != nil {
			return "", err
		}
		encoded, _ := json.Marshal(raw)
		data[name] = encoded
	}
	raw, _ = json.Marshal(data)
	return byteIdentity(raw), nil
}
func readOperatorFile(path string) ([]byte, error) {
	if err := trustedAncestors(filepath.Dir(path)); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 || info.Mode().Perm()&0022 != 0 || !operatorOwned(info) {
		return nil, errors.New("managed input must be a bounded, non-writable regular operator file")
	}
	return os.ReadFile(path)
}

func trustedAncestors(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("absolute canonical operator path required")
	}
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !trustedDirectoryOwner(info) || (info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return errors.New("managed input has an untrusted parent directory")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}
func privateOperatorRoot(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return trustedAncestors(filepath.Dir(path))
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || !operatorOwned(info) || info.Mode().Perm()&0077 != 0 {
		return errors.New("managed operator root must be private and operator-owned")
	}
	return trustedAncestors(path)
}
func byteIdentity(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func environmentIdentity(env []string) string {
	values := append([]string(nil), env...)
	filtered := values[:0]
	for _, v := range values {
		name, _, _ := strings.Cut(v, "=")
		if name != "KAMAL_VERSION" && name != "KAMAL_HOST" && name != "KAMAL_CONTAINER_NAME" {
			filtered = append(filtered, v)
		}
	}
	sort.Strings(filtered)
	raw, _ := json.Marshal(filtered)
	return byteIdentity(raw)
}
func mergeEnvironment(base []string, extra map[string]string) []string {
	values := map[string]string{}
	for _, v := range base {
		k, v, ok := strings.Cut(v, "=")
		if ok {
			values[k] = v
		}
	}
	for k, v := range extra {
		values[k] = v
	}
	out := make([]string, 0, len(values))
	for k, v := range values {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
func writeGate(p HostProfile, open bool) error {
	raw, _ := json.Marshal(struct {
		Publish bool `json:"publish"`
	}{open})
	return securefs.WritePrivateFileAtomic(filepath.Join(p.StateRoot, "ingress.json"), raw)
}

type portBinding struct{ HostIP, HostPort string }
type mountInfo struct {
	Type, Source, Destination string
	RW                        bool
}
type containerInfo struct {
	ID, Name, Image string
	Config          struct {
		Image  string
		Env    []string
		Labels map[string]string
	}
	State struct {
		Running, OOMKilled bool
		ExitCode           int
		Status             string
	}
	HostConfig struct {
		NetworkMode   string
		PortBindings  map[string][]portBinding
		RestartPolicy struct{ Name string }
	}
	Mounts []mountInfo
}

func validateInventory(p HostProfile, r Request, items []containerInfo, private bool) error {
	owners, proxies := 0, 0
	for _, c := range items {
		if !c.State.Running {
			continue
		}
		name := strings.TrimPrefix(c.Name, "/")
		if c.HostConfig.NetworkMode == "host" {
			return errors.New("host-network container bypasses maintenance ingress")
		}
		if c.HostConfig.NetworkMode != "kamal" {
			return errors.New("managed containers must use the enrolled Kamal network")
		}
		if name == "kamal-proxy" {
			proxies++
			if c.Config.Image != p.ProxyImage {
				return errors.New("unexpected proxy image")
			}
			if private {
				for _, bindings := range c.HostConfig.PortBindings {
					if len(bindings) > 0 {
						return errors.New("private proxy still publishes a host port")
					}
				}
			}
			continue
		}
		if c.Config.Labels["service"] != p.Service || c.Config.Labels["role"] != "web" {
			// A dedicated managed app host has no other container writer/ingress owner.
			return errors.New("unrelated running container prevents exclusive managed handoff")
		}
		owners++
		release := r.Predecessor
		if c.Config.Image == r.Candidate.Image {
			release = r.Candidate
		}
		if c.Config.Image != release.Image || name != p.Service+"-web-"+release.Revision {
			return errors.New("unrecognized managed process identity")
		}
		for _, bindings := range c.HostConfig.PortBindings {
			if len(bindings) > 0 {
				return errors.New("application publishes an alternate ingress")
			}
		}
		env := map[string]string{}
		for _, v := range c.Config.Env {
			k, v, ok := strings.Cut(v, "=")
			if ok {
				env[k] = v
			}
		}
		if env["LEAPVIEW_HOME"] != p.Home || env["LEAPVIEW_MAINTENANCE_SOCKET"] != p.Socket {
			return errors.New("application lacks managed home/admission binding")
		}
		bound := false
		for _, m := range c.Mounts {
			if strings.HasPrefix(m.Destination, p.Home+"/") {
				return errors.New("nested mount shadows managed application state")
			}
			if strings.HasPrefix(p.Home, m.Destination+"/") || p.Home == m.Destination {
				if m.Type != "bind" || !m.RW || m.Source != m.Destination {
					return errors.New("managed home must use the exact host-local bind")
				}
				bound = true
			}
		}
		if !bound {
			return errors.New("managed home mount is absent")
		}
	}
	if owners > 1 || proxies > 1 {
		return errors.New("expected at most one dedicated proxy and application owner")
	}
	return nil
}
