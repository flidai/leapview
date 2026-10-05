package hostinstall

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/platform/ociref"
)

// MaintenanceProfile is operator-owned configuration, bound into the private
// request digest. It is not a release-authority target registration.
type MaintenanceProfile struct {
	ControlMigratorURLFile string            `json:"controlMigratorUrlFile,omitempty"`
	Version                int               `json:"version"`
	ID                     string            `json:"id"`
	Hostname               string            `json:"hostname"`
	Root                   string            `json:"root"`
	StateRoot              string            `json:"stateRoot"`
	Project                string            `json:"project"`
	AppService             string            `json:"appService"`
	ProxyService           string            `json:"proxyService"`
	Postgres               string            `json:"postgres"`
	PostgresImage          string            `json:"postgresImage"`
	Network                string            `json:"network"`
	Origin                 string            `json:"origin"`
	HTTPBinding            string            `json:"httpBinding"`
	HTTPSBinding           string            `json:"httpsBinding"`
	RehearsalBinding       string            `json:"rehearsalBinding"`
	Volumes                map[string]string `json:"volumes"`
}

var dockerSelector = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func (p MaintenanceProfile) Validate() error {
	if p.Version != 1 {
		return errors.New("unsupported installation profile")
	}
	for _, v := range []string{p.ID, p.Hostname, p.Project, p.AppService, p.ProxyService, p.Postgres, p.Network} {
		if !dockerSelector.MatchString(v) {
			return errors.New("invalid installation selector")
		}
	}
	for _, path := range []string{p.Root, p.StateRoot} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, ",\r\n") {
			return errors.New("absolute canonical installation directories required")
		}
	}
	if p.ControlMigratorURLFile != "" && (!filepath.IsAbs(p.ControlMigratorURLFile) || filepath.Clean(p.ControlMigratorURLFile) != p.ControlMigratorURLFile || strings.ContainsAny(p.ControlMigratorURLFile, ",\r\n")) {
		return errors.New("private migrator file must have an absolute canonical path")
	}
	if p.Root == p.StateRoot || p.AppService == p.ProxyService {
		return errors.New("recovery storage must be separate from installation")
	}
	u, err := url.Parse(p.Origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return errors.New("canonical HTTPS origin required")
	}
	if _, err = ociref.ParseImmutable(p.PostgresImage); err != nil {
		return err
	}
	// Physical copies support the PostgreSQL 18 container layout only. The exact
	// image is provided by the installation, never selected by the upgrader.
	if !regexp.MustCompile(`(^|/)postgres:18([.-][a-zA-Z0-9_.-]+)?@sha256:`).MatchString(p.PostgresImage) {
		return errors.New("only PostgreSQL 18 physical recovery is supported")
	}
	seen := map[string]bool{}
	for _, binding := range []string{p.HTTPBinding, p.HTTPSBinding, p.RehearsalBinding} {
		host, port, err := net.SplitHostPort(binding)
		if err != nil || host != "127.0.0.1" || port == "0" || seen[binding] {
			return errors.New("distinct fixed loopback maintenance bindings required")
		}
		number, parseErr := strconv.Atoi(port)
		if parseErr != nil || number < 1024 || number > 65535 || strconv.Itoa(number) != port {
			return errors.New("numeric unprivileged maintenance port required")
		}
		seen[binding] = true
	}
	if len(p.Volumes) != 4 {
		return errors.New("exact PostgreSQL, home and proxy state inventory required")
	}
	seenVolumes := map[string]bool{}
	for _, domain := range []string{"postgres", "home", "caddy-data", "caddy-config"} {
		if !dockerSelector.MatchString(p.Volumes[domain]) || seenVolumes[p.Volumes[domain]] {
			return fmt.Errorf("invalid %s volume", domain)
		}
		seenVolumes[p.Volumes[domain]] = true
	}
	return nil
}
