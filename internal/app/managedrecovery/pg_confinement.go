package managedrecovery

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PGStagingCluster grants a confined launcher for one private recovered data
// directory. Production readback starts foreground postgres through this
// capability; it must not use an ambient pg_ctl/postgres process. Kernel mount
// confinement also contains tablespaces introduced by WAL after the backup.
type PGStagingCluster struct {
	directory  string
	bubblewrap string
}

func (cluster *PGStagingCluster) Directory() string { return cluster.directory }

func (cluster *PGStagingCluster) PostgresCommand(ctx context.Context, program string, args ...string) (*exec.Cmd, error) {
	if cluster == nil || !pinnedProgram(program) || filepath.Base(program) != "postgres" {
		return nil, errors.New("pinned foreground PostgreSQL required")
	}
	args = append([]string{"-D", cluster.directory}, args...)
	command := exec.CommandContext(ctx, cluster.bubblewrap, append(cluster.arguments(), append([]string{program}, args...)...)...)
	command.Env = []string{"PATH=/nonexistent", "LANG=C", "TZ=UTC"}
	if err := configureRestoreProcess(command); err != nil {
		return nil, err
	}
	return command, nil
}

func (cluster *PGStagingCluster) arguments() []string {
	// Reintroduce the parent read-only after the private /tmp mount because
	// qualification and private repository/config inputs may live below /tmp.
	// Only this staging directory is writable, never its parent or the original.
	return []string{"--ro-bind", "/", "/", "--tmpfs", "/tmp", "--ro-bind", filepath.Dir(cluster.directory), filepath.Dir(cluster.directory), "--bind", cluster.directory, cluster.directory, "--dev", "/dev", "--ro-bind", "/proc", "/proc", "--die-with-parent", "--chdir", cluster.directory, "--"}
}

func pinnedProgram(program string) bool {
	return filepath.IsAbs(program) && filepath.Clean(program) == program && strings.HasPrefix(program, "/nix/store/") && !strings.ContainsAny(program, "\r\n\x00")
}

func validateDefaultPGLayout(directory string) error {
	entries, err := os.ReadDir(filepath.Join(directory, "pg_tblspc"))
	if err != nil || len(entries) != 0 {
		return errors.New("managed PostgreSQL recovery supports only the default tablespace layout")
	}
	return filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("managed PostgreSQL recovery rejects retained filesystem links")
		}
		return nil
	})
}

func validatePGRestoreConfig(value []byte) error {
	for _, line := range strings.Split(string(value), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			return errors.New("invalid retained pgBackRest configuration")
		}
		key = strings.TrimSpace(key)
		if key == "link-all" || key == "link-map" || key == "tablespace-map" || key == "tablespace-map-all" || key == "recovery-option" || strings.HasPrefix(key, "config") {
			return errors.New("retained pgBackRest configuration cannot override managed restore confinement")
		}
	}
	return nil
}
