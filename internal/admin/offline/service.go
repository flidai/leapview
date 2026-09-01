package offline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

var ErrInstanceAlreadyInitialized = errors.New("LeapView instance is already initialized")

func (service *Service) Initialize(ctx context.Context, request InitializeRequest, out io.Writer) error {
	if request.Format != "json" {
		return fmt.Errorf("admin initialize supports only --format json")
	}
	email, err := service.initialAdminEmail()
	if err != nil {
		return err
	}
	lock, err := service.acquire(ctx)
	if err != nil {
		return err
	}
	defer lock.Release()

	environment, err := service.resolveEnvironment(ctx)
	if err != nil {
		return err
	}
	initialized, err := service.deps.State.Initialized(ctx)
	if err != nil {
		return err
	}
	if initialized {
		credentials, readErr := service.deps.Recovery.Read()
		if readErr == nil {
			if _, decodeErr := DecodeInitialCredentials(credentials); decodeErr != nil {
				return decodeErr
			}
			return writeAll(out, credentials)
		}
		if os.IsNotExist(readErr) {
			return ErrInstanceAlreadyInitialized
		}
		return readErr
	}
	if err := service.deps.Recovery.Remove(); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale initialization credentials: %w", err)
	}

	var encoded []byte
	_, err = service.deps.Initializer.Initialize(ctx, InitializationInput{
		Email: email, Environment: environment, Now: service.deps.Now().UTC(),
	}, func(credentials InitialCredentials) error {
		encoded, err = json.Marshal(credentials)
		if err != nil {
			return err
		}
		encoded = append(encoded, '\n')
		return service.deps.Recovery.Write(encoded)
	})
	if err != nil {
		if removeErr := service.deps.Recovery.Remove(); removeErr != nil && !os.IsNotExist(removeErr) {
			return errors.Join(err, fmt.Errorf("remove failed initialization credentials: %w", removeErr))
		}
		return err
	}
	return writeAll(out, encoded)
}

func DecodeInitialCredentials(contents []byte) (InitialCredentials, error) {
	var credentials InitialCredentials
	if err := json.Unmarshal(contents, &credentials); err != nil ||
		credentials.Email == "" ||
		credentials.TemporaryPassword == "" ||
		credentials.PublisherToken == "" ||
		credentials.PublisherTokenExpiresAt == "" {
		return InitialCredentials{}, fmt.Errorf("initialization credential recovery file is invalid")
	}
	return credentials, nil
}

func (service *Service) AcknowledgeInitialCredentials(ctx context.Context) error {
	lock, err := service.acquire(ctx)
	if err != nil {
		return err
	}
	defer lock.Release()
	if _, err := service.resolveEnvironment(ctx); err != nil {
		return err
	}
	initialized, err := service.deps.State.Initialized(ctx)
	if err != nil {
		return err
	}
	if !initialized {
		return fmt.Errorf("LeapView instance has not been initialized")
	}
	if err := service.deps.Recovery.Remove(); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("acknowledge initialization credentials: %w", err)
	}
	return nil
}

func (service *Service) Maintenance(ctx context.Context, request MaintenanceRequest, out io.Writer) error {
	if request.AuditDays < 0 || request.QueryDays < 0 || request.ArchivedAgentDays < 0 || request.AuthStateDays < 0 {
		return fmt.Errorf("retention days must be zero or greater")
	}
	release, err := service.acquireIf(ctx, request.Apply)
	if err != nil {
		return err
	}
	defer release.Release()
	result, err := service.deps.Retention.Prune(ctx, RetentionPolicy{
		AuditEventsMaxAge:             days(request.AuditDays),
		QueryEventsMaxAge:             days(request.QueryDays),
		ArchivedAgentConversationsAge: days(request.ArchivedAgentDays),
		AuthStateMaxAge:               days(request.AuthStateDays),
		DryRun:                        !request.Apply,
	})
	if err != nil {
		return fmt.Errorf("operational maintenance: %w", err)
	}
	mode := "dry-run"
	if request.Apply {
		mode = "apply"
	}
	fmt.Fprintf(out, "mode: %s\n", mode)
	fmt.Fprintf(out, "audit events: %d\n", result.AuditEventsDeleted)
	fmt.Fprintf(out, "delivered audit intents: %d\n", result.DeliveredAuditIntentsDeleted)
	fmt.Fprintf(out, "query events: %d\n", result.QueryEventsDeleted)
	fmt.Fprintf(out, "archived agent conversations: %d\n", result.ArchivedAgentConversationsDeleted)
	fmt.Fprintf(out, "expired oauth states: %d\n", result.ExpiredOAuthStatesDeleted)
	fmt.Fprintf(out, "stale sessions: %d\n", result.StaleSessionsDeleted)
	fmt.Fprintf(out, "stale api tokens: %d\n", result.StaleAPITokensDeleted)
	fmt.Fprintf(out, "stale service principal secrets: %d\n", result.StaleServicePrincipalSecretsDeleted)
	return nil
}

