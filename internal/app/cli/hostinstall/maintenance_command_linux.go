//go:build linux

package hostinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/postgresbaseline"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

func runNative(ctx context.Context, action string, r NativeRequest, journalPath, credential, digest string, stdin io.Reader, stdout io.Writer) error {
	if action == "migrate" || action == "rehearse" {
		return runNativeMigration(ctx, r, journalPath, credential, digest, action == "rehearse")
	}
	id, err := r.Identity()
	if err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("host maintenance requires root")
	}
	host, err := os.Hostname()
	if err != nil || host != r.Profile.Hostname {
		return errors.New("host maintenance requires the verified configured host")
	}
	binary := buildinfo.Current()
	if binary.Dirty || binary.Revision != r.CandidateRevision {
		return errors.New("controller must be the qualified candidate")
	}
	installLock, err := instancelock.AcquireNamed(r.Profile.Root, installLockName)
	if err != nil {
		return err
	}
	defer installLock.Release()
	// Persist the request before the journal can fence the host. A crash after
	// preparing the journal must never leave recovery without its request file.
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	operation := filepath.Join(r.Profile.StateRoot, "upgrade-operations", strings.TrimPrefix(id.ArtifactAdmissionDigest, "sha256:"))
	if err = securefs.WritePrivateFileAtomic(filepath.Join(operation, "request.json"), raw); err != nil {
		return err
	}
	journal, err := OpenJournal(r.Profile.Root, id)
	if err != nil {
		return err
	}
	defer journal.Close()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err = installMaintenanceController(r.Profile.Root, executable); err != nil {
		return err
	}
	defer func() {
		state, readErr := journal.Load(context.Background())
		if readErr == nil && (state.Phase == Succeeded || state.Phase == Recovered) {
			// Failure leaves the newer guarded controller selected, a safe state.
			_ = maintenanceControllerLink(r.Profile.Root, "current/leapviewctl")
		}
	}()
	effects, err := NewNativeEffects(r, stdin, stdout)
	if err != nil {
		return err
	}
	defer effects.Close()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()
	coordinator := Coordinator{Journal: journal, Effects: effects}
	if action == "recover" {
		state, err := journal.Load(ctx)
		if err != nil {
			return err
		}
		if state.Phase == Committed || state.Phase == Succeeded {
			if err = coordinator.Run(ctx, id); err != nil {
				return err
			}
			_, err = fmt.Fprintln(stdout, "DEPLOYMENT_COMMITTED")
			return err
		}
		if err = coordinator.Recover(ctx, id); err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, "PREDECESSOR_RECOVERED")
		return err
	}
	if action != "apply" {
		return errors.New("unsupported native upgrade action")
	}
	if err = coordinator.Run(ctx, id); err != nil {
		// A fresh timeout allows recovery after runner cancellation; a committed
		// operation is NEVER restored, even if public verification subsequently fails.
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		state, loadErr := journal.Load(recoveryCtx)
		if loadErr != nil {
			return errors.Join(err, loadErr)
		}
		if state.Phase == Committed || state.Phase == Succeeded {
			return err
		}
		recoveryErr := coordinator.Recover(recoveryCtx, id)
		if recoveryErr == nil {
			_, _ = fmt.Fprintln(effects.log, "predecessor recovery completed")
		}
		return errors.Join(err, recoveryErr)
	}
	_, err = fmt.Fprintln(stdout, "DEPLOYMENT_COMMITTED")
	return err
}
func validateMigrationIntent(r NativeRequest, state State, digest string, binary buildinfo.Identity) error {
	id, err := r.Identity()
	if err != nil {
		return err
	}
	if binary.Dirty || binary.Revision != r.CandidateRevision {
		return errors.New("migration binary is not the qualified candidate")
	}
	if state.Identity != id || state.Phase != Migrating || !state.RestoreRequired || !digestPattern.MatchString(digest) || state.RecoveryDigest != digest {
		return errors.New("migration requires the exact durable stopped-target and recovery intent")
	}
	return state.validate()
}
func runNativeMigration(ctx context.Context, r NativeRequest, journalPath, credential, digest string, rehearsal bool) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	revalidate := func(context.Context) error {
		state, err := readJournalFile(journalPath)
		if err != nil {
			return err
		}
		if rehearsal {
			id, err := r.Identity()
			if err != nil {
				return err
			}
			binary := buildinfo.Current()
			if binary.Dirty || binary.Revision != r.CandidateRevision || state.Identity != id || state.Phase != Capturing || !digestPattern.MatchString(digest) {
				return errors.New("rehearsal requires the exact capturing operation")
			}
			return nil
		}
		return validateMigrationIntent(r, state, digest, buildinfo.Current())
	}
	if err := revalidate(ctx); err != nil {
		return err
	}
	raw, err := securefs.ReadPrivateFile(credential)
	if err != nil {
		return err
	}
	config, err := pgxpool.ParseConfig(string(raw))
	if err != nil {
		return errors.New("invalid private migrator URL")
	}
	c := config.ConnConfig
	if c.Host != r.Profile.Postgres || c.Database != "leapview_control" || c.User != "leapview_control_migrator" || c.TLSConfig == nil || c.TLSConfig.InsecureSkipVerify || len(c.Fallbacks) > 0 {
		return errors.New("migration requires the control migrator role with mandatory TLS")
	}
	config.MaxConns = 3
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return errors.New("open host migration pool")
	}
	defer pool.Close()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	return postgresbaseline.ApplyUpgrade(ctx, pool, db, int64(r.Plan.CurrentSchema), revalidate)
}

func checkMaintenancePlan(ctx context.Context, r NativeRequest) error {
	if os.Geteuid() != 0 {
		return errors.New("operator maintenance requires root")
	}
	host, err := os.Hostname()
	if err != nil || host != r.Profile.Hostname {
		return errors.New("installation host mismatch")
	}
	binary := buildinfo.Current()
	if binary.Dirty || binary.Revision != r.CandidateRevision {
		return errors.New("controller must match qualified candidate")
	}
	if err := requireLocalDocker(ctx); err != nil {
		return err
	}
	return validateImageSupplies(ctx, r)
}
