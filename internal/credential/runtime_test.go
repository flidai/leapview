package credential

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/credential/encryption"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

func TestRuntimeResolverUsesExactAuthorityVersionAndClearsCallbackValues(t *testing.T) {
	fixture := newRuntimeFixture(t)
	newer := fixture.addVersion(t)
	fixture.authority.references = []RuntimeCredentialReference{fixture.reference, fixture.reference}

	var retained map[string]string
	var deliveredReference RuntimeCredentialReference
	err := fixture.resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(reference RuntimeCredentialReference, fields map[string]string) error {
		deliveredReference = reference
		retained = fields
		if fields["password"] != "pinned-secret" {
			t.Fatalf("callback password = %q, want exact pinned version", fields["password"])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fixture.repository.reads != 1 || fixture.repository.lastVersionID != fixture.reference.VersionID || fixture.reference.VersionID == newer.Metadata.Binding.VersionID {
		t.Fatalf("repository read version=%q reads=%d; want exact authority-pinned version %q, newer=%q", fixture.repository.lastVersionID, fixture.repository.reads, fixture.reference.VersionID, newer.Metadata.Binding.VersionID)
	}
	if fixture.authority.calls != 2 {
		t.Fatalf("authority calls = %d, want pre-read and pre-decrypt checks", fixture.authority.calls)
	}
	if deliveredReference != fixture.reference {
		t.Fatalf("callback reference = %+v, want authority-pinned scope/version %+v", deliveredReference, fixture.reference)
	}
	if len(retained) != 0 {
		t.Fatal("consumer retained credential fields after callback")
	}
	assertClearedBytes(t, fixture.keys.lastPlaintext)
}

func TestRuntimeResolverRequiresAuthorityBeforeStorageAndDecryption(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "denied", err: ErrForbidden, want: ErrForbidden},
		{name: "uncommitted draft", err: ErrNotFound, want: ErrNotFound},
		{name: "authority unavailable", err: errors.New("authority backend includes secret"), want: ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeFixture(t)
			fixture.authority.err = test.err
			err := fixture.resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(RuntimeCredentialReference, map[string]string) error {
				t.Fatal("unauthorized credential reached consumer")
				return nil
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if fixture.repository.reads != 0 || fixture.keys.decryptCalls != 0 {
				t.Fatalf("unauthorized read reached storage/decrypt: reads=%d decrypts=%d", fixture.repository.reads, fixture.keys.decryptCalls)
			}
		})
	}

	t.Run("revoked after exact row read", func(t *testing.T) {
		fixture := newRuntimeFixture(t)
		fixture.authority.err = ErrForbidden
		fixture.authority.errAtCall = 2
		err := fixture.resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(RuntimeCredentialReference, map[string]string) error {
			t.Fatal("revoked runtime authority reached consumer")
			return nil
		})
		if !errors.Is(err, ErrForbidden) || fixture.repository.reads != 1 || fixture.keys.decryptCalls != 0 {
			t.Fatalf("revoked authority error=%v reads=%d decrypts=%d", err, fixture.repository.reads, fixture.keys.decryptCalls)
		}
	})
}

