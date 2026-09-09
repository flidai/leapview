package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/recoveryset"
	recoverypg "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/flidai/leapview/internal/recoveryset/successor"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The reader represents already-uploaded exact-version payloads. It deliberately
// has no latest lookup. These are PostgreSQL persistence tests, not provider tests.
type successorPayloadReader struct {
	mu            sync.Mutex
	objects       map[recoverypg.ValidatedLocator][]byte
	reads, closes int
}
type successorBody struct {
	io.Reader
	close func()
}

type successorReaderFunc func(context.Context, recoverypg.ValidatedLocator) (io.ReadCloser, error)

func (f successorReaderFunc) ReadExact(ctx context.Context, l recoverypg.ValidatedLocator) (io.ReadCloser, error) {
	return f(ctx, l)
}

type successorFailedClose struct {
	io.ReadCloser
	cause error
}

func (b successorFailedClose) Close() error { _ = b.ReadCloser.Close(); return b.cause }

func (b *successorBody) Close() error { b.close(); return nil }
func (r *successorPayloadReader) ReadExact(ctx context.Context, l recoverypg.ValidatedLocator) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, ok := r.objects[l]
	if !ok {
		return nil, errors.New("exact payload version unavailable")
	}
	r.reads++
	return &successorBody{Reader: bytes.NewReader(bytes.Clone(raw)), close: func() { r.mu.Lock(); defer r.mu.Unlock(); r.closes++ }}, nil
}

func successorInputs(t *testing.T, name string) (recoverypg.Set3Input, recoverypg.TrustInput, *successorPayloadReader) {
	t.Helper()
	set, evidence, docs := successorGolden(t, name)
	reader := &successorPayloadReader{objects: make(map[recoverypg.ValidatedLocator][]byte)}
	trust := recoverypg.TrustInput{Evidence: evidence, Generation: recoverypg.TrustGeneration{IncarnationID: "11111111-1111-4111-8111-111111111111", Revision: 1, PolicyDigest: "sha256:" + fmt.Sprintf("%064x", 19)}}
	input := recoverypg.Set3Input{Set: set}
	values := []struct {
		name, family string
		version      int32
		value        interface{ Digest() (string, error) }
		target       *recoverypg.PayloadReference
	}{
		{"set", recoverypg.PayloadFamilySet, 3, set, &input.Payloads.Set},
		{"manifest", recoverypg.PayloadFamilyManifest, 2, evidence.Manifest, &input.Payloads.Manifest},
		{"anchor", recoverypg.PayloadFamilyAnchor, 2, evidence.Anchor, &input.Payloads.Anchor},
		{"profiles", recoverypg.PayloadFamilyProfiles, 2, evidence.Profiles, &input.Payloads.Profiles},
		{"receipt", recoverypg.PayloadFamilyReceipt, 2, evidence.Receipt, &input.Payloads.Receipt},
		{"authority", recoverypg.PayloadFamilyAuthority, 2, evidence.Authorities, &input.Payloads.Authority},
	}
	for _, v := range values {
		digest, err := v.value.Digest()
		if err != nil {
			t.Fatal(err)
		}
		raw := bytes.Clone(docs[v.name])
		sum := sha256.Sum256(raw)
		locator := recoverypg.ValidatedLocator{Backend: "s3", StorageProfileID: "22222222-2222-4222-8222-222222222222", StorageProfileRevision: 1,
			AccountIdentity: "qualification", Endpoint: "https://evidence.example.test", Region: "test-region-1", Bucket: "qualification", Namespace: "evidence", Key: fmt.Sprintf("evidence/%s/v%d/sha256/%s", strings.TrimPrefix(v.family, "leapview."), v.version, hex.EncodeToString(sum[:])), VersionID: "immutable-" + v.name,
			PayloadFamily: v.family, PayloadVersion: v.version, PayloadDigest: digest, PayloadSHA256: hex.EncodeToString(sum[:]), PayloadSize: int64(len(raw))}
		*v.target = recoverypg.PayloadReference{Locator: locator, CanonicalBytes: raw}
		reader.objects[locator] = bytes.Clone(raw)
	}
	return input, trust, reader
}

func provisionSuccessorGeneration(t *testing.T, admin *pgxpool.Pool, g recoverypg.TrustGeneration) {
	t.Helper()
	// Operator-owned policy provisioning, not a test-only permission grant.
	_, err := admin.Exec(t.Context(), `INSERT INTO recovery.successor_trust_generation(singleton,incarnation_id,revision,policy_digest) VALUES(true,$1,$2,$3)`, g.IncarnationID, g.Revision, g.PolicyDigest)
	if err != nil {
		t.Fatal(err)
	}
}

