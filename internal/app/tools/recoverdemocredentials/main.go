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
	ID           string `json:"id"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	Name         string `json:"name"`
}

type input struct {
	Credentials       []credential `json:"credentials"`
	Tokens            []credential `json:"tokens"`
	PlatformAdminMode string       `json:"platformAdminMode"`
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
	pool, err := pgxpool.New(ctx, strings.TrimSpace(os.Getenv("LEAPVIEW_POSTGRES_CONTROL_URL")))
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
		_, err = tx.Exec(ctx, `INSERT INTO access.service_principal_secret(id,service_principal_id,name,secret_fingerprint,verifier,expires_at) VALUES ($1,$2,$3,$4,$5,$6)`, uuid.Must(uuid.NewV7()), principalID, "demo deployment recovery", fingerprint.Sum(nil), []byte(verifier), time.Now().UTC().Add(180*24*time.Hour))
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("restore %s credential: %w", item.Name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s credential recovery: %w", item.Name, err)
		}
		fmt.Printf("restored %s credential\n", item.Name)
	}
	if err := ensureDeploymentPolicy(ctx, pool, []byte(key), request.Credentials); err != nil {
		return err
	}
	return updateTemporaryPlatformAdmin(ctx, pool, request.PlatformAdminMode, request.Credentials, request.Tokens)
}

func updateTemporaryPlatformAdmin(ctx context.Context, pool *pgxpool.Pool, mode string, credentials, tokens []credential) error {
	bindingIDs := map[string]uuid.UUID{
		"publisher": uuid.MustParse("01a0ac00-0000-7000-8000-000000000001"),
		"release":   uuid.MustParse("01a0ac00-0000-7000-8000-000000000002"),
	}
	switch mode {
	case "", "unchanged":
		return nil
	case "grant":
		for _, item := range credentials {
			principalID := uuid.MustParse(strings.TrimSpace(item.ClientID))
			if _, err := pool.Exec(ctx, `INSERT INTO access.platform_role_binding(id,principal_id,role) VALUES($1,$2,'platform_admin') ON CONFLICT DO NOTHING`, bindingIDs[item.Name], principalID); err != nil {
				return fmt.Errorf("grant temporary %s platform administration: %w", item.Name, err)
			}
			fmt.Printf("granted temporary %s platform administration\n", item.Name)
		}
		return nil
	case "revoke":
		for _, item := range credentials {
			if _, err := pool.Exec(ctx, `UPDATE access.platform_role_binding SET revoked_at=clock_timestamp() WHERE id=$1 AND revoked_at IS NULL`, bindingIDs[item.Name]); err != nil {
				return fmt.Errorf("revoke temporary %s platform administration: %w", item.Name, err)
			}
			fmt.Printf("revoked temporary %s platform administration\n", item.Name)
		}
		return nil
	case "grant-generation":
		return updateTemporaryGenerationRoles(ctx, pool, true, credentials)
	case "revoke-generation":
		return updateTemporaryGenerationRoles(ctx, pool, false, credentials)
	case "grant-api-tokens":
		return updateTemporaryAPITokens(ctx, pool, true, tokens)
	case "revoke-api-tokens":
		return updateTemporaryAPITokens(ctx, pool, false, tokens)
	default:
		return fmt.Errorf("unsupported temporary platform admin mode %q", mode)
	}
}

func updateTemporaryAPITokens(ctx context.Context, pool *pgxpool.Pool, grant bool, tokens []credential) error {
	if len(tokens) != 2 {
		return errors.New("exactly two temporary API tokens are required")
	}
	capabilities := map[string][]access.Capability{
		"publisher": {access.CapabilityResourceUse, access.CapabilityResourceRead, access.CapabilityResourceEdit, access.CapabilityResourcePublish},
		"release":   {access.CapabilityProjectAdmin},
	}
	key := strings.TrimSpace(os.Getenv("LEAPVIEW_TOKEN_HASH_KEY"))
	if len(key) < 32 {
		key = strings.TrimSpace(os.Getenv("LEAPVIEW_CSRF_KEY"))
	}
	for _, item := range tokens {
		tokenID, err := uuid.Parse(strings.TrimSpace(item.ID))
		if err != nil {
			return fmt.Errorf("%s temporary API token ID is invalid: %w", item.Name, err)
		}
		principalID, err := uuid.Parse(strings.TrimSpace(item.ClientID))
		if err != nil {
			return fmt.Errorf("%s temporary API token principal is invalid: %w", item.Name, err)
		}
		if !grant {
			if _, err := pool.Exec(ctx, `UPDATE access.api_token SET revoked_at=clock_timestamp() WHERE id=$1 AND revoked_at IS NULL`, tokenID); err != nil {
				return fmt.Errorf("revoke temporary %s API token: %w", item.Name, err)
			}
			fmt.Printf("revoked temporary %s API token\n", item.Name)
			continue
		}
		var kind, status string
		if err := pool.QueryRow(ctx, `SELECT principal_type,status FROM access.principal WHERE id=$1`, principalID).Scan(&kind, &status); err != nil {
			return fmt.Errorf("resolve temporary %s API token principal: %w", item.Name, err)
		}
		if kind != "user" || status != "active" {
			return fmt.Errorf("temporary %s API token principal is not an active user", item.Name)
		}
		secret := strings.TrimSpace(item.ClientSecret)
		if secret == "" {
			return fmt.Errorf("temporary %s API token secret is empty", item.Name)
		}
		fingerprint := hmac.New(sha256.New, []byte(key))
		_, _ = fingerprint.Write([]byte(secret))
		verifier, err := argon2id.CreateHash(secret, verifierParams)
		if err != nil {
			return fmt.Errorf("hash temporary %s API token: %w", item.Name, err)
		}
		encodedCapabilities, err := json.Marshal(capabilities[item.Name])
		if err != nil {
			return fmt.Errorf("encode temporary %s API token capabilities: %w", item.Name, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO access.api_token
				(id,principal_id,name,description,token_fingerprint,verifier,capabilities,expires_at)
			VALUES($1,$2,$3,'Bounded demo generation transition',$4,$5,$6::jsonb,clock_timestamp()+interval '30 minutes')`,
			tokenID, principalID, "demo transition "+item.Name, fingerprint.Sum(nil), []byte(verifier), encodedCapabilities); err != nil {
			return fmt.Errorf("grant temporary %s API token: %w", item.Name, err)
		}
		fmt.Printf("granted temporary %s API token\n", item.Name)
	}
	return nil
}