func TestRuntimeResolverRejectsInvalidIdentityAndScopeBeforeReading(t *testing.T) {
	t.Run("identity scope mismatch", func(t *testing.T) {
		fixture := newRuntimeFixture(t)
		identity, err := projectgraph.NewServingIdentity("other-project", fixture.resource.Environment, "generation-a")
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.resolver.WithCredential(t.Context(), identity, fixture.resource, func(RuntimeCredentialReference, map[string]string) error { return nil }); !errors.Is(err, ErrInvalid) {
			t.Fatalf("mismatched identity error = %v, want invalid", err)
		}
		if fixture.authority.calls != 0 || fixture.repository.reads != 0 || fixture.keys.decryptCalls != 0 {
			t.Fatal("identity mismatch reached authority, storage, or decryption")
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*RuntimeCredentialReference)
	}{
		{name: "different resource", mutate: func(reference *RuntimeCredentialReference) { reference.Scope.Resource.ResourceID = "connection:other" }},
		{name: "wrong provider", mutate: func(reference *RuntimeCredentialReference) { reference.Scope.Provider = "mysql" }},
		{name: "wrong purpose", mutate: func(reference *RuntimeCredentialReference) { reference.Scope.Purpose = "agent-provider" }},
		{name: "missing owner", mutate: func(reference *RuntimeCredentialReference) { reference.Scope.OwnerID = "" }},
		{name: "invalid destination", mutate: func(reference *RuntimeCredentialReference) { reference.Scope.Destination = "unhashed" }},
		{name: "noncanonical version", mutate: func(reference *RuntimeCredentialReference) {
			reference.VersionID = strings.ToUpper(reference.VersionID)
		}},
		{name: "zero version", mutate: func(reference *RuntimeCredentialReference) { reference.VersionID = uuid.Nil.String() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeFixture(t)
			bad := fixture.reference
			test.mutate(&bad)
			fixture.authority.references = []RuntimeCredentialReference{bad}
			if err := fixture.resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(RuntimeCredentialReference, map[string]string) error { return nil }); !errors.Is(err, ErrForbidden) {
				t.Fatalf("invalid authority reference error = %v, want forbidden", err)
			}
			if fixture.repository.reads != 0 || fixture.keys.decryptCalls != 0 {
				t.Fatal("invalid authority reference reached storage or decryption")
			}
		})
	}
}

func TestRuntimeResolverRechecksReferenceAfterStorageRead(t *testing.T) {
	fixture := newRuntimeFixture(t)
	other := fixture.addVersion(t)
	fixture.authority.references = []RuntimeCredentialReference{
		fixture.reference,
		{Scope: fixture.scope, VersionID: other.Metadata.Binding.VersionID},
	}

	err := fixture.resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(RuntimeCredentialReference, map[string]string) error {
		t.Fatal("changed authority reached credential consumer")
		return nil
	})
	if !errors.Is(err, ErrConflict) || fixture.repository.reads != 1 || fixture.keys.decryptCalls != 0 {
		t.Fatalf("changed authority error=%v reads=%d decrypts=%d", err, fixture.repository.reads, fixture.keys.decryptCalls)
	}
}

func TestRuntimeResolverRejectsStoredMetadataSubstitutionBeforeDecrypt(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*encryption.Binding)
	}{
		{name: "destination", mutate: func(binding *encryption.Binding) { binding.Destination = "sha256:" + strings.Repeat("c", 64) }},
		{name: "version", mutate: func(binding *encryption.Binding) { binding.VersionID = uuid.NewString() }},
		{name: "owner", mutate: func(binding *encryption.Binding) { binding.OwnerID = "foreign-owner" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeFixture(t)
			stored := fixture.repository.versions[fixture.reference.VersionID]
			test.mutate(&stored.Metadata.Binding)
			fixture.repository.versions[fixture.reference.VersionID] = stored
			fixture.repository.ignoreStoredFilters = true
			fixture.authority.references = []RuntimeCredentialReference{fixture.reference, fixture.reference}
			err := fixture.resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(RuntimeCredentialReference, map[string]string) error { return nil })
			if !errors.Is(err, ErrNotFound) || fixture.keys.decryptCalls != 0 {
				t.Fatalf("substituted metadata error=%v decrypts=%d", err, fixture.keys.decryptCalls)
			}
		})
	}
}