func successorRepo(pool *pgxpool.Pool, reader *successorPayloadReader, trust recoverypg.TrustInput) *recoverypg.SuccessorRepository {
	return recoverypg.NewSuccessorRepository(pool, recoverypg.SuccessorOptions{Reader: reader, Trust: func(context.Context, string) (recoverypg.TrustInput, error) { return trust, nil }})
}

func TestSuccessorLocatorRejectsAmbiguousTransport(t *testing.T) {
	input, _, _ := successorInputs(t, "minimal")
	base := input.Payloads.Manifest.Locator
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*recoverypg.ValidatedLocator)
	}{
		{"missing backend", func(l *recoverypg.ValidatedLocator) { l.Backend = "" }},
		{"unknown family", func(l *recoverypg.ValidatedLocator) { l.PayloadFamily = "unknown"; l.PayloadVersion = 0 }},
		{"missing host", func(l *recoverypg.ValidatedLocator) { l.Endpoint = "https:" }},
		{"credential URL", func(l *recoverypg.ValidatedLocator) { l.Endpoint = "https://user:secret@evidence.example.test" }},
		{"mutable version", func(l *recoverypg.ValidatedLocator) { l.VersionID = "latest" }},
		{"unversioned object", func(l *recoverypg.ValidatedLocator) { l.VersionID = "null" }},
		{"missing profile revision", func(l *recoverypg.ValidatedLocator) { l.StorageProfileRevision = 0 }},
		{"wrong namespace", func(l *recoverypg.ValidatedLocator) { l.Key = "other/manifest.json" }},
		{"oversized payload", func(l *recoverypg.ValidatedLocator) { l.PayloadSize = int64(successor.MaxDocumentBytes) + 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := base
			tc.mutate(&l)
			if err := l.Validate(); err == nil {
				t.Fatal("ambiguous locator accepted")
			}
		})
	}
}

func TestSuccessorPersistenceRestartRetryAndExactRead(t *testing.T) {
	for _, name := range []string{"minimal", "closure", "empty"} {
		t.Run(name, func(t *testing.T) {
			pool, admin, reopenURL := successorDatabase(t)
			input, trust, reader := successorInputs(t, name)
			provisionSuccessorGeneration(t, admin, trust.Generation)
			repo := successorRepo(pool, reader, trust)
			manifestInput := recoverypg.ManifestInput{Set: input.Set, Payloads: input.Payloads}
			for range 2 {
				if err := repo.InsertManifest(t.Context(), manifestInput); err != nil {
					t.Fatal(err)
				}
			}
			gotManifest, err := repo.ReadManifest(t.Context(), input.Set.ManagedObservationManifestDigest)
			if err != nil {
				t.Fatal(err)
			}
			gotRaw, err := gotManifest.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotRaw, input.Payloads.Manifest.CanonicalBytes) {
				t.Fatal("manifest canonical bytes changed")
			}
			for range 2 {
				if _, err := repo.CreateSet3(t.Context(), input); err != nil {
					t.Fatal(err)
				}
			}
			pool.Close()
			reopened, err := pgxpool.New(t.Context(), reopenURL)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reloaded, err := successorRepo(reopened, reader, trust).ReadSet3(t.Context(), input.Set.ID)
			if err != nil {
				t.Fatal(err)
			}
			gotRaw, err = reloaded.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotRaw, input.Payloads.Set.CanonicalBytes) {
				t.Fatal("set bytes changed after connection restart")
			}
			if err := reloaded.ValidateEvidence(trust.Evidence); err != nil {
				t.Fatal(err)
			}
			reader.mu.Lock()
			defer reader.mu.Unlock()
			if reader.reads == 0 || reader.reads != reader.closes {
				t.Fatalf("payload readers leaked: read=%d close=%d", reader.reads, reader.closes)
			}
		})
	}
}

