package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/flidai/leapview/internal/access"
	accesspg "github.com/flidai/leapview/internal/access/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type credential struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	Name         string `json:"name"`
}

type input struct {
	Credentials []credential `json:"credentials"`
}

var verifierParams = &argon2id.Params{Memory: 19 * 1024, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	var request input
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		return fmt.Errorf("decode credential recovery request: %w", err)
	}
	if len(request.Credentials) != 2 {
		return errors.New("exactly two deployment credentials are required")
	}
	key := strings.TrimSpace(os.Getenv("LEAPVIEW_TOKEN_HASH_KEY"))
	if len(key) < 32 {
		key = strings.TrimSpace(os.Getenv("LEAPVIEW_CSRF_KEY"))
	}
	if len(key) < 32 {
		return errors.New("runtime fingerprint key is unavailable")
	}
	pool, err := pgxpool.New(ctx, strings.TrimSpace(os.Getenv("LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL")))
	if err != nil {
		return fmt.Errorf("open control database: %w", err)
	}
	defer pool.Close()
	for _, item := range request.Credentials {
		principalID, err := uuid.Parse(strings.TrimSpace(item.ClientID))
		if err != nil {
			return fmt.Errorf("%s deployment client ID is not a canonical UUID", item.Name)
		}
		if strings.TrimSpace(item.ClientSecret) == "" {
			return fmt.Errorf("%s deployment client secret is empty", item.Name)
		}
		if strings.TrimSpace(item.Name) == "" {
			return errors.New("deployment credential name is empty")
		}
		var kind, status string
		if err := pool.QueryRow(ctx, `SELECT principal_type,status FROM access.principal WHERE id=$1`, principalID).Scan(&kind, &status); err != nil {
			return fmt.Errorf("resolve %s principal: %w", item.Name, err)
		}
		if kind != "service" || status != "active" {
			return fmt.Errorf("%s principal is not active", item.Name)
		}
		fingerprint := hmac.New(sha256.New, []byte(key))
		_, _ = fingerprint.Write([]byte(item.ClientSecret))
		verifier, err := argon2id.CreateHash(item.ClientSecret, verifierParams)
		if err != nil {
			return fmt.Errorf("hash %s credential: %w", item.Name, err)
		}
		var alreadyValid bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access.service_principal_secret WHERE service_principal_id=$1 AND secret_fingerprint=$2 AND revoked_at IS NULL AND expires_at > clock_timestamp())`, principalID, fingerprint.Sum(nil)).Scan(&alreadyValid); err != nil {
			return fmt.Errorf("inspect %s credential: %w", item.Name, err)
		}
		if alreadyValid {
			fmt.Printf("%s credential is already active\n", item.Name)
			continue
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin %s credential recovery: %w", item.Name, err)
		}
		if _, err = tx.Exec(ctx, `SET LOCAL access.maintenance='on'`); err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM access.service_principal_secret WHERE secret_fingerprint=$1 AND (revoked_at IS NOT NULL OR expires_at <= clock_timestamp())`, fingerprint.Sum(nil))
		}
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO access.service_principal_secret(id,service_principal_id,name,secret_fingerprint,verifier,expires_at) VALUES ($1,$2,$3,$4,$5,$6)`, uuid.Must(uuid.NewV7()), principalID, "demo deployment recovery", fingerprint.Sum(nil), []byte(verifier), time.Now().UTC().Add(180*24*time.Hour))
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("restore %s credential: %w", item.Name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s credential recovery: %w", item.Name, err)
		}
		fmt.Printf("restored %s credential\n", item.Name)
	}
	return ensureDeploymentPolicy(ctx, pool, []byte(key), request.Credentials)
}

func ensureDeploymentPolicy(ctx context.Context, pool *pgxpool.Pool, fingerprintKey []byte, credentials []credential) error {
	ids := map[string]string{}
	for _, item := range credentials {
		ids[item.Name] = strings.TrimSpace(item.ClientID)
	}
	var scope access.AuthorizationPolicyScope
	if err := pool.QueryRow(ctx, `
		SELECT target_id,project_id,environment
		FROM delivery.delivery_target
		ORDER BY updated_at DESC
		LIMIT 1`).Scan(&scope.TargetID, &scope.ProjectID, &scope.Environment); err != nil {
		return fmt.Errorf("resolve demo authorization scope: %w", err)
	}
	repository, err := accesspg.NewAccess(pool, accesspg.FingerprintConfig{Key: fingerprintKey})
	if err != nil {
		return fmt.Errorf("compose demo authorization repository: %w", err)
	}
	desired := []access.RoleBinding{
		{ID: "demo-publisher-contributor", Name: "Demo publisher authoring", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: ids["publisher"]}, Role: access.ProjectRoleContributor, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleContributor)},
		{ID: "demo-publisher-deployer", Name: "Demo publisher releases", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: ids["publisher"]}, Role: access.ProjectRoleDeployer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleDeployer)},
		{ID: "demo-release-admin", Name: "Demo release administrator", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: ids["release"]}, Role: access.ProjectRoleAdmin, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleAdmin)},
	}
	policy, err := repository.AuthorizationPolicy(ctx, scope)
	if err != nil {
		return fmt.Errorf("read demo authorization policy: %w", err)
	}
	for _, binding := range desired {
		present := false
		for _, current := range policy.RoleBindings {
			if current.ID == binding.ID {
				if current.Subject != binding.Subject || current.Role != binding.Role {
					return fmt.Errorf("demo role binding %s has incompatible identity", binding.ID)
				}
				present = true
				break
			}
		}
		if present {
			continue
		}
		policy, err = repository.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{
			Scope: scope, Binding: binding, ExpectedRevision: policy.Revision,
			IdempotencyKey: fmt.Sprintf("demo-credential-recovery-%s-%d", binding.ID, policy.Revision),
		})
		if err != nil {
			return fmt.Errorf("create demo role binding %s: %w", binding.ID, err)
		}
		fmt.Printf("created %s role binding\n", binding.ID)
	}
	return nil
}