func TestRuntimeResolverSanitizesDecryptAndCredentialShapeFailures(t *testing.T) {
	t.Run("decrypt failure", func(t *testing.T) {
		fixture := newRuntimeFixture(t)
		fixture.authority.references = []RuntimeCredentialReference{fixture.reference, fixture.reference}
		fixture.keys.decryptErr = errors.New("invalid key with private provider detail")
		err := fixture.resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(RuntimeCredentialReference, map[string]string) error {
			t.Fatal("failed decryption reached consumer")
			return nil
		})
		if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private provider detail") {
			t.Fatalf("decrypt error was not sanitized: %v", err)
		}
		assertClearedBytes(t, fixture.keys.lastPlaintext)
	})

	t.Run("repository failure", func(t *testing.T) {
		fixture := newRuntimeFixture(t)
		fixture.repository.readErr = errors.New("postgres failure includes private-provider-detail")
		err := fixture.resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(RuntimeCredentialReference, map[string]string) error {
			t.Fatal("storage failure reached consumer")
			return nil
		})
		if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private-provider-detail") {
			t.Fatalf("repository error was not sanitized: %v", err)
		}
	})

	t.Run("unsupported credential shape", func(t *testing.T) {
		fixture := newRuntimeFixture(t)
		fixture.authority.references = []RuntimeCredentialReference{fixture.reference, fixture.reference}
		fixture.keys.plaintext = []byte(`{"password":"password-secret","connection_string":"host=attacker"}`)
		err := fixture.resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(RuntimeCredentialReference, map[string]string) error {
			t.Fatal("unsupported fields reached consumer")
			return nil
		})
		if !errors.Is(err, ErrValidationFailed) {
			t.Fatalf("credential shape error = %v", err)
		}
		assertClearedBytes(t, fixture.keys.lastPlaintext)
	})
}

func TestRuntimeResolverSanitizesCallbackErrorsAndClearsValues(t *testing.T) {
	fixture := newRuntimeFixture(t)
	fixture.authority.references = []RuntimeCredentialReference{fixture.reference, fixture.reference}
	var retained map[string]string
	err := fixture.resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(_ RuntimeCredentialReference, fields map[string]string) error {
		retained = fields
		return errors.New("provider failed with password pinned-secret")
	})
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "pinned-secret") {
		t.Fatalf("callback error was not sanitized: %v", err)
	}
	if len(retained) != 0 {
		t.Fatal("callback error retained the field map")
	}
	assertClearedBytes(t, fixture.keys.lastPlaintext)
}

func TestRuntimeResolverClearsSecretsAndRedactsConsumerPanics(t *testing.T) {
	fixture := newRuntimeFixture(t)
	fixture.authority.references = []RuntimeCredentialReference{fixture.reference, fixture.reference}
	var retained map[string]string
	var panicValue any
	func() {
		defer func() { panicValue = recover() }()
		_ = fixture.resolver.WithCredential(t.Context(), fixture.identity, fixture.resource, func(_ RuntimeCredentialReference, fields map[string]string) error {
			retained = fields
			panic("provider panic contains pinned-secret")
		})
	}()
	if panicValue != errRuntimeConsumerPanicked {
		t.Fatalf("panic value = %v, want redacted runtime panic sentinel", panicValue)
	}
	if len(retained) != 0 {
		t.Fatal("consumer panic retained the field map")
	}
	assertClearedBytes(t, fixture.keys.lastPlaintext)
}

func TestRuntimeResolverUncaughtConsumerPanicRedactsProcessOutput(t *testing.T) {
	const childFlag = "LEAPVIEW_TEST_RUNTIME_CONSUMER_PANIC"
	if os.Getenv(childFlag) == "1" {
		_ = invokeRuntimeConsumer(func(RuntimeCredentialReference, map[string]string) error {
			panic("synthetic-password-sentinel")
		}, RuntimeCredentialReference{}, map[string]string{"password": "synthetic-password-sentinel"})
		t.Fatal("unhandled consumer panic returned normally")
	}

	command := exec.Command(os.Args[0], "-test.run=^TestRuntimeResolverUncaughtConsumerPanicRedactsProcessOutput$")
	command.Env = make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, childFlag+"=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, childFlag+"=1")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("child returned success; output=%s", output)
	}
	if bytes.Contains(output, []byte("synthetic-password-sentinel")) {
		t.Fatalf("child process exposed original panic payload: %s", output)
	}
	if !bytes.Contains(output, []byte(errRuntimeConsumerPanicked.Error())) {
		t.Fatalf("child output lacks fixed panic sentinel: %s", output)
	}
}

