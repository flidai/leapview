//go:build linux

package demoupgrade

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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

func runNative(ctx context.Context, action string, r NativeRequest, journalPath, credential, digest string, stdin io.Reader, stdout io.Writer) error {
	if action == "migrate" {
		return runNativeMigration(ctx, r, journalPath, credential, digest)
	}
	id, err := r.Identity()
	if err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("demo upgrade requires root")
	}
	host, err := os.Hostname()
	if err != nil || host != "app-leapview-demo-02" {
		return errors.New("demo upgrade requires the verified demo-02 host")
	}
	binary := buildinfo.Current()
	if binary.Dirty || binary.Revision != r.CandidateRevision {
		return errors.New("controller must be the qualified candidate")
	}
	// Persist the request before the journal can fence the host. A crash after
	// preparing the journal must never leave recovery without its request file.
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	operation := filepath.Join(nativeProvider, "upgrade-operations", strings.TrimPrefix(id.ArtifactAdmissionDigest, "sha256:"))
	if err = securefs.WritePrivateFileAtomic(filepath.Join(operation, "request.json"), raw); err != nil {
		return err
	}
	journal, err := OpenJournal(nativeProvider, id)
	if err != nil {
		return err
	}
	defer journal.Close()
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
func runNativeMigration(ctx context.Context, r NativeRequest, journalPath, credential, digest string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	revalidate := func(context.Context) error {
		state, err := readJournalFile(journalPath)
		if err != nil {
			return err
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
	if c.Host != nativePG || c.Database != "leapview_control" || c.User != "leapview_control_migrator" || c.TLSConfig == nil || len(c.Fallbacks) > 0 {
		return errors.New("migration requires the demo control migrator role with mandatory TLS")
	}
	config.MaxConns = 3
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return errors.New("open demo migration pool")
	}
	defer pool.Close()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	return postgresbaseline.ApplyDemoUpgrade(ctx, pool, db, revalidate)
}
