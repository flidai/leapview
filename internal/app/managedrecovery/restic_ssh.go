package managedrecovery

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

// ResticSSHConfig is exact off-host repository transport trust. It never
// accepts an arbitrary shell command or ambient SSH authentication/config.
type ResticSSHConfig struct {
	SSH            string
	Address        string
	Port           int
	User           string
	IdentityFile   string
	KnownHostsFile string
}

func managedResticSSHCommand(repository string, transport *ResticSSHConfig) (string, error) {
	if transport == nil {
		if !filepath.IsAbs(repository) || filepath.Clean(repository) != repository || repository == "/" {
			return "", errors.New("canonical local or explicitly enrolled SFTP repository required")
		}
		return "", nil
	}
	if !pinnedProgram(transport.SSH) || transport.Port < 1 || transport.Port > 65535 || !regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,63}$`).MatchString(transport.User) {
		return "", errors.New("exact pinned SFTP transport identity required")
	}
	address, err := netip.ParseAddr(transport.Address)
	if err != nil || address.String() != transport.Address || address.IsUnspecified() || address.IsMulticast() {
		return "", errors.New("canonical enrolled SFTP address required")
	}
	for _, path := range []string{transport.IdentityFile, transport.KnownHostsFile} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return "", errors.New("canonical private SFTP trust files required")
		}
		contents, err := securefs.ReadPrivateFile(path)
		if err != nil || len(contents) == 0 {
			return "", errors.New("private enrolled SFTP trust unavailable")
		}
	}
	parsed, err := url.Parse(repository)
	if err != nil || parsed.Scheme != "sftp" || parsed.User == nil || parsed.User.Username() != transport.User || parsed.Host != net.JoinHostPort(transport.Address, strconv.Itoa(transport.Port)) || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || !filepath.IsAbs(parsed.Path) || filepath.Clean(parsed.Path) != parsed.Path || parsed.Path == "/" {
		return "", errors.New("SFTP repository differs from exact enrolled transport")
	}
	if _, password := parsed.User.Password(); password {
		return "", errors.New("SFTP repository rejects password arguments")
	}
	exact := (&url.URL{Scheme: "sftp", User: url.User(transport.User), Host: net.JoinHostPort(transport.Address, strconv.Itoa(transport.Port)), Path: parsed.Path}).String()
	if exact != repository {
		return "", errors.New("canonical exact SFTP repository required")
	}
	args := []string{transport.SSH, "-F", "/dev/null", "-T", "-p", strconv.Itoa(transport.Port), "-i", transport.IdentityFile, "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "StrictHostKeyChecking=yes", "-o", "GlobalKnownHostsFile=/dev/null", "-o", "UserKnownHostsFile=" + transport.KnownHostsFile, "-o", "ConnectTimeout=10", "-o", "LogLevel=ERROR", "-s", transport.User + "@" + transport.Address, "sftp"}
	for index, value := range args {
		args[index] = "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	}
	return strings.Join(args, " "), nil
}