func TestRuntimeResolverPreservesActualContextCancellationAndClearsValues(t *testing.T) {
	fixture := newRuntimeFixture(t)
	fixture.authority.references = []RuntimeCredentialReference{fixture.reference, fixture.reference}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var retained map[string]string
	err := fixture.resolver.WithCredential(ctx, fixture.identity, fixture.resource, func(_ RuntimeCredentialReference, fields map[string]string) error {
		retained = fields
		cancel()
		return errors.New("provider failed with pinned-secret")
	})
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "pinned-secret") {
		t.Fatalf("cancellation result = %v, want actual context cancellation", err)
	}
	if len(retained) != 0 {
		t.Fatal("cancellation retained the field map")
	}
	assertClearedBytes(t, fixture.keys.lastPlaintext)
}

func TestRuntimeResolverDoesNotDispatchAfterCancellationDuringDecrypt(t *testing.T) {
	fixture := newRuntimeFixture(t)
	fixture.authority.references = []RuntimeCredentialReference{fixture.reference, fixture.reference}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fixture.keys.afterDecrypt = cancel
	consumerCalled := false
	err := fixture.resolver.WithCredential(ctx, fixture.identity, fixture.resource, func(RuntimeCredentialReference, map[string]string) error {
		consumerCalled = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || consumerCalled {
		t.Fatalf("decrypt-time cancellation error=%v consumerCalled=%v", err, consumerCalled)
	}
	assertClearedBytes(t, fixture.keys.lastPlaintext)
}

func TestNewRuntimeResolverRequiresEveryDependency(t *testing.T) {
	fixture := newRuntimeFixture(t)
	var nilRepository *runtimeTestRepository
	if _, err := NewRuntimeResolver(nilRepository, fixture.keys, fixture.authority); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing/typed-nil repository error = %v", err)
	}
	if _, err := NewRuntimeResolver(fixture.repository, nil, fixture.authority); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing keyring error = %v", err)
	}
	if _, err := NewRuntimeResolver(fixture.repository, fixture.keys, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing authority error = %v", err)
	}
}

type runtimeFixture struct {
	resolver   *RuntimeResolver
	identity   projectgraph.ServingIdentity
	resource   Resource
	scope      Scope
	reference  RuntimeCredentialReference
	repository *runtimeTestRepository
	keys       *runtimeTestKeyring
	authority  *runtimeTestAuthority
}

func newRuntimeFixture(t *testing.T) runtimeFixture {
	t.Helper()
	resource := connectionResource()
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(resource.ProjectID), resource.Environment, "generation-a")
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{
		Resource: resource, OwnerID: "customer-a", Purpose: runtimeConnectionPurpose,
		Provider: "postgres", Destination: "sha256:" + strings.Repeat("a", 64),
	}
	versionID := uuid.NewString()
	binding := expectedEncryptionBinding("deployment-a", scope, versionID)
	plaintext, err := json.Marshal(map[string]string{"password": "pinned-secret"})
	if err != nil {
		t.Fatal(err)
	}
	version := StoredVersion{
		Metadata: Metadata{Binding: binding, ActorID: "draft-actor", CreatedAt: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)},
		Envelope: encryption.Envelope{Format: "test-only", KeyID: "key-a", Ciphertext: []byte("opaque")},
	}
	repository := &runtimeTestRepository{versions: map[string]StoredVersion{versionID: version}}
	keys := &runtimeTestKeyring{plaintext: plaintext, binding: binding}
	reference := RuntimeCredentialReference{Scope: scope, VersionID: versionID}
	authority := &runtimeTestAuthority{references: []RuntimeCredentialReference{reference, reference}}
	resolver, err := NewRuntimeResolver(repository, keys, authority)
	if err != nil {
		t.Fatal(err)
	}
	return runtimeFixture{
		resolver: resolver, identity: identity, resource: resource, scope: scope,
		reference: reference, repository: repository, keys: keys, authority: authority,
	}
}

