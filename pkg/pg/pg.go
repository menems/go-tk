// Package pg opens a PostgreSQL connection pool from a DSN.
//
// New does not dial: pgxpool connects lazily on the first query. Whether the
// database is reachable is a readiness question, answered by pkg/health, to
// which *pgxpool.Pool's Ping method can be handed directly.
package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultMaxConns is the pool size New applies when WithMaxConns is not given.
const DefaultMaxConns = 10

// Option adjusts the pool configuration parsed from the DSN.
type Option func(*pgxpool.Config)

// WithMaxConns sets the maximum number of connections the pool may open.
func WithMaxConns(n int32) Option {
	return func(c *pgxpool.Config) { c.MaxConns = n }
}

// New parses dsn and opens a connection pool. The caller owns the pool and
// closes it.
//
// New owns the pool size: a pool_max_conns in the DSN is overwritten by
// DefaultMaxConns or by WithMaxConns. Two places to size a pool is one too
// many, and the option is the one a service can compute at boot.
func New(ctx context.Context, dsn string, opts ...Option) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pg: parse dsn: %w", err)
	}
	cfg.MaxConns = DefaultMaxConns

	for _, opt := range opts {
		opt(cfg)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: open pool: %w", err)
	}
	return pool, nil
}
