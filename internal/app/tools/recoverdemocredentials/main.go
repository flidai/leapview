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
	pool, err := pgxpool.New(ctx, strings.TrimSpace(os.Getenv("LEAPVIEW_POSTGRES_CONTROL_URL")))
	if err != nil {
		return fmt.Errorf("open control database: %w", err)
	}
	defer pool.Close()
	for _, item := range request.Credentials {
		principalID, err := uuid.Parse(strings.TrimSpace(item.ClientID))
		if err != nil || strings.TrimSpace(item.ClientSecret) == "" || strings.TrimSpace(item.Name) == "" {
			return errors.New("deployment credential input is invalid")
		}
		var kind, status string
		if err := pool.QueryRow(ctx, `SELECT kind,status FROM access.principal WHERE id=$1`, principalID).Scan(&kind, &status); err != nil {
			return fmt.Errorf("resolve %s principal: %w", item.Name, err)
		}
		if kind != "service_principal" || status != "active" {
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
		if _, err := pool.Exec(ctx, `INSERT INTO access.service_principal_secret(id,service_principal_id,name,secret_fingerprint,verifier,expires_at) VALUES ($1,$2,$3,$4,$5,$6)`, uuid.Must(uuid.NewV7()), principalID, "demo deployment recovery", fingerprint.Sum(nil), []byte(verifier), time.Now().UTC().Add(180*24*time.Hour)); err != nil {
			return fmt.Errorf("restore %s credential: %w", item.Name, err)
		}
		fmt.Printf("restored %s credential\n", item.Name)
	}
	return nil
}