// BootstrapPhysicalPool validates and, when Apply is true, admits one
// operator-supplied physical-pool compatibility tuple. The operation is
// intentionally offline and lock-protected: production startup never
// synthesizes a pool from environment configuration.
func (service *Service) BootstrapPhysicalPool(ctx context.Context, request PhysicalPoolBootstrapRequest, out io.Writer) error {
	if service == nil || service.deps.PhysicalPool == nil {
		return fmt.Errorf("physical-pool bootstrap is unavailable")
	}
	if err := request.Pool.Validate(); err != nil {
		return fmt.Errorf("physical-pool identity: %w", err)
	}
	evidence := request.Evidence
	if err := evidence.Verify(); err != nil {
		return fmt.Errorf("physical-pool conformance evidence: %w", err)
	}
	if validator, ok := service.deps.PhysicalPool.(PhysicalPoolEvidenceValidator); ok {
		if err := validator.ValidateEvidence(evidence); err != nil {
			return fmt.Errorf("physical-pool conformance checklist: %w", err)
		}
	}
	if request.Pool.Compatibility.StableEqual(evidence.Compatibility) == false {
		return fmt.Errorf("physical-pool identity and evidence storage contract differ")
	}
	if !request.Apply {
		return writePoolBootstrapResult(out, PhysicalPoolBootstrapResult{
			PoolID: string(mustPoolID(request.Pool)), CompatibilityDigest: mustCompatibilityDigest(evidence.Compatibility),
			EvidenceDigest: evidence.Digest, ConformanceVersion: evidence.ConformanceVersion,
			Applied: false,
		})
	}
	lock, err := service.acquire(ctx)
	if err != nil {
		return err
	}
	defer lock.Release()
	result, err := service.deps.PhysicalPool.Bootstrap(ctx, request)
	if err != nil {
		return fmt.Errorf("physical-pool bootstrap: %w", err)
	}
	return writePoolBootstrapResult(out, result)
}

func writePoolBootstrapResult(out io.Writer, result PhysicalPoolBootstrapResult) error {
	if out == nil {
		return nil
	}
	_, err := fmt.Fprintf(out, "pool_id: %s\ncompatibility_digest: %s\nevidence_digest: %s\nconformance_version: %s\napplied: %t\n", result.PoolID, result.CompatibilityDigest, result.EvidenceDigest, result.ConformanceVersion, result.Applied)
	return err
}

func mustPoolID(pool physicalpool.PoolIdentity) physicalpool.PoolID {
	created, _ := physicalpool.NewPhysicalPool(pool)
	return created.ID
}

func mustCompatibilityDigest(tuple physicalpool.Compatibility) string {
	digest, _ := tuple.Digest()
	return digest
}

func (service *Service) resolveEnvironment(ctx context.Context) (string, error) {
	requested := strings.TrimSpace(service.config.Environment)
	bound, err := service.deps.State.Environment(ctx)
	if err == nil {
		if requested != "" && requested != bound {
			return "", fmt.Errorf("LeapView instance is bound to environment %q, not %q", bound, requested)
		}
		return bound, nil
	}
	if !errors.Is(err, ErrStateNotFound) {
		return "", fmt.Errorf("read instance environment: %w", err)
	}
	environment := service.configuredEnvironment()
	if err := service.deps.State.BindEnvironment(ctx, environment); err != nil {
		return "", err
	}
	return environment, nil
}

func (service *Service) configuredEnvironment() string {
	if value := strings.TrimSpace(service.config.Environment); value != "" {
		return value
	}
	if service.config.Production {
		return "prod"
	}
	return "dev"
}

func (service *Service) initialAdminEmail() (string, error) {
	email := strings.TrimSpace(service.config.BootstrapEmail)
	if email == "" {
		if service.config.Production {
			return "", fmt.Errorf("production instance initialization requires LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL")
		}
		email = "admin@localhost"
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address == "" {
		return "", fmt.Errorf("instance initialization requires a valid LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL")
	}
	return parsed.Address, nil
}

func (service *Service) acquire(ctx context.Context) (Lock, error) {
	if service == nil || service.deps.Locker == nil {
		return nil, fmt.Errorf("offline Admin locker is required")
	}
	return service.deps.Locker.Acquire(ctx)
}

type noopLock struct{}

func (noopLock) Release() error { return nil }

func (service *Service) acquireIf(ctx context.Context, required bool) (Lock, error) {
	if !required {
		return noopLock{}, nil
	}
	return service.acquire(ctx)
}

func days(value int) time.Duration {
	if value <= 0 {
		return 0
	}
	return time.Duration(value) * 24 * time.Hour
}

func writeAll(out io.Writer, contents []byte) error {
	written, err := out.Write(contents)
	if err == nil && written != len(contents) {
		return io.ErrShortWrite
	}
	return err
}