func updateTemporaryGenerationRoles(ctx context.Context, pool *pgxpool.Pool, grant bool, credentials []credential) error {
	ids := map[string]string{}
	for _, item := range credentials {
		ids[item.Name] = strings.TrimSpace(item.ClientID)
	}
	var projectID, environment, generationID string
	if err := pool.QueryRow(ctx, `
		SELECT t.project_id,t.environment,p.generation_id::text
		FROM delivery.delivery_target t
		JOIN delivery.delivery_active_pointer p ON p.target_id=t.target_id
		ORDER BY t.updated_at DESC
		LIMIT 1`).Scan(&projectID, &environment, &generationID); err != nil {
		return fmt.Errorf("resolve active demo generation: %w", err)
	}
	type binding struct {
		id, subject, role, name string
		capabilities            []access.Capability
	}
	bindings := []binding{
		{"demo-recovery-active-publisher-contributor", ids["publisher"], string(access.ProjectRoleContributor), "Temporary demo publisher authoring", access.ProjectRoleCapabilities(access.ProjectRoleContributor)},
		{"demo-recovery-active-publisher-deployer", ids["publisher"], string(access.ProjectRoleDeployer), "Temporary demo publisher releases", access.ProjectRoleCapabilities(access.ProjectRoleDeployer)},
		{"demo-recovery-active-release-admin", ids["release"], string(access.ProjectRoleAdmin), "Temporary demo release administrator", access.ProjectRoleCapabilities(access.ProjectRoleAdmin)},
	}
	for _, item := range bindings {
		if grant {
			capabilities, err := json.Marshal(item.capabilities)
			if err != nil {
				return fmt.Errorf("encode temporary generation capabilities: %w", err)
			}
			if _, err := pool.Exec(ctx, `
				INSERT INTO access.authorization_role_binding
					(id,project_id,environment,generation_id,subject_kind,subject_id,role,capabilities,name)
				VALUES($1,$2,$3,$4,'principal',$5,$6,$7::jsonb,$8)
				ON CONFLICT DO NOTHING`, item.id, projectID, environment, generationID, item.subject, item.role, capabilities, item.name); err != nil {
				return fmt.Errorf("grant temporary active-generation role %s: %w", item.id, err)
			}
			fmt.Printf("granted temporary active-generation role %s\n", item.id)
			continue
		}
		if _, err := pool.Exec(ctx, `
			UPDATE access.authorization_role_binding SET revoked_at=clock_timestamp()
			WHERE id=$1 AND revoked_at IS NULL`, item.id); err != nil {
			return fmt.Errorf("revoke temporary active-generation role %s: %w", item.id, err)
		}
		fmt.Printf("revoked temporary active-generation role %s\n", item.id)
	}
	return nil
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