func (fixture *runtimeFixture) addVersion(t *testing.T) StoredVersion {
	t.Helper()
	versionID := uuid.NewString()
	binding := expectedEncryptionBinding("deployment-a", fixture.scope, versionID)
	version := StoredVersion{
		Metadata: Metadata{Binding: binding, ActorID: "draft-actor", CreatedAt: time.Date(2026, 9, 28, 9, 1, 0, 0, time.UTC)},
		Envelope: encryption.Envelope{Format: "test-only", KeyID: "key-a", Ciphertext: []byte("opaque")},
	}
	fixture.repository.versions[versionID] = version
	return version
}

func assertClearedBytes(t *testing.T, value []byte) {
	t.Helper()
	if len(value) == 0 {
		t.Fatal("fake keyring did not observe plaintext")
	}
	if !bytes.Equal(value, make([]byte, len(value))) {
		t.Fatal("resolver retained decrypted plaintext bytes")
	}
}

type runtimeTestRepository struct {
	versions            map[string]StoredVersion
	reads               int
	lastVersionID       string
	lastDeployment      string
	lastOwner           string
	lastResource        Resource
	readErr             error
	ignoreStoredFilters bool
}

func (repository *runtimeTestRepository) GetStoredDraft(_ context.Context, deploymentID, ownerID string, resource Resource, versionID string) (StoredVersion, error) {
	repository.reads++
	repository.lastDeployment = deploymentID
	repository.lastOwner = ownerID
	repository.lastResource = resource
	repository.lastVersionID = versionID
	if repository.readErr != nil {
		return StoredVersion{}, repository.readErr
	}
	version, ok := repository.versions[versionID]
	if !ok || !repository.ignoreStoredFilters && (!metadataMatchesScope(version.Metadata, deploymentID, ownerID, resource) || version.Metadata.Binding.VersionID != versionID) {
		return StoredVersion{}, ErrNotFound
	}
	return version, nil
}

type runtimeTestKeyring struct {
	plaintext     []byte
	lastPlaintext []byte
	binding       encryption.Binding
	decryptErr    error
	decryptCalls  int
	afterDecrypt  func()
}

func (*runtimeTestKeyring) DeploymentID() string { return "deployment-a" }

func (keyring *runtimeTestKeyring) Decrypt(binding encryption.Binding, _ encryption.Envelope) ([]byte, error) {
	keyring.decryptCalls++
	if binding != keyring.binding {
		return nil, errors.New("associated data does not match")
	}
	keyring.lastPlaintext = bytes.Clone(keyring.plaintext)
	if keyring.afterDecrypt != nil {
		keyring.afterDecrypt()
	}
	return keyring.lastPlaintext, keyring.decryptErr
}

type runtimeTestAuthority struct {
	references []RuntimeCredentialReference
	call       func(int, projectgraph.ServingIdentity, Resource)
	err        error
	calls      int
	errAtCall  int
}

func (authority *runtimeTestAuthority) ResolveRuntimeCredential(_ context.Context, identity projectgraph.ServingIdentity, resource Resource) (RuntimeCredentialReference, error) {
	authority.calls++
	if authority.call != nil {
		authority.call(authority.calls, identity, resource)
	}
	if authority.err != nil && (authority.errAtCall == 0 || authority.errAtCall == authority.calls) {
		return RuntimeCredentialReference{}, authority.err
	}
	index := min(authority.calls-1, len(authority.references)-1)
	if index < 0 {
		return RuntimeCredentialReference{}, ErrNotFound
	}
	return authority.references[index], nil
}
