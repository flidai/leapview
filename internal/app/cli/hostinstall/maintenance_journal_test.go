package hostinstall

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	instancelock "github.com/flidai/leapview/internal/platform/locking"
)

func TestJournalSurvivesRestartAndRejectsConcurrentOwner(t *testing.T) {
	root := t.TempDir()
	j, err := OpenJournal(root, identity())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(root, identity()); !errors.Is(err, instancelock.ErrAlreadyInUse) {
		t.Fatal(err)
	}
	s, _ := j.Load(context.Background())
	s.Phase = Quiescing
	if err := j.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenJournal(root, identity())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	s, err = j.Load(context.Background())
	if err != nil || s.Phase != Quiescing {
		t.Fatalf("%+v %v", s, err)
	}
	c := Coordinator{Journal: j, Effects: &effects{}}
	if !errors.Is(c.Run(context.Background(), identity()), ErrRecoveryRequired) {
		t.Fatal("interrupted operation replayed")
	}
}
func TestJournalRejectsCorruptionAndIdentityReplacement(t *testing.T) {
	root := t.TempDir()
	j, err := OpenJournal(root, identity())
	if err != nil {
		t.Fatal(err)
	}
	j.Close()
	other := identity()
	other.Target = "different"
	if _, err = OpenJournal(root, other); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, JournalName), []byte(`{"version":1,"state":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenJournal(root, identity()); err == nil {
		t.Fatal("accepted corrupted journal")
	}
}
func TestJournalRejectsSkippedPhasesAndRecoveryReplacement(t *testing.T) {
	j, err := OpenJournal(t.TempDir(), identity())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	s, _ := j.Load(context.Background())
	s.Phase = Committed
	s.RecoveryDigest = "sha256:" + hex64('d')
	s.RestoreRequired = true
	if j.Save(context.Background(), s) == nil {
		t.Fatal("skipped phases")
	}
	c := Coordinator{Journal: j, Effects: &effects{}}
	if err = c.Run(context.Background(), identity()); err != nil {
		t.Fatal(err)
	}
	s, _ = j.Load(context.Background())
	s.RecoveryDigest = "sha256:" + hex64('e')
	if j.Save(context.Background(), s) == nil {
		t.Fatal("changed recovery identity")
	}
}
func TestJournalRefusesSymlinkAndBroadPermissions(t *testing.T) {
	for _, kind := range []string{"symlink", "permissions"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			j, err := OpenJournal(root, identity())
			if err != nil {
				t.Fatal(err)
			}
			j.Close()
			path := filepath.Join(root, JournalName)
			if kind == "symlink" {
				os.Rename(path, path+".real")
				os.Symlink(path+".real", path)
			} else {
				os.Chmod(path, 0644)
			}
			if _, err := OpenJournal(root, identity()); err == nil {
				t.Fatal("accepted unsafe journal")
			}
		})
	}
}

func TestNextUpgradeArchivesTerminalEvidence(t *testing.T) {
	root := t.TempDir()
	j, err := OpenJournal(root, identity())
	if err != nil {
		t.Fatal(err)
	}
	c := Coordinator{Journal: j, Effects: &effects{}}
	if err = c.Run(context.Background(), identity()); err != nil {
		t.Fatal(err)
	}
	j.Close()
	next := identity()
	next.Predecessor = next.Candidate
	next.Candidate = "ghcr.io/flidai/leapview@sha256:" + hex64('e')
	j, err = OpenJournal(root, next)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	s, err := j.Load(context.Background())
	if err != nil || s.Phase != Prepared || s.Identity != next {
		t.Fatalf("%+v %v", s, err)
	}
	history, err := os.ReadDir(filepath.Join(root, "upgrade-history"))
	if err != nil || len(history) != 1 {
		t.Fatalf("history not retained: %v %v", history, err)
	}
}
func TestPreparedOperationCanBeAbortedWithoutRuntimeEffects(t *testing.T) {
	j, err := OpenJournal(t.TempDir(), identity())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	e := &effects{}
	c := Coordinator{Journal: j, Effects: e}
	if err = c.Recover(context.Background(), identity()); err != nil {
		t.Fatal(err)
	}
	if len(e.calls) != 0 {
		t.Fatal(e.calls)
	}
	s, _ := j.Load(context.Background())
	if s.Phase != Recovered {
		t.Fatal(s)
	}
}

func TestImageDeploymentPythonLockInteroperates(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	root := t.TempDir()
	j, err := OpenJournal(root, identity())
	if err != nil {
		t.Fatal(err)
	}
	probe := func() error {
		return exec.Command("python3", "-c", `import fcntl,sys
with open(sys.argv[1], 'a+') as f:
    fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
`, filepath.Join(root, LockName)).Run()
	}
	if probe() == nil {
		t.Fatal("Python image deployment entered Go upgrade lock")
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	if err = probe(); err != nil {
		t.Fatal("lock remained held after close:", err)
	}
}
