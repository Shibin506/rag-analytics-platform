// Package auth resolves a client's API key to its tenant.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrUnauthorized = errors.New("invalid or missing API key")

// Authenticator maps an API key to a tenant ID.
type Authenticator interface {
	Tenant(ctx context.Context, apiKey string) (string, error)
}

// PGAuthenticator looks keys up by their SHA-256 and caches hits for a minute,
// so the hot path does not touch the database.
type PGAuthenticator struct {
	pool *pgxpool.Pool
	ttl  time.Duration

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	tenant  string
	expires time.Time
}

func NewPG(pool *pgxpool.Pool) *PGAuthenticator {
	return &PGAuthenticator{pool: pool, ttl: time.Minute, cache: map[string]cached{}}
}

func HashKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

func (a *PGAuthenticator) Tenant(ctx context.Context, apiKey string) (string, error) {
	if apiKey == "" {
		return "", ErrUnauthorized
	}
	h := HashKey(apiKey)
	now := time.Now()

	a.mu.Lock()
	if c, ok := a.cache[h]; ok && now.Before(c.expires) {
		a.mu.Unlock()
		return c.tenant, nil
	}
	a.mu.Unlock()

	var tenant string
	err := a.pool.QueryRow(ctx, `SELECT id FROM tenants WHERE api_key_sha256 = $1`, h).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUnauthorized
	}
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	a.cache[h] = cached{tenant: tenant, expires: now.Add(a.ttl)}
	a.mu.Unlock()
	return tenant, nil
}

// Static is an in-memory Authenticator for tests and local tools.
type Static map[string]string

func (s Static) Tenant(_ context.Context, apiKey string) (string, error) {
	if t, ok := s[apiKey]; ok {
		return t, nil
	}
	return "", ErrUnauthorized
}