func TestSuccessorPersistenceRejectsInvalidEvidence(t *testing.T) {
	pool, admin, _ := successorDatabase(t)
	input, trust, reader := successorInputs(t, "minimal")
	provisionSuccessorGeneration(t, admin, trust.Generation)
	for _, phase := range []string{"open", "close"} {
		t.Run("sanitized transport "+phase, func(t *testing.T) {
			cause := errors.New("provider failure with credential=private-test-sentinel")
			failing := successorReaderFunc(func(ctx context.Context, l recoverypg.ValidatedLocator) (io.ReadCloser, error) {
				if phase == "open" {
					return nil, cause
				}
				body, err := reader.ReadExact(ctx, l)
				if err != nil {
					return nil, err
				}
				return successorFailedClose{ReadCloser: body, cause: cause}, nil
			})
			repo := recoverypg.NewSuccessorRepository(pool, recoverypg.SuccessorOptions{Reader: failing, Trust: func(context.Context, string) (recoverypg.TrustInput, error) { return trust, nil }})
			_, err := repo.CreateSet3(t.Context(), input)
			if !errors.Is(err, cause) || !errors.Is(err, recoverypg.ErrSuccessorTampered) {
				t.Fatalf("transport cause/category lost: %v", err)
			}
			if strings.Contains(err.Error(), "private-test-sentinel") {
				t.Fatal("provider credential leaked in diagnostic")
			}
		})
	}
	for _, name := range []string{"missing manifest", "wrong digest", "wrong anchor", "invalid receipt", "cache substitution", "locator substitution", "stale generation"} {
		t.Run(name, func(t *testing.T) {
			bad, badTrust, badReader := successorInputs(t, "minimal")
			switch name {
			case "missing manifest":
				delete(badReader.objects, bad.Payloads.Manifest.Locator)
			case "wrong digest":
				bad.Payloads.Manifest.Locator.PayloadDigest = "sha256:" + fmt.Sprintf("%064x", 22)
			case "wrong anchor":
				bad.Set.SourceFrontierAnchorDigest = "sha256:" + fmt.Sprintf("%064x", 23)
			case "invalid receipt":
				raw := bytes.Clone(badReader.objects[bad.Payloads.Receipt.Locator])
				raw[len(raw)-3] ^= 1
				badReader.objects[bad.Payloads.Receipt.Locator] = raw
			case "cache substitution":
				bad.Payloads.Manifest.CanonicalBytes = []byte(`{}`)
			case "locator substitution":
				bad.Payloads.Manifest.Locator.Namespace = "unrelated/"
			case "stale generation":
				badTrust.Generation.Revision++
			}
			if _, err := successorRepo(pool, badReader, badTrust).CreateSet3(t.Context(), bad); err == nil {
				t.Fatal("invalid evidence accepted")
			}
			var count int
			if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM recovery.recovery_set_v3`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("failed association left usable state")
			}
		})
	}
	// Repair succeeds; subsequent reads may not fall back to the SQL byte cache.
	repo := successorRepo(pool, reader, trust)
	if _, err := repo.CreateSet3(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	reader.mu.Lock()
	saved := reader.objects[input.Payloads.Manifest.Locator]
	delete(reader.objects, input.Payloads.Manifest.Locator)
	reader.mu.Unlock()
	if _, err := repo.ReadSet3(t.Context(), input.Set.ID); err == nil {
		t.Fatal("missing off-host evidence trusted SQL cache")
	}
	reader.mu.Lock()
	reader.objects[input.Payloads.Manifest.Locator] = saved
	reader.mu.Unlock()
	if _, err := repo.ReadSet3(t.Context(), input.Set.ID); err != nil {
		t.Fatal(err)
	}
	trust.Evidence.Authorities = successor.AuthorityRegistry{}
	if _, err := successorRepo(pool, reader, trust).ReadSet3(t.Context(), input.Set.ID); err == nil {
		t.Fatal("persisted authority replaced independently rejected authority")
	}
}

func TestSuccessorPersistenceConcurrentImmutableWinner(t *testing.T) {
	pool, admin, _ := successorDatabase(t)
	input, trust, reader := successorInputs(t, "minimal")
	provisionSuccessorGeneration(t, admin, trust.Generation)
	repo := successorRepo(pool, reader, trust)
	run := func(n int, fn func(int) error) []error {
		result := make([]error, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; result[i] = fn(i) }()
		}
		close(start)
		wg.Wait()
		return result
	}
	for _, err := range run(8, func(int) error {
		return repo.InsertManifest(t.Context(), recoverypg.ManifestInput{Set: input.Set, Payloads: input.Payloads})
	}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, err := range run(8, func(int) error { _, err := repo.CreateSet3(t.Context(), input); return err }) {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, err := range run(8, func(int) error { _, err := repo.ReadSet3(t.Context(), input.Set.ID); return err }) {
		if err != nil {
			t.Fatal(err)
		}
	}
	conflicting := input
	conflicting.Payloads.Manifest.Locator.VersionID = "different-immutable-version"
	reader.mu.Lock()
	reader.objects[conflicting.Payloads.Manifest.Locator] = bytes.Clone(input.Payloads.Manifest.CanonicalBytes)
	reader.mu.Unlock()
	results := run(8, func(i int) error {
		v := input
		if i%2 == 1 {
			v = conflicting
		}
		return repo.InsertManifest(t.Context(), recoverypg.ManifestInput{Set: v.Set, Payloads: v.Payloads})
	})
	for i, err := range results {
		if i%2 == 0 && err != nil {
			t.Fatal(err)
		}
		if i%2 == 1 && !errors.Is(err, recoverypg.ErrSuccessorConflict) {
			t.Fatalf("conflicting insert category: %v", err)
		}
	}
	var count int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM recovery.recovery_set_v3`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("%d winners", count)
	}
}

func TestSuccessorPersistenceAssociationRollback(t *testing.T) {
	pool, admin, _ := successorDatabase(t)
	input, trust, reader := successorInputs(t, "minimal")
	provisionSuccessorGeneration(t, admin, trust.Generation)
	// A storage failure after inserts must abort the entire association transaction.
	_, err := admin.Exec(t.Context(), `CREATE FUNCTION recovery.qualification_reject_root() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected association failure'; END $$; CREATE TRIGGER qualification_reject_root BEFORE INSERT ON recovery.recovery_set_v3_root FOR EACH ROW EXECUTE FUNCTION recovery.qualification_reject_root()`)
	if err != nil {
		t.Fatal(err)
	}
	repo := successorRepo(pool, reader, trust)
	if _, err := repo.CreateSet3(t.Context(), input); err == nil {
		t.Fatal("injected failure succeeded")
	}
	for _, table := range []string{"recovery_set_v3", "successor_manifest_binding", "successor_evidence_v2"} {
		var count int
		if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM recovery.`+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("partial commit in %s", table)
		}
	}
	if _, err := admin.Exec(t.Context(), `DROP TRIGGER qualification_reject_root ON recovery.recovery_set_v3_root; DROP FUNCTION recovery.qualification_reject_root()`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateSet3(t.Context(), input); err != nil {
		t.Fatal(err)
	}
}

func TestSuccessorPersistencePreservesV1AndReservesIdentity(t *testing.T) {
	pool, admin, _ := successorDatabase(t)
	input, trust, reader := successorInputs(t, "minimal")
	provisionSuccessorGeneration(t, admin, trust.Generation)
	raw, err := os.ReadFile("../testdata/successor-legacy/recoveryset-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var old recoveryset.RecoverySet
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	old.ID = input.Set.ID
	old.FrontierDigest = ""
	legacy := recoverypg.New(pool)
	created, err := legacy.Create(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}
	want, err := created.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := successorRepo(pool, reader, trust).CreateSet3(t.Context(), input); err == nil {
		t.Fatal("v3 replaced v1 identity")
	}
	loaded, err := legacy.ReadExact(t.Context(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loaded.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !created.IdentityEqual(loaded) || !created.FrontierEqual(loaded) {
		t.Fatal("v1 evidence changed")
	}
}

func TestSuccessorPersistenceGenerationChangesDuringVerification(t *testing.T) {
	pool, admin, _ := successorDatabase(t)
	input, trust, reader := successorInputs(t, "minimal")
	provisionSuccessorGeneration(t, admin, trust.Generation)
	var once sync.Once
	var advanceErr error
	changingReader := successorReaderFunc(func(ctx context.Context, l recoverypg.ValidatedLocator) (io.ReadCloser, error) {
		once.Do(func() {
			_, advanceErr = admin.Exec(ctx, `UPDATE recovery.successor_trust_generation SET revision=revision+1 WHERE singleton=true`)
		})
		if advanceErr != nil {
			return nil, advanceErr
		}
		return reader.ReadExact(ctx, l)
	})
	repo := recoverypg.NewSuccessorRepository(pool, recoverypg.SuccessorOptions{Reader: changingReader, Trust: func(context.Context, string) (recoverypg.TrustInput, error) { return trust, nil }})
	if _, err := repo.CreateSet3(t.Context(), input); !errors.Is(err, recoverypg.ErrSuccessorConflict) {
		t.Fatalf("stale pre-transaction authority category: %v", err)
	}
	var count int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM recovery.recovery_set_v3`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("stale generation associated")
	}
	trust.Generation.Revision++
	if _, err := successorRepo(pool, reader, trust).CreateSet3(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"manifest", "set"} {
		var readOnce sync.Once
		readReader := successorReaderFunc(func(ctx context.Context, l recoverypg.ValidatedLocator) (io.ReadCloser, error) {
			var updateErr error
			readOnce.Do(func() {
				_, updateErr = admin.Exec(ctx, `UPDATE recovery.successor_trust_generation SET revision=revision+1 WHERE singleton=true`)
			})
			if updateErr != nil {
				return nil, updateErr
			}
			return reader.ReadExact(ctx, l)
		})
		readRepo := recoverypg.NewSuccessorRepository(pool, recoverypg.SuccessorOptions{Reader: readReader, Trust: func(context.Context, string) (recoverypg.TrustInput, error) { return trust, nil }})
		var readErr error
		if kind == "manifest" {
			_, readErr = readRepo.ReadManifest(t.Context(), input.Set.ManagedObservationManifestDigest)
		} else {
			_, readErr = readRepo.ReadSet3(t.Context(), input.Set.ID)
		}
		if !errors.Is(readErr, recoverypg.ErrSuccessorConflict) {
			t.Fatalf("%s accepted revocation during network read: %v", kind, readErr)
		}
		trust.Generation.Revision++
		if _, err := successorRepo(pool, reader, trust).ReadSet3(t.Context(), input.Set.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSuccessorPersistenceFirstContendedManifestWinner(t *testing.T) {
	pool, admin, _ := successorDatabase(t)
	a, trust, reader := successorInputs(t, "minimal")
	provisionSuccessorGeneration(t, admin, trust.Generation)
	b := a
	b.Payloads.Manifest.Locator.VersionID = "second-version-same-bytes"
	reader.objects[b.Payloads.Manifest.Locator] = bytes.Clone(a.Payloads.Manifest.CanonicalBytes)
	repo := successorRepo(pool, reader, trust)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, input := range []recoverypg.Set3Input{a, b} {
		go func() {
			<-start
			results <- repo.InsertManifest(t.Context(), recoverypg.ManifestInput{Set: input.Set, Payloads: input.Payloads})
		}()
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, recoverypg.ErrSuccessorConflict):
			conflicts++
		default:
			t.Fatalf("unexpected contention outcome: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d", successes, conflicts)
	}
	if _, err := repo.ReadManifest(t.Context(), a.Set.ManagedObservationManifestDigest); err != nil {
		t.Fatal(err)
	}
}

func TestSuccessorPersistenceImmutableRowsAndUntrustedSQL(t *testing.T) {
	pool, admin, _ := successorDatabase(t)
	input, trust, reader := successorInputs(t, "minimal")
	provisionSuccessorGeneration(t, admin, trust.Generation)
	repo := successorRepo(pool, reader, trust)
	if _, err := repo.CreateSet3(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE recovery.successor_evidence_v2 SET canonical_bytes=canonical_bytes`,
		`DELETE FROM recovery.successor_evidence_v2`,
		`UPDATE recovery.successor_manifest_binding SET verification_metadata=verification_metadata`,
		`DELETE FROM recovery.recovery_set_v3`,
	} {
		if _, err := pool.Exec(t.Context(), statement); err == nil {
			t.Fatalf("maintenance mutable evidence path: %s", statement)
		}
		if _, err := admin.Exec(t.Context(), statement); err == nil {
			t.Fatalf("immutability trigger missing: %s", statement)
		}
	}
	if _, err := repo.ReadSet3(t.Context(), input.Set.ID); err != nil {
		t.Fatal(err)
	}
	// Fault injection explicitly disables a guard using the disposable database
	// administrator. A damaged SQL scalar must still not be trusted on read.
	if _, err := admin.Exec(t.Context(), `ALTER TABLE recovery.recovery_set_v3 DISABLE TRIGGER recovery_set_v3_immutable; UPDATE recovery.recovery_set_v3 SET created_by='corrupted-sql-metadata'; ALTER TABLE recovery.recovery_set_v3 ENABLE TRIGGER recovery_set_v3_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReadSet3(t.Context(), input.Set.ID); !errors.Is(err, recoverypg.ErrSuccessorTampered) {
		t.Fatalf("untrusted scalar accepted/category lost: %v", err)
	}
}
