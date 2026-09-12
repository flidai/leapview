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
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

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
		{"core", recoverypg.PayloadFamilyCore, 2, evidence.Receipt.Core, &input.Payloads.Core},
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

func TestSuccessorTrustResolverErrorIsSanitized(t *testing.T) {
	const secret = "trust-resolver-private-test-sentinel"
	cause := errors.New("trust resolver failed with credential=" + secret)
	var db *pgxpool.Pool
	repo := recoverypg.NewSuccessorRepository(db, recoverypg.SuccessorOptions{
		Reader: successorReaderFunc(func(context.Context, recoverypg.ValidatedLocator) (io.ReadCloser, error) {
			return nil, errors.New("reader must not be called")
		}),
		Trust: func(context.Context, string) (recoverypg.TrustInput, error) {
			return recoverypg.TrustInput{}, cause
		},
	})

	_, err := repo.CreateSet3(t.Context(), recoverypg.Set3Input{})
	if err == nil {
		t.Fatal("trust resolver failure was accepted")
	}
	if !errors.Is(err, recoverypg.ErrSuccessorUntrusted) {
		t.Fatalf("trust resolver failure category lost: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("trust resolver cause lost: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("trust resolver secret leaked in diagnostic")
	}
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

func TestSuccessorPersistenceProcessRestartReadback(t *testing.T) {
	if os.Getenv("LEAPVIEW_FAI520_CORE_READBACK_HELPER") == "1" {
		runSuccessorCoreReadbackHelper(t)
		return
	}

	pool, admin, reopenURL := successorDatabase(t)
	input, trust, reader := successorInputs(t, "minimal")
	provisionSuccessorGeneration(t, admin, trust.Generation)
	if _, err := successorRepo(pool, reader, trust).CreateSet3(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	pool.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSuccessorPersistenceProcessRestartReadback$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"LEAPVIEW_FAI520_CORE_READBACK_HELPER=1",
		"LEAPVIEW_FAI520_CORE_READBACK_DSN="+reopenURL,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fresh-process capture-core readback failed: %v\n%s", err, output)
	}
}

func runSuccessorCoreReadbackHelper(t *testing.T) {
	dsn := os.Getenv("LEAPVIEW_FAI520_CORE_READBACK_DSN")
	if dsn == "" {
		t.Fatal("readback DSN is required")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	input, trust, reader := successorInputs(t, "minimal")
	got, err := successorRepo(pool, reader, trust).ReadSet3(t.Context(), input.Set.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotRaw, err := got.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotRaw, input.Payloads.Set.CanonicalBytes) {
		t.Fatal("set bytes changed after process restart")
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.reads != 7 || reader.closes != reader.reads {
		t.Fatalf("fresh process did not read and close all seven exact payloads: read=%d close=%d", reader.reads, reader.closes)
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
	for _, name := range []string{"missing manifest", "missing core", "wrong digest", "invalid core digest", "wrong anchor", "core authority mismatch", "invalid receipt", "cache substitution", "locator substitution", "stale generation"} {
		t.Run(name, func(t *testing.T) {
			bad, badTrust, badReader := successorInputs(t, "minimal")
			switch name {
			case "missing manifest":
				delete(badReader.objects, bad.Payloads.Manifest.Locator)
			case "missing core":
				delete(badReader.objects, bad.Payloads.Core.Locator)
			case "wrong digest":
				bad.Payloads.Manifest.Locator.PayloadDigest = "sha256:" + fmt.Sprintf("%064x", 22)
			case "invalid core digest":
				locator := bad.Payloads.Core.Locator
				core := badTrust.Evidence.Receipt.Core
				core.CaptureID = "different-capture"
				raw, err := core.CanonicalJSON()
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(raw)
				bad.Payloads.Core.Locator.PayloadSHA256 = hex.EncodeToString(sum[:])
				bad.Payloads.Core.Locator.PayloadSize = int64(len(raw))
				delete(badReader.objects, locator)
				badReader.objects[bad.Payloads.Core.Locator] = raw
			case "wrong anchor":
				bad.Set.SourceFrontierAnchorDigest = "sha256:" + fmt.Sprintf("%064x", 23)
			case "core authority mismatch":
				badTrust.Evidence.Receipt.Core.AuthorityID = "authority-b"
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

func TestSuccessorPersistenceReadsLegacyV3WithoutFabricatedCoreTransport(t *testing.T) {
	pool, admin, _ := successorDatabase(t)
	input, trust, reader := successorInputs(t, "minimal")
	provisionSuccessorGeneration(t, admin, trust.Generation)
	insertLegacySuccessorSet(t, admin, input, trust)

	repo := successorRepo(pool, reader, trust)
	if _, err := repo.ReadManifest(t.Context(), input.Set.ManagedObservationManifestDigest); err != nil {
		t.Fatalf("legacy v3 manifest became unreadable: %v", err)
	}
	if _, err := repo.ReadSet3(t.Context(), input.Set.ID); err != nil {
		t.Fatalf("legacy v3 set became unreadable: %v", err)
	}
	if _, err := repo.CreateSet3(t.Context(), input); !errors.Is(err, recoverypg.ErrSuccessorConflict) {
		t.Fatalf("legacy evidence was silently upgraded instead of conflicting: %v", err)
	}
	var cores int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM recovery.successor_evidence_v2 WHERE payload_family='core'`).Scan(&cores); err != nil {
		t.Fatal(err)
	}
	if cores != 0 {
		t.Fatal("legacy v3 evidence gained a fabricated capture-core transport")
	}
}

func insertLegacySuccessorSet(t *testing.T, admin *pgxpool.Pool, input recoverypg.Set3Input, trust recoverypg.TrustInput) {
	t.Helper()
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	for _, item := range []struct {
		family string
		ref    recoverypg.PayloadReference
	}{
		{"manifest", input.Payloads.Manifest},
		{"anchor", input.Payloads.Anchor},
		{"profile", input.Payloads.Profiles},
		{"receipt", input.Payloads.Receipt},
		{"authority", input.Payloads.Authority},
	} {
		ref := item.ref
		if _, err := tx.Exec(t.Context(), `INSERT INTO recovery.successor_evidence_v2(payload_family,payload_version,payload_digest,canonical_bytes) VALUES($1,2,$2,$3)`, item.family, ref.Locator.PayloadDigest, ref.CanonicalBytes); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO recovery.successor_evidence_locator_v2(payload_family,payload_version,payload_digest,backend,storage_profile_id,storage_profile_revision,account_identity,endpoint,region,bucket,namespace,object_key,version_id,byte_length,raw_sha256) VALUES($1,2,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			item.family, ref.Locator.PayloadDigest, ref.Locator.Backend, ref.Locator.StorageProfileID, ref.Locator.StorageProfileRevision, ref.Locator.AccountIdentity, ref.Locator.Endpoint, ref.Locator.Region, ref.Locator.Bucket, ref.Locator.Namespace, ref.Locator.Key, ref.Locator.VersionID, ref.Locator.PayloadSize, strings.TrimPrefix(ref.Locator.PayloadSHA256, "sha256:")); err != nil {
			t.Fatal(err)
		}
	}
	setRaw, err := input.Set.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	setLocator, err := json.Marshal(input.Payloads.Set.Locator)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(struct {
		IncarnationID string `json:"incarnation_id"`
		Revision      int64  `json:"revision"`
		PolicyDigest  string `json:"policy_digest"`
		VerifiedAt    string `json:"verified_at"`
	}{trust.Generation.IncarnationID, trust.Generation.Revision, trust.Generation.PolicyDigest, trust.Evidence.VerificationTime.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")})
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, _ := trust.Evidence.Manifest.Digest()
	anchorDigest, _ := trust.Evidence.Anchor.Digest()
	profileDigest, _ := trust.Evidence.Profiles.Digest()
	receiptDigest, _ := trust.Evidence.Receipt.Digest()
	coreDigest, _ := trust.Evidence.Receipt.Core.Digest()
	authorityDigest, _ := trust.Evidence.Authorities.Digest()
	if _, err := tx.Exec(t.Context(), `INSERT INTO recovery.successor_manifest_binding(manifest_digest,set_id,anchor_digest,profile_digest,receipt_digest,authority_digest,canonical_set,set_locator,verification_metadata) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, manifestDigest, input.Set.ID, anchorDigest, profileDigest, receiptDigest, authorityDigest, setRaw, setLocator, metadata); err != nil {
		t.Fatal(err)
	}
	frontier, err := input.Set.CommitmentBytes()
	if err != nil {
		t.Fatal(err)
	}
	frontierDigest, err := input.Set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO recovery.recovery_set_v3(set_id,schema_version,manifest_digest,anchor_digest,profile_digest,receipt_digest,receipt_core_digest,authority_digest,frontier_projection,frontier_digest,canonical_bytes,created_by) VALUES($1,3,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, input.Set.ID, manifestDigest, anchorDigest, profileDigest, receiptDigest, coreDigest, authorityDigest, frontier, frontierDigest, setRaw, input.Set.CreatedBy); err != nil {
		t.Fatal(err)
	}
	for _, root := range input.Set.ObjectRoots {
		raw, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO recovery.recovery_set_v3_root(set_id,root_kind,root_uri,version_id,root_digest,provider_recovery_frontier,canonical_bytes) VALUES($1,$2,$3,$4,$5,$6,$7)`, input.Set.ID, root.Kind, root.URI, root.VersionID, root.Digest, root.ProviderRecoveryFrontier, raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
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
	conflictingCore := input
	conflictingCore.Payloads.Core.Locator.VersionID = "different-immutable-core-version"
	reader.mu.Lock()
	reader.objects[conflictingCore.Payloads.Core.Locator] = bytes.Clone(input.Payloads.Core.CanonicalBytes)
	reader.mu.Unlock()
	if _, err := repo.CreateSet3(t.Context(), conflictingCore); !errors.Is(err, recoverypg.ErrSuccessorConflict) {
		t.Fatalf("conflicting capture-core retry category: %v", err)
	}
	reader.mu.Lock()
	storedCore := reader.objects[input.Payloads.Core.Locator]
	delete(reader.objects, input.Payloads.Core.Locator)
	reader.mu.Unlock()
	if _, err := repo.ReadManifest(t.Context(), input.Set.ManagedObservationManifestDigest); !errors.Is(err, recoverypg.ErrSuccessorTampered) {
		t.Fatalf("manifest read trusted missing capture-core transport: %v", err)
	}
	if _, err := repo.ReadSet3(t.Context(), input.Set.ID); !errors.Is(err, recoverypg.ErrSuccessorTampered) {
		t.Fatalf("set read trusted missing capture-core transport: %v", err)
	}
	reader.mu.Lock()
	reader.objects[input.Payloads.Core.Locator] = storedCore
	reader.mu.Unlock()
	if _, err := repo.ReadSet3(t.Context(), input.Set.ID); err != nil {
		t.Fatalf("repaired capture-core transport did not read deterministically: %v", err)
	}
	if _, err := admin.Exec(t.Context(), `ALTER TABLE recovery.successor_manifest_binding DISABLE TRIGGER successor_manifest_binding_immutable`); err != nil {
		t.Fatal(err)
	}
	_, nullCoreErr := admin.Exec(t.Context(), `UPDATE recovery.successor_manifest_binding SET capture_core_digest=NULL WHERE manifest_digest=$1`, input.Set.ManagedObservationManifestDigest)
	if _, err := admin.Exec(t.Context(), `ALTER TABLE recovery.successor_manifest_binding ENABLE TRIGGER successor_manifest_binding_immutable`); err != nil {
		t.Fatal(err)
	}
	if nullCoreErr == nil {
		t.Fatal("capture-core required marker allowed a new binding to lose its transport reference")
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
